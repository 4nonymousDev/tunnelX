package server

import (
	"errors"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"tunnelx/internal/proto"
	"tunnelx/internal/session"
	"tunnelx/internal/store"
)

func (s *Server) validateIdentity(fp, id string) error {
	if s.store != nil {
		ctx, cancel := s.dbContext()
		defer cancel()
		return s.store.ValidateIdentity(ctx, fp, id)
	}
	bound, ok := s.cfg.IdentityBindings[id]
	if !ok {
		return store.ErrIdentityUnbound
	}
	if bound != fp {
		return store.ErrIdentityMismatch
	}
	return nil
}

func (s *Server) handshake(sid string, conn *ssh.ServerConn, pc *proto.Conn) bool {
	env, raw, err := pc.Recv()
	if err != nil {
		return false
	}
	if env.V != proto.Version {
		s.sendErr(conn, pc, proto.CodeVersionMismatch, "请更新客户端程序，保留原配置和密钥")
		return false
	}
	var h proto.Hello
	if env.Type != proto.TypeHello || proto.Decode(raw, &h) != nil || !validHello(h) {
		s.sendErr(conn, pc, proto.CodeBadRequest, "hello 字段无效")
		return false
	}
	current, ok := s.sessions.Get(sid)
	if !ok {
		return false
	}
	version := s.policy.Version()
	if s.rejectClientVersion(conn, pc, h.ClientVersion) {
		s.sessions.Disconnect(sid, proto.CodeUpdateRequired)
		return false
	}
	if s.store != nil {
		ctx, cancel := s.dbContext()
		err = s.store.RecordIdentityClaim(ctx, current.Fingerprint, h.ID, h.Name, current.RemoteIP)
		cancel()
		if err != nil {
			s.sendErr(conn, pc, proto.CodeInternalError, "无法记录身份核验请求")
			return false
		}
	}
	if err = s.validateIdentity(current.Fingerprint, h.ID); err != nil {
		code := proto.CodeIdentityMismatch
		if errors.Is(err, store.ErrIdentityUnbound) {
			code = proto.CodeIdentityUnverified
		}
		s.sendErr(conn, pc, code, "设备标识与公钥需要管理员核验")
		return false
	}
	if s.store != nil {
		s.mu.Lock()
		auditID := s.auditIDs[sid]
		s.mu.Unlock()
		now := time.Now().UTC()
		ctx, cancel := s.dbContext()
		err = s.store.UpsertClient(ctx, store.Seen{Fingerprint: current.Fingerprint, ClientID: h.ID, ReportedName: h.Name, Role: h.Role, Version: h.ClientVersion, At: now})
		if err == nil {
			err = s.store.CompleteHello(ctx, auditID, store.ConnectionAudit{ClientID: h.ID, HelloAt: &now, Result: "online", ReportedName: h.Name, Role: h.Role, Version: h.ClientVersion})
		}
		cancel()
		if err != nil {
			s.sendErr(conn, pc, proto.CodeInternalError, "无法持久化会话")
			return false
		}
		s.notifyAudit(auditID)
	}
	admitted, err := s.policy.AdmitVersion(current.Fingerprint, version, func() error {
		if s.clientVersionBlocked(h.ClientVersion) {
			return errors.New("client update required")
		}
		return s.sessions.CompleteHello(sid, session.Hello{ClientID: h.ID, Name: h.Name, Role: h.Role, Version: h.ClientVersion})
	})
	if !admitted || err != nil {
		if s.rejectClientVersion(conn, pc, h.ClientVersion) {
			s.sessions.Disconnect(sid, proto.CodeUpdateRequired)
			return false
		}
		code := proto.CodeServerBusy
		if errors.Is(err, session.ErrDuplicateClient) {
			code = proto.CodeDuplicateID
		}
		s.sendErr(conn, pc, code, "会话登记失败或授权状态已改变")
		return false
	}
	s.cancelHelloTimer(sid)
	if s.sendControl(conn, pc, proto.HelloOK{V: proto.Version, Type: proto.TypeHelloOK, ServerVersion: s.cfg.Version, SessionID: sid, Fingerprint: current.Fingerprint, UpdatePolicy: s.clientUpdateStatus(h.ClientVersion)}) != nil {
		return false
	}
	if s.adminAPI != nil {
		s.adminAPI.Publish("sessions.changed", map[string]string{"session_id": sid})
		s.adminAPI.Publish("clients.changed", map[string]string{"fingerprint": current.Fingerprint})
	}
	return true
}

func validHello(h proto.Hello) bool {
	return store.ValidClientID(h.ID) && utf8.ValidString(h.Name) && utf8.RuneCountInString(h.Name) <= 200 && utf8.RuneCountInString(h.ClientVersion) <= 100 && (h.Role == proto.RoleExporter || h.Role == proto.RoleImporter)
}
