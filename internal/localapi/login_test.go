package localapi

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/core"
)

func TestLoginRequiresLocalTokenAndDoesNotEchoCredentialPayload(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{ID: "old-device"}
	cfg.SetPathForTest(filepath.Join(dir, "config.json"))
	service, err := core.New(cfg, core.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	server, err := Start(service, filepath.Join(dir, EndpointFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	}()
	const secret = "must-never-be-echoed"
	for _, tc := range []struct {
		body   string
		auth   bool
		status int
	}{
		{`{"username":"alice","password":"` + secret + `"}`, false, http.StatusUnauthorized},
		{`{"username":"alice","password":"valid","` + secret + `":"unknown-field"}`, true, http.StatusBadRequest},
		{`{"username":"alice","password":"valid"} {"password":"` + secret + `"}`, true, http.StatusBadRequest},
		{`{"username":"alice","password":"` + secret + strings.Repeat("x", 4096) + `"}`, true, http.StatusBadRequest},
	} {
		req, err := http.NewRequest(http.MethodPost, server.Endpoint().Address+"/v1/login", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if tc.auth {
			req.Header.Set("Authorization", "Bearer "+server.Endpoint().Token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status || strings.Contains(string(data), secret) {
			t.Fatalf("unsafe response status=%d body=%s", resp.StatusCode, data)
		}
	}
}
