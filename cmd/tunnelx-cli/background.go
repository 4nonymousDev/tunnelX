package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/localapi"
)

const backgroundStartupEnv = "TUNNELX_INTERNAL_BACKGROUND_STARTUP"
const backgroundStartupTimeout = 15 * time.Second

type startupMessage struct {
	Address string `json:"address,omitempty"`
	Token   string `json:"token"`
	Error   string `json:"error,omitempty"`
}

// A private, one-shot handshake distinguishes a started core from a merely
// spawned process. No shell, inherited terminal handles, or PID file is needed.
func startBackground(cfg *config.Config, endpoint, fingerprint string, confirmViaAPI bool) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	stateDir, err := cfg.StateDir()
	if err != nil {
		return err
	}
	endpoint, err = filepath.Abs(endpoint)
	if err != nil {
		return err
	}
	if client, err := localapi.NewClient(endpoint); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = client.Snapshot(ctx)
		cancel()
		if err == nil {
			return fmt.Errorf("TunnelX核心已在运行")
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("创建后台启动通道: %w", err)
	}
	defer listener.Close()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	startup := startupMessage{Address: listener.Addr().String(), Token: hex.EncodeToString(secret[:])}
	payload, err := json.Marshal(startup)
	if err != nil {
		return err
	}
	args := []string{"run", "--config", cfg.Path(), "--state-dir", stateDir, "--endpoint", endpoint}
	if fingerprint != "" {
		args = append(args, "--accept-host-key", fingerprint)
	} else {
		// No terminal is available; pending confirmations remain accessible via
		// status/confirm rather than reading from the null device.
		confirmViaAPI = true
	}
	if confirmViaAPI {
		args = append(args, "--confirm-via-api")
	}
	cmd := exec.Command(executable, args...)
	cmd.Env = append(withoutBackgroundStartup(os.Environ()), backgroundStartupEnv+"="+string(payload))
	// Nil standard streams are attached to os.DevNull by os/exec.
	if err := detachBackground(cmd); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动后台进程: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := make(chan error, 1)
	go func() { ready <- receiveBackgroundStartup(listener, startup.Token) }()
	timer := time.NewTimer(backgroundStartupTimeout)
	defer timer.Stop()
	select {
	case err = <-ready:
		if err == nil {
			fmt.Printf("TunnelX 已在后台启动 (PID %d)，日志: %s\n", cmd.Process.Pid, filepath.Join(stateDir, "tunnelx.log"))
			return nil
		}
	case exitErr := <-done:
		// The child may have sent a useful startup error just before exiting.
		_ = listener.Close()
		err = <-ready
		if err == nil {
			err = fmt.Errorf("后台进程在启动期间退出: %v", exitErr)
		}
		return err
	case <-timer.C:
		err = fmt.Errorf("后台启动超时 (%s)", backgroundStartupTimeout)
	}
	_ = cmd.Process.Kill()
	<-done
	return err
}

func withoutBackgroundStartup(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, backgroundStartupEnv) {
			result = append(result, entry)
		}
	}
	return result
}

func receiveBackgroundStartup(listener net.Listener, token string) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("后台启动通道关闭: %w", err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var message startupMessage
		err = json.NewDecoder(io.LimitReader(conn, 64<<10)).Decode(&message)
		if err != nil || message.Token != token {
			conn.Close()
			continue
		}
		// Ack ensures the parent has the result before the child can exit.
		_, err = conn.Write([]byte{1})
		conn.Close()
		if message.Error != "" {
			return fmt.Errorf("后台启动失败: %s", message.Error)
		}
		return err
	}
}

func backgroundStartupReporter() (func(error) error, error) {
	raw, ok := os.LookupEnv(backgroundStartupEnv)
	if !ok {
		return nil, nil
	}
	_ = os.Unsetenv(backgroundStartupEnv)
	var startup startupMessage
	if err := json.Unmarshal([]byte(raw), &startup); err != nil {
		return nil, fmt.Errorf("无效的后台启动信息: %w", err)
	}
	host, _, err := net.SplitHostPort(startup.Address)
	if err != nil || host != "127.0.0.1" || len(startup.Token) != 64 {
		return nil, fmt.Errorf("无效的后台启动通道")
	}
	reported := false
	return func(startErr error) error {
		if reported {
			return nil
		}
		reported = true
		conn, err := net.DialTimeout("tcp4", startup.Address, time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		message := startupMessage{Token: startup.Token}
		if startErr != nil {
			message.Error = startErr.Error()
		}
		if err := json.NewEncoder(conn).Encode(message); err != nil {
			return err
		}
		var ack [1]byte
		_, err = conn.Read(ack[:])
		return err
	}, nil
}
