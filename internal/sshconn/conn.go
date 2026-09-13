// Package sshconn 管理唯一的 SSH 连接及其生命周期。
// 整个程序只建立一条 SSH 连接、只认证一次。所有隧道与控制通道都是这条连接上的
// channel——SSH 是多路复用协议，一条 TCP 连接可承载任意多个逻辑 channel
// 。
// Package sshconn manages the sole SSH connection and its lifecycle.
// The program creates and authenticates exactly one SSH connection. Every tunnel and
// control channel is multiplexed as a logical channel over that one TCP connection.
package sshconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"tunnelx/internal/logbuf"
	"tunnelx/internal/tunnel"
)

// 心跳参数。
// TCP 连接断开未必立刻报错——对端断电、拔网线、NAT 丢弃映射时，本地 socket 可能
// 长时间"看起来正常"。必须主动心跳。
// 15s×3 约 45 秒内发现断线。现有脚本是 30s×3（约 90 秒），对远程开发调试太慢；
// 更激进（如 10s×2）则易被网络抖动误判。
// Heartbeat parameters.
// A broken TCP connection may not fail immediately after power loss, cable removal,
// or an expired NAT mapping, so active heartbeats are required. 15s x 3 detects a
// failure in about 45 seconds; 30s x 3 is too slow for remote development, while more
// aggressive settings such as 10s x 2 risk false positives during network jitter.
const (
	KeepaliveInterval = 15 * time.Second
	KeepaliveMaxFail  = 3
	// keepaliveType 用 @openssh.com 后缀，服务端会以"不支持"回应——
	// 收到任何回应都证明连接存活，这正是 OpenSSH 自身的 keepalive 做法。
	// The @openssh.com suffix makes the server reply "unsupported"; any response proves
	// the connection is alive, matching OpenSSH's own keepalive technique.
	keepaliveType = "keepalive@openssh.com"

	DialTimeout = 15 * time.Second
)

// Dialer 描述建立一条连接所需的全部参数。
// Dialer contains all parameters required to establish a connection.
type Dialer struct {
	Addr       string // host:port
	User       string
	KeyPath    string
	KnownHosts string
	Prompt     HostKeyPrompt
	Log        *logbuf.Buffer
}

// Conn 是一条活动的 SSH 连接。
// Conn is an active SSH connection.
type Conn struct {
	client *ssh.Client

	mu     sync.Mutex
	closed bool
	// done 在连接失效时关闭，供隧道 goroutine 感知并退出。
	// done closes when the connection fails so tunnel goroutines can detect it and exit.
	done chan struct{}
	// cause 记录连接失效的原因，供上层分类。
	// cause records why the connection failed for classification by the caller.
	cause error
}

// Dial 建立连接并启动心跳。
// Dial establishes the connection and starts heartbeats.
func (d Dialer) Dial(ctx context.Context) (*Conn, error) {
	auth, err := loadKey(d.KeyPath)
	if err != nil {
		return nil, err
	}

	hostKey, err := hostKeyCallback(d.KnownHosts, d.Prompt)
	if err != nil {
		return nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            d.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: hostKey,
		Timeout:         DialTimeout,
	}

	// 用 DialContext 而非 ssh.Dial，以便用户中途取消时能立刻返回。
	// Use DialContext so cancellation returns immediately while connecting.
	var dialer net.Dialer
	rawConn, err := dialer.DialContext(ctx, "tcp", d.Addr)
	if err != nil {
		return nil, fmt.Errorf("连接 %s: %w", d.Addr, err)
	}

	// 握手本身也需超时保护：TCP 连上但对端不是 sshd 时，握手会一直挂着。
	// Bound the handshake too: it otherwise hangs if TCP connects to a non-SSH service.
	if err := rawConn.SetDeadline(time.Now().Add(DialTimeout)); err != nil {
		rawConn.Close()
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(rawConn, d.Addr, cfg)
	if err != nil {
		rawConn.Close()
		return nil, err
	}
	// 握手完成后清除 deadline，否则后续数据传输会被它掐断。
	// Clear the deadline after the handshake so it cannot terminate later data transfers.
	if err := rawConn.SetDeadline(time.Time{}); err != nil {
		sshConn.Close()
		return nil, err
	}

	c := &Conn{
		client: ssh.NewClient(sshConn, chans, reqs),
		done:   make(chan struct{}),
	}

	go c.keepalive(d.Log)
	go c.watch()

	return c, nil
}

// Client 暴露底层 client，供隧道建立 channel。
// Client exposes the underlying client so tunnels can open channels.
func (c *Conn) Client() *ssh.Client { return c.client }

// Done 在连接失效时关闭。
// Done closes when the connection fails.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Cause 返回连接失效的原因；连接仍存活时返回 nil。
// Cause returns the failure reason, or nil while the connection remains alive.
func (c *Conn) Cause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cause
}

// Close 主动关闭连接。可重复调用。
// Close actively closes the connection and is idempotent.
func (c *Conn) Close() error {
	return c.fail(nil)
}

func (c *Conn) fail(cause error) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.cause = cause
	close(c.done)
	c.mu.Unlock()

	return c.client.Close()
}

// watch 等待连接自然结束（对端关闭、网络中断）。
// watch waits for a natural connection termination, such as peer closure or network loss.
func (c *Conn) watch() {
	err := c.client.Wait()
	if err == nil {
		err = errors.New("连接已关闭")
	}
	c.fail(err)
}

// keepalive 周期性发送心跳，连续失败达阈值即判定连接已断。
// keepalive sends periodic probes and declares the connection dead at the failure threshold.
func (c *Conn) keepalive(log *logbuf.Buffer) {
	ticker := time.NewTicker(KeepaliveInterval)
	defer ticker.Stop()

	fails := 0
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			// wantReply=true：服务端必然回应（哪怕是"不支持"），
			// 收到回应即证明连接存活。
			// wantReply=true guarantees a server response, even "unsupported"; any reply
			// proves the connection is alive.
			_, _, err := c.client.SendRequest(keepaliveType, true, nil)
			if err == nil {
				fails = 0
				continue
			}

			fails++
			if log != nil {
				log.Warnf("conn", "心跳无响应（%d/%d）", fails, KeepaliveMaxFail)
			}
			if fails >= KeepaliveMaxFail {
				c.fail(fmt.Errorf("心跳连续 %d 次无响应，判定连接已断开", fails))
				return
			}
		}
	}
}

func loadKey(path string) (ssh.AuthMethod, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取私钥 %s: %w", path, err)
	}

	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		// 带 passphrase 的私钥需要额外交互，当前不支持——明确报错优于
		// 让用户对着"认证失败"猜原因。
		// Passphrase-protected keys require unsupported interaction; report this clearly
		// instead of leaving the user to diagnose a generic authentication failure.
		var passErr *ssh.PassphraseMissingError
		if errors.As(err, &passErr) {
			return nil, tunnel.Classify(fmt.Errorf(
				"私钥 %s 受密码保护，当前不支持；请使用无密码的私钥", path))
		}
		return nil, fmt.Errorf("解析私钥 %s: %w", path, err)
	}
	return ssh.PublicKeys(signer), nil
}
