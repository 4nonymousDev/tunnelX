package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func detachBackground(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	return nil
}
