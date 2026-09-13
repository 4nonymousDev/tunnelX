package main

import (
	"os"
	"strings"
	"testing"
)

func TestConfirmationForRunUsesControlAPI(t *testing.T) {
	handler, err := confirmationForRun("", true, os.Stdin)
	if err != nil {
		t.Fatalf("confirmationForRun() error = %v", err)
	}
	if handler != nil {
		t.Fatalf("confirmationForRun() = %#v，API 确认模式必须让 core.Service 管理待确认请求", handler)
	}
}

func TestConfirmationForRunRejectsConflictingModes(t *testing.T) {
	_, err := confirmationForRun("SHA256:test", true, os.Stdin)
	if err == nil {
		t.Fatal("同时启用 API 确认和固定指纹时应返回错误")
	}
	if !strings.Contains(err.Error(), "不能与") {
		t.Fatalf("error = %q，应解释两个确认选项冲突", err)
	}
}
