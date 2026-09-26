package sshconn

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/enrollment"
)

const EnrollmentTimeout = 60 * time.Second

// Enroll proves ownership of the existing device key over a temporary SSH connection.
// Passwords are used only here; the ordinary tunnel connection remains public-key authenticated.
func (d Dialer) Enroll(ctx context.Context, username, password, clientID, name string) error {
	username = enrollment.NormalizeUsername(username)
	if !enrollment.ValidUsername(username) || !enrollment.ValidLoginPassword(password) {
		return errors.New("请输入有效账号和密码")
	}
	ctx, cancel := context.WithTimeout(ctx, EnrollmentTimeout)
	defer cancel()
	signer, err := loadSigner(d.KeyPath)
	if err != nil {
		return err
	}
	client, closeClient, err := d.dialEnrollment(ctx, username, password)
	if err != nil {
		return err
	}
	defer closeClient()
	channel, requests, err := client.OpenChannel(enrollment.ChannelType, nil)
	if err != nil {
		return errors.New("服务器未接受设备登录，请确认服务端已更新且账号可用")
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	sig, err := signer.Sign(rand.Reader, enrollment.SigningPayload(client.SessionID(), username, clientID, signer.PublicKey()))
	if err != nil {
		return errors.New("无法签署设备登录请求")
	}
	request := enrollment.Request{ClientID: clientID, PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), Name: name, Signature: ssh.Marshal(sig)}
	if err := enrollment.Write(channel, request); err != nil {
		return errors.New("设备登录请求发送失败")
	}
	var response enrollment.Response
	if err := enrollment.Read(channel, &response); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("设备登录未完成，请检查服务器状态后重试")
	}
	if !response.OK {
		return errors.New("设备登录被拒绝，请联系管理员检查账号和设备授权")
	}
	return nil
}

func (d Dialer) dialEnrollment(ctx context.Context, username, password string) (*ssh.Client, func(), error) {
	for attempt := 0; attempt < 2; attempt++ {
		newTrust, passwordSent := false, false
		// The prompt can take longer than the server's bounded handshake timeout.
		// Track the accepted trust decision in the synchronous verifier, not in the
		// prompt goroutine, which may outlive cancellation.
		verify, err := hostKeyCallbackContext(ctx, d.KnownHosts, d.Prompt)
		if err != nil {
			return nil, nil, err
		}
		// Checking the file again after a successful verification distinguishes a
		// newly stored host from an already pinned one without weakening matching.
		known, err := hostKeyCallbackContext(ctx, d.KnownHosts, nil)
		if err != nil {
			return nil, nil, err
		}
		var hostErr error
		cfg := &ssh.ClientConfig{
			User: username,
			Auth: []ssh.AuthMethod{ssh.PasswordCallback(func() (string, error) {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				passwordSent = true
				return password, nil
			})},
			HostKeyCallback: func(host string, remote net.Addr, key ssh.PublicKey) error {
				prompted := known(host, remote, key) != nil
				hostErr = verify(host, remote, key)
				newTrust = prompted && hostErr == nil
				return hostErr
			},
		}
		raw, err := (&net.Dialer{Timeout: DialTimeout}).DialContext(ctx, "tcp", d.Addr)
		if err != nil {
			return nil, nil, fmt.Errorf("连接登录服务器: %w", err)
		}
		stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
		closeRaw := func() { stop(); _ = raw.Close() }
		deadline, _ := ctx.Deadline()
		if err := raw.SetDeadline(deadline); err != nil {
			closeRaw()
			return nil, nil, err
		}
		conn, chans, reqs, err := ssh.NewClientConn(raw, d.Addr, cfg)
		if err == nil {
			client := ssh.NewClient(conn, chans, reqs)
			return client, func() { closeRaw(); _ = client.Close() }, nil
		}
		closeRaw()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if hostErr != nil {
			return nil, nil, hostErr
		}
		if attempt == 0 && newTrust && !passwordSent {
			continue
		}
		// A peer's disconnect reason may contain credential text; never relay it.
		return nil, nil, errors.New("登录失败，请检查账号、密码和服务器状态")
	}
	return nil, nil, errors.New("登录失败，请重试")
}
