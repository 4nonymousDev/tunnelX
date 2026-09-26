package server

import (
	"context"
	"errors"
	"io"
	"time"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/proto"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

func (s *Server) acquire(visitorID string, target proto.Target) (*session.Lease, error) {
	visitor, ok := s.sessions.Get(visitorID)
	if !ok {
		return nil, session.ErrInvalidTarget
	}
	version := s.policy.Version()
	if s.policy.Blocked(target.Fingerprint) || !s.fingerprintAuthorized(visitor.Fingerprint) || !s.fingerprintAuthorized(target.Fingerprint) {
		return nil, session.ErrInvalidTarget
	}
	if s.validateIdentity(visitor.Fingerprint, visitor.ClientID) != nil || s.validateIdentity(target.Fingerprint, target.ClientID) != nil {
		return nil, session.ErrInvalidTarget
	}
	var lease *session.Lease
	admitted, err := s.policy.AdmitVersion(visitor.Fingerprint, version, func() error {
		var e error
		lease, e = s.sessions.Acquire(visitorID, target)
		return e
	})
	if !admitted {
		return nil, session.ErrInvalidTarget
	}
	return lease, err
}

// boundedChannel ensures cancellation cannot wait indefinitely for a peer that
// stopped reading SSH packets. Closing the transport interrupts its channel I/O.
type boundedChannel struct {
	ssh.Channel
	conn    *ssh.ServerConn
	timeout time.Duration
}

func (c boundedChannel) Close() error {
	timer := time.AfterFunc(c.timeout, func() { _ = c.conn.Close() })
	defer timer.Stop()
	return c.Channel.Close()
}

func (s *Server) forward(visitor *ssh.ServerConn, newCh ssh.NewChannel, lease *session.Lease, rate *rateBucket) {
	defer lease.Close()
	select {
	case <-lease.Done():
		s.rejectChannel(visitor, newCh, ssh.Prohibited, "目标已撤销")
		return
	default:
	}
	owner, ok := lease.Connection.(*ssh.ServerConn)
	if !ok {
		s.rejectChannel(visitor, newCh, ssh.ConnectionFailed, "目标离线")
		return
	}
	payload, err := proto.EncodeOpen(lease.Target)
	if err != nil {
		s.rejectChannel(visitor, newCh, ssh.Prohibited, "目标无效")
		return
	}
	type opened struct {
		ch   ssh.Channel
		reqs <-chan *ssh.Request
		err  error
	}
	result := make(chan opened, 1)
	go func() {
		ch, reqs, e := owner.OpenChannel(proto.ForwardChannelType, payload)
		result <- opened{ch, reqs, e}
	}()
	timer := time.NewTimer(s.cfg.OpenTimeout)
	defer timer.Stop()
	var out opened
	select {
	case out = <-result:
	case <-timer.C:
		_ = owner.Close()
		out = <-result
		if out.ch != nil {
			_ = out.ch.Close()
		}
		s.rejectChannel(visitor, newCh, ssh.ConnectionFailed, "目标应答超时")
		return
	case <-lease.Done():
		// A departing visitor cannot disconnect a responsive exporter. Keep this
		// pending-open reservation until its reply or the original deadline.
		select {
		case out = <-result:
		case <-timer.C:
			_ = owner.Close()
			out = <-result
		}
		if out.ch != nil {
			_ = (boundedChannel{out.ch, owner, s.cfg.WriteTimeout}).Close()
		}
		s.rejectChannel(visitor, newCh, ssh.Prohibited, "目标已撤销")
		return
	}
	if out.err != nil {
		s.rejectChannel(visitor, newCh, ssh.ConnectionFailed, "目标拒绝连接")
		return
	}
	if !lease.Attach(boundedChannel{out.ch, owner, s.cfg.WriteTimeout}) {
		s.rejectChannel(visitor, newCh, ssh.Prohibited, "目标已撤销")
		return
	}
	s.mu.Lock()
	ownerRate := s.requestRates[owner]
	s.mu.Unlock()
	if ownerRate == nil {
		s.rejectChannel(visitor, newCh, ssh.ConnectionFailed, "目标已离线")
		return
	}
	go s.discardRequests(owner, out.reqs, ownerRate)
	if !s.sessions.Validate(lease) {
		s.rejectChannel(visitor, newCh, ssh.Prohibited, "目标已改变")
		return
	}
	var auditID int64
	if s.store != nil {
		ctx, cancel := s.dbContext()
		auditID, err = s.store.StartAccess(ctx, store.AccessAudit{VisitorSessionID: lease.Visitor.ID, VisitorFingerprint: lease.Visitor.Fingerprint, VisitorClientID: lease.Visitor.ClientID, TargetSessionID: lease.Owner.ID, TargetFingerprint: lease.Owner.Fingerprint, TargetClientID: lease.Owner.ClientID, TargetTunnelID: lease.Tunnel.ID, TargetTunnelName: lease.Tunnel.Name, Result: "started"})
		cancel()
		if err != nil {
			s.rejectChannel(visitor, newCh, ssh.ConnectionFailed, "无法记录访问审计")
			return
		}
		s.notifyAudit(auditID)
	}
	outcome, reason := "failed", "target_changed"
	var sent, received int64
	defer func() {
		if auditID > 0 {
			ended := time.Now()
			s.queueAudit(func(ctx context.Context) {
				if err := s.store.FinishAccess(ctx, auditID, outcome, reason, sent, received, ended); err != nil {
					s.limitedLog("访问收尾审计失败: %v", err)
				}
				s.notifyAudit(auditID)
			})
		}
	}()
	if !s.sessions.Validate(lease) {
		s.rejectChannel(visitor, newCh, ssh.Prohibited, "目标已改变")
		return
	}
	in, reqs, err := s.acceptChannel(visitor, newCh)
	if err != nil {
		reason = "accept_failed"
		return
	}
	if !lease.Attach(boundedChannel{in, visitor, s.cfg.WriteTimeout}) {
		return
	}
	go s.discardRequests(visitor, reqs, rate)
	type copied struct {
		n   int64
		err error
	}
	copying := make(chan copied, 1)
	go func() {
		n, e := io.Copy(out.ch, in)
		if e != nil {
			lease.Cancel()
		}
		s.closeWrite(owner, out.ch)
		copying <- copied{n, e}
	}()
	received, err = io.Copy(in, out.ch)
	if err != nil {
		lease.Cancel()
	}
	s.closeWrite(visitor, in)
	other := <-copying
	sent = other.n
	if err == nil && other.err == nil {
		outcome, reason = "completed", ""
	} else {
		reason = "transfer_interrupted"
	}
}

func (s *Server) rejectChannel(conn *ssh.ServerConn, ch ssh.NewChannel, reason ssh.RejectionReason, msg string) {
	timer := time.AfterFunc(s.cfg.WriteTimeout, func() { _ = conn.Close() })
	defer timer.Stop()
	_ = ch.Reject(reason, msg)
}

func (s *Server) closeWrite(conn *ssh.ServerConn, ch ssh.Channel) {
	timer := time.AfterFunc(s.cfg.WriteTimeout, func() { _ = conn.Close() })
	defer timer.Stop()
	_ = ch.CloseWrite()
}
func (s *Server) acceptChannel(conn *ssh.ServerConn, ch ssh.NewChannel) (ssh.Channel, <-chan *ssh.Request, error) {
	timer := time.AfterFunc(s.cfg.WriteTimeout, func() { _ = conn.Close() })
	defer timer.Stop()
	return ch.Accept()
}
func (s *Server) discardRequests(conn *ssh.ServerConn, reqs <-chan *ssh.Request, rate *rateBucket) {
	for req := range reqs {
		if !rate.allow() {
			_ = conn.Close()
			return
		}
		if req.WantReply {
			timer := time.AfterFunc(s.cfg.WriteTimeout, func() { _ = conn.Close() })
			err := req.Reply(false, nil)
			timer.Stop()
			if err != nil && !errors.Is(err, io.EOF) {
				_ = conn.Close()
				return
			}
		}
	}
}
