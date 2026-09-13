// Package localapi exposes the headless core to local, potentially non-Go frontends.
package localapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"tunnelx/internal/config"
	"tunnelx/internal/core"
	"tunnelx/internal/logbuf"
	"tunnelx/internal/manager"
	"tunnelx/internal/proto"
	"tunnelx/internal/tunnel"
)

const APIVersion = 1
const EndpointFileName = ".tunnelx-control.json"

type Endpoint struct {
	Version int    `json:"version"`
	Address string `json:"address"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
}

type ConnectionDTO struct {
	State   string    `json:"state"`
	Reason  string    `json:"reason,omitempty"`
	RetryAt time.Time `json:"retry_at,omitempty"`
}

type TunnelDTO struct {
	Index      int             `json:"index"`
	Config     TunnelConfigDTO `json:"config"`
	State      string          `json:"state"`
	Reason     string          `json:"reason,omitempty"`
	RetryAt    time.Time       `json:"retry_at,omitempty"`
	RemotePort int             `json:"remote_port,omitempty"`
}

type TunnelConfigDTO struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	LocalHost    string `json:"local_host,omitempty"`
	LocalPort    int    `json:"local_port,omitempty"`
	PeerID       string `json:"peer_id,omitempty"`
	PeerTunnelID string `json:"peer_tunnel_id,omitempty"`
	PeerName     string `json:"peer_name,omitempty"`
	PeerSrcPort  int    `json:"peer_src_port,omitempty"`
	ListenPort   int    `json:"listen_port,omitempty"`
}

type LogDTO struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

type ConfirmationDTO struct {
	ID          uint64   `json:"id"`
	Kind        string   `json:"kind"`
	Host        string   `json:"host,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Path        string   `json:"path,omitempty"`
	Readers     []string `json:"readers,omitempty"`
	Message     string   `json:"message"`
}

type SnapshotDTO struct {
	Version    int               `json:"version"`
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	ServerAddr string            `json:"server_addr"`
	ServerUser string            `json:"server_user"`
	KeyPath    string            `json:"key_path"`
	Connection ConnectionDTO     `json:"connection"`
	Tunnels    []TunnelDTO       `json:"tunnels"`
	Registry   []RegistryDTO     `json:"registry"`
	Logs       []LogDTO          `json:"logs,omitempty"`
	Pending    []ConfirmationDTO `json:"pending_confirmations,omitempty"`
}

type RegistryDTO struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	TunnelID      string    `json:"tunnel_id,omitempty"`
	SourceHost    string    `json:"source_host,omitempty"`
	SourcePort    int       `json:"source_port"`
	TunnelName    string    `json:"tunnel_name"`
	RemotePort    int       `json:"remote_port"`
	Since         time.Time `json:"since"`
	ClientVersion string    `json:"client_version"`
}

type EventDTO struct {
	Version      int              `json:"version"`
	Seq          uint64           `json:"seq"`
	Time         time.Time        `json:"time"`
	Kind         string           `json:"kind"`
	Log          *LogDTO          `json:"log,omitempty"`
	Confirmation *ConfirmationDTO `json:"confirmation,omitempty"`
}

type Server struct {
	service      *core.Service
	endpointPath string
	endpoint     Endpoint
	listener     net.Listener
	httpServer   *http.Server
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

func DefaultEndpointPath(cfg *config.Config) (string, error) {
	dir, err := cfg.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, EndpointFileName), nil
}

