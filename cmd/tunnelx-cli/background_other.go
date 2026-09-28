//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

func detachBackground(cmd *exec.Cmd) error {
	return fmt.Errorf("--bg 不支持当前平台 %s", runtime.GOOS)
}
