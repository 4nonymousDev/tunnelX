package server

import (
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/proto"
	"tunnelx/internal/session"
)

func (s *Server) serveControl(sid string, conn *ssh.ServerConn, ch ssh.Channel, rate *rateBucket) {
	pc := proto.NewConn(ch)
	if !s.handshake(sid, conn, pc) {
		return
	}
	updates, unsub := s.sessions.Subscribe()
	defer unsub()
	done := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	defer func() { close(done); _ = conn.Close(); writer.Wait() }()
	go func() {
		defer writer.Done()
		defer conn.Close()
		if s.pushRegistry(sid, conn, pc) != nil {
			return
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var lastGeneration int64
		for {
			select {
			case <-done:
				return
			case <-updates:
				if s.pushRegistry(sid, conn, pc) != nil {
					return
				}
			case <-ticker.C:
				current, ok := s.sessions.Get(sid)
				if !ok {
					return
				}
				if s.rejectClientVersion(conn, pc, current.Version) {
					s.sessions.Disconnect(sid, "update_required")
					return
				}
				if p := s.clientUpdates.Load(); p != nil && p.Generation != lastGeneration {
					lastGeneration = p.Generation
					if s.pushRegistry(sid, conn, pc) != nil {
						return
					}
				}
			}
		}
	}()
	for {
		env, raw, err := pc.Recv()
		if err != nil {
			return
		}
		if !rate.allow() {
			s.sendErr(conn, pc, proto.CodeServerBusy, "请求过于频繁")
			return
		}
		if env.V != proto.Version {
			s.sendErr(conn, pc, proto.CodeVersionMismatch, "协议版本不兼容")
			return
		}
		if env.Type != proto.TypePublish {
			s.sendErr(conn, pc, proto.CodeBadRequest, "未知消息类型")
			continue
		}
		var p proto.Publish
		if len(raw) > s.cfg.Limits.PublishBytes {
			s.sendErr(conn, pc, proto.CodePublishTooLarge, "发布内容超出限制")
			continue
		}
		if proto.Decode(raw, &p) != nil || !validTunnelSpecs(p.Tunnels) || len(p.Tunnels) > s.cfg.Limits.ExportsPerSession {
			s.sendErr(conn, pc, proto.CodeBadRequest, "发布字段无效或数量超出限制")
			continue
		}
		current, ok := s.sessions.Get(sid)
		if !ok {
			return
		}
		version := s.policy.Version()
		if s.validateIdentity(current.Fingerprint, current.ClientID) != nil {
			return
		}
		tunnels := make([]session.Tunnel, len(p.Tunnels))
		for i, t := range p.Tunnels {
			tunnels[i] = session.Tunnel{ID: t.TunnelID, Name: t.Name, SrcHost: t.SrcHost, SrcPort: t.SrcPort}
		}
		var closeStale func()
		admitted, err := s.policy.AdmitVersion(current.Fingerprint, version, func() error {
			if s.clientVersionBlocked(current.Version) {
				return errors.New("client update required")
			}
			var e error
			closeStale, e = s.sessions.PublishDeferred(sid, tunnels)
			return e
		})
		if closeStale != nil {
			closeStale()
		}
		if !admitted {
			return
		}
		if err != nil {
			if s.rejectClientVersion(conn, pc, current.Version) {
				s.sessions.Disconnect(sid, proto.CodeUpdateRequired)
				return
			}
			code := proto.CodeBadRequest
			if errors.Is(err, session.ErrCapacity) {
				code = proto.CodeRegistryCapacity
			}
			s.sendErr(conn, pc, code, "发布未生效：超出容量或会话状态已改变")
			continue
		}
		if s.sendControl(conn, pc, proto.PublishOK{V: proto.Version, Type: proto.TypePublishOK, Targets: s.sessions.Targets(sid)}) != nil {
			return
		}
		if s.adminAPI != nil {
			s.adminAPI.Publish("sessions.changed", map[string]string{"session_id": sid})
		}
	}
}

func validTunnelSpecs(xs []proto.TunnelSpec) bool {
	seen := map[string]bool{}
	for _, t := range xs {
		if t.TunnelID == "" || seen[t.TunnelID] || t.SrcPort < 1 || t.SrcPort > 65535 || t.RemotePort != 0 {
			return false
		}
		if !utf8.ValidString(t.TunnelID) || utf8.RuneCountInString(t.TunnelID) > 128 || utf8.RuneCountInString(t.Name) > 200 || utf8.RuneCountInString(t.SrcHost) > 255 {
			return false
		}
		seen[t.TunnelID] = true
	}
	return true
}
func (s *Server) sendControl(conn *ssh.ServerConn, pc *proto.Conn, msg any) error {
	timer := time.AfterFunc(s.cfg.WriteTimeout, func() { _ = conn.Close() })
	defer timer.Stop()
	err := pc.Send(msg)
	if err != nil {
		_ = conn.Close()
	}
	return err
}
func (s *Server) sendErr(conn *ssh.ServerConn, pc *proto.Conn, code, msg string) {
	_ = s.sendControl(conn, pc, proto.Error{V: proto.Version, Type: proto.TypeError, Code: code, Msg: msg})
}
func (s *Server) pushRegistry(sid string, conn *ssh.ServerConn, pc *proto.Conn) error {
	current, ok := s.sessions.Get(sid)
	if !ok {
		return errors.New("session closed")
	}
	if s.rejectClientVersion(conn, pc, current.Version) {
		s.sessions.Disconnect(sid, proto.CodeUpdateRequired)
		return errors.New("client update required")
	}
	return s.sendControl(conn, pc, proto.Registry{V: proto.Version, Type: proto.TypeRegistry, Entries: s.sessions.RegistrySnapshot(), UpdatePolicy: s.clientUpdateStatus(current.Version)})
}