func Start(service *core.Service, endpointPath string) (*Server, error) {
	if service == nil {
		return nil, errors.New("core service不能为空")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("监听本地控制端口: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		listener.Close()
		return nil, fmt.Errorf("生成控制令牌: %w", err)
	}
	s := &Server{
		service: service, endpointPath: endpointPath, listener: listener,
		endpoint: Endpoint{Version: APIVersion, Address: "http://" + listener.Addr().String(), Token: hex.EncodeToString(tokenBytes), PID: os.Getpid()},
		shutdown: make(chan struct{}),
	}
	endpointFile, err := reserveEndpoint(endpointPath)
	if err != nil {
		listener.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/snapshot", s.auth(s.snapshot))
	mux.HandleFunc("POST /v1/connect", s.auth(s.connect))
	mux.HandleFunc("POST /v1/disconnect", s.auth(s.disconnect))
	mux.HandleFunc("POST /v1/shutdown", s.auth(s.shutdownCore))
	mux.HandleFunc("GET /v1/events", s.auth(s.events))
	mux.HandleFunc("POST /v1/tunnels", s.auth(s.addTunnel))
	mux.HandleFunc("POST /v1/tunnels/batch", s.auth(s.addTunnels))
	mux.HandleFunc("PUT /v1/tunnels/{id}", s.auth(s.updateTunnel))
	mux.HandleFunc("DELETE /v1/tunnels/{id}", s.auth(s.removeTunnel))
	mux.HandleFunc("PUT /v1/settings", s.auth(s.updateSettings))
	mux.HandleFunc("POST /v1/confirm/{id}", s.auth(s.confirm))
	s.httpServer = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := writeEndpoint(endpointFile, s.endpoint); err != nil {
		listener.Close()
		_ = os.Remove(endpointPath)
		return nil, err
	}
	go func() { _ = s.httpServer.Serve(listener) }()
	return s, nil
}

func reserveEndpoint(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建控制目录: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("保留控制端点: %w", err)
	}
	client, clientErr := NewClient(path)
	if clientErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		_, aliveErr := client.Snapshot(ctx)
		cancel()
		if aliveErr == nil {
			return nil, errors.New("TunnelX核心已在运行")
		}
	}
	if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) < 5*time.Second {
		return nil, errors.New("TunnelX核心正在启动")
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("移除失效控制端点: %w", err)
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("保留控制端点: %w", err)
	}
	return f, nil
}

func writeEndpoint(f *os.File, endpoint Endpoint) error {
	defer f.Close()
	data, err := json.Marshal(endpoint)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("写入控制端点: %w", err)
	}
	if err := secureEndpointFile(f); err != nil {
		return fmt.Errorf("设置控制端点权限: %w", err)
	}
	return f.Sync()
}

func (s *Server) Endpoint() Endpoint { return s.endpoint }

// Done is closed after an authenticated frontend requests process shutdown.
// The executable host owns the process lifecycle and decides how to unwind.
func (s *Server) Done() <-chan struct{} { return s.shutdown }

func (s *Server) Close(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)
	data, readErr := os.ReadFile(s.endpointPath)
	if readErr == nil {
		var current Endpoint
		if json.Unmarshal(data, &current) == nil && current.Token == s.endpoint.Token {
			_ = os.Remove(s.endpointPath)
		}
	}
	return err
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.endpoint.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) snapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, makeSnapshot(s.service.Snapshot(logbuf.Debug)))
}

func makeSnapshot(s core.Snapshot) SnapshotDTO {
	state := connectionStateName(s.Connection.State)
	out := SnapshotDTO{Version: APIVersion, ID: s.Config.ID, Name: s.Config.Name,
		ServerAddr: s.Config.ServerAddr, ServerUser: s.Config.ServerUser, KeyPath: s.Config.KeyPath,
		Connection: ConnectionDTO{State: state, Reason: s.Connection.Reason, RetryAt: s.Connection.RetryAt},
		Tunnels:    make([]TunnelDTO, 0, len(s.Tunnels)),
		Registry:   make([]RegistryDTO, 0, len(s.Registry)),
	}
	for _, entry := range s.Logs {
		out.Logs = append(out.Logs, logDTO(entry))
	}
	for _, pending := range s.Pending {
		out.Pending = append(out.Pending, confirmationDTO(pending))
	}
	for _, entry := range s.Registry {
		out.Registry = append(out.Registry, registryDTO(entry))
	}
	for _, item := range s.Tunnels {
		out.Tunnels = append(out.Tunnels, TunnelDTO{Index: item.Index, Config: tunnelConfigDTO(item.Config),
			State: tunnelStateName(item.Status.State), Reason: item.Status.Reason,
			RetryAt: item.Status.RetryAt, RemotePort: item.Status.RemotePort})
	}
	return out
}

