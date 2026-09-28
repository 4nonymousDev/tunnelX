//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"os/exec"
	"syscall"
)

func detachBackground(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