func tunnelConfigDTO(value config.Tunnel) TunnelConfigDTO {
	return TunnelConfigDTO{ID: value.ID, Kind: string(value.Kind), Name: value.Name, Enabled: value.Enabled,
		LocalHost: value.LocalHost, LocalPort: value.LocalPort, PeerID: value.PeerID,
		PeerTunnelID: value.PeerTunnelID, PeerName: value.PeerName,
		PeerSrcPort: value.PeerSrcPort, ListenPort: value.ListenPort}
}

func (value TunnelConfigDTO) config() config.Tunnel {
	return config.Tunnel{ID: value.ID, Kind: config.TunnelKind(value.Kind), Name: value.Name, Enabled: value.Enabled,
		LocalHost: value.LocalHost, LocalPort: value.LocalPort, PeerID: value.PeerID,
		PeerTunnelID: value.PeerTunnelID, PeerName: value.PeerName,
		PeerSrcPort: value.PeerSrcPort, ListenPort: value.ListenPort}
}

func logDTO(value logbuf.Entry) LogDTO {
	return LogDTO{Time: value.Time, Level: value.Level.String(), Source: value.Source, Message: value.Msg}
}

func confirmationDTO(value core.Confirmation) ConfirmationDTO {
	return ConfirmationDTO{ID: value.ID, Kind: value.Kind, Host: value.Host,
		Fingerprint: value.Fingerprint, Path: value.Path,
		Readers: append([]string(nil), value.Readers...), Message: value.Message}
}

func connectionStateName(state manager.ConnState) string {
	switch state {
	case manager.ConnIdle:
		return "idle"
	case manager.ConnConnecting:
		return "connecting"
	case manager.ConnConnected:
		return "connected"
	case manager.ConnRetrying:
		return "retrying"
	case manager.ConnFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// tunnelStateName keeps the local API independent from the Chinese labels used
// by any specific frontend. Non-Go frontends can treat these values as stable
// protocol enums and localize them on their own.
func tunnelStateName(state tunnel.State) string {
	switch state {
	case tunnel.StateStopped:
		return "stopped"
	case tunnel.StateRunning:
		return "running"
	case tunnel.StateReconnecting:
		return "reconnecting"
	case tunnel.StateError:
		return "error"
	case tunnel.StatePeerOffline:
		return "peer_offline"
	default:
		return "unknown"
	}
}

func registryDTO(entry proto.RegistryEntry) RegistryDTO {
	return RegistryDTO{ID: entry.ID, Name: entry.Name, TunnelID: entry.TunnelID,
		SourceHost: entry.SrcHost, SourcePort: entry.SrcPort,
		TunnelName: entry.TunnelName, RemotePort: entry.RemotePort, Since: entry.Since,
		ClientVersion: entry.ClientVersion}
}

func (s *Server) connect(w http.ResponseWriter, _ *http.Request) {
	s.service.Start()
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) disconnect(w http.ResponseWriter, _ *http.Request) {
	s.service.Stop()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) shutdownCore(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	events, unsubscribe := s.service.Subscribe(128)
	defer unsubscribe()
	flusher.Flush()
	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			dto := EventDTO{Version: APIVersion, Seq: event.Seq, Time: event.Time, Kind: string(event.Kind)}
			if event.Log != nil {
				value := logDTO(*event.Log)
				dto.Log = &value
			}
			if event.Confirmation != nil {
				value := confirmationDTO(*event.Confirmation)
				dto.Confirmation = &value
			}
			_ = enc.Encode(dto)
			flusher.Flush()
		}
	}
}

func (s *Server) addTunnel(w http.ResponseWriter, r *http.Request) {
	var value TunnelConfigDTO
	if !decodeJSON(w, r, &value) {
		return
	}
	if err := s.service.AddTunnel(value.config()); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addTunnels(w http.ResponseWriter, r *http.Request) {
	var values []TunnelConfigDTO
	if !decodeJSON(w, r, &values) {
		return
	}
	if len(values) == 0 {
		http.Error(w, "至少需要一条隧道", http.StatusBadRequest)
		return
	}
	tunnels := make([]config.Tunnel, 0, len(values))
	for _, value := range values {
		tunnels = append(tunnels, value.config())
	}
	if err := s.service.AddTunnels(tunnels); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateTunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "invalid tunnel id", http.StatusBadRequest)
		return
	}
	var value TunnelConfigDTO
	if !decodeJSON(w, r, &value) {
		return
	}
	value.ID = id
	if err := s.service.UpdateTunnelByID(id, value.config()); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeTunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "invalid tunnel id", http.StatusBadRequest)
		return
	}
	if err := s.service.RemoveTunnelByID(id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type settingsRequest struct {
	Name       string `json:"name"`
	ServerAddr string `json:"server_addr"`
	ServerUser string `json:"server_user"`
	KeyPath    string `json:"key_path"`
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var v settingsRequest
	if !decodeJSON(w, r, &v) {
		return
	}
	if err := s.service.UpdateSettings(v.Name, v.ServerAddr, v.ServerUser, v.KeyPath); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type confirmRequest struct {
	Accept bool `json:"accept"`
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid confirmation id", http.StatusBadRequest)
		return
	}
	var v confirmRequest
	if !decodeJSON(w, r, &v) {
		return
	}
	if err := s.service.Confirm(id, v.Accept); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type Client struct {
	endpoint Endpoint
	http     *http.Client
}

func NewClient(endpointPath string) (*Client, error) {
	data, err := os.ReadFile(endpointPath)
	if err != nil {
		return nil, fmt.Errorf("读取控制端点: %w", err)
	}
	var endpoint Endpoint
	if err := json.Unmarshal(data, &endpoint); err != nil {
		return nil, fmt.Errorf("解析控制端点: %w", err)
	}
	if endpoint.Version != APIVersion {
		return nil, fmt.Errorf("控制协议版本不兼容: %d", endpoint.Version)
	}
	return &Client{endpoint: endpoint, http: &http.Client{}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint.Address+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.endpoint.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接本地 TunnelX 核心: %w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var v map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return nil, fmt.Errorf("控制请求失败: %s", firstNonEmpty(v["error"], resp.Status))
	}
	return resp, nil
}

func (c *Client) Snapshot(ctx context.Context) (SnapshotDTO, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v1/snapshot", nil)
	if err != nil {
		return SnapshotDTO{}, err
	}
	defer resp.Body.Close()
	var out SnapshotDTO
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}
func (c *Client) Connect(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodPost, "/v1/connect", nil)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}
func (c *Client) Disconnect(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodPost, "/v1/disconnect", nil)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}
func (c *Client) Shutdown(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodPost, "/v1/shutdown", nil)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}
func (c *Client) Confirm(ctx context.Context, id uint64, accept bool) error {
	resp, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/v1/confirm/%d", id), confirmRequest{Accept: accept})
	if resp != nil {
		resp.Body.Close()
	}
	return err
}
func (c *Client) Events(ctx context.Context) (<-chan EventDTO, <-chan error) {
	out, errs := make(chan EventDTO, 64), make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		resp, err := c.request(ctx, http.MethodGet, "/v1/events", nil)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			var event EventDTO
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				errs <- err
				return
			}
			out <- event
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
		}
	}()
	return out, errs
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown error"
}
