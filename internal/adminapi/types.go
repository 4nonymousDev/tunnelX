package adminapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type Overview struct {
	OnlineUsers     int       `json:"online_users"`
	Importers       int       `json:"importers"`
	ActiveExporters int       `json:"active_exporters"`
	ActiveTunnels   int       `json:"active_tunnels"`
	RejectedToday   int       `json:"rejected_today"`
	Timezone        string    `json:"timezone"`
	PeriodStart     time.Time `json:"period_start"`
}

type Tunnel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	RemotePort uint32 `json:"remote_port"`
}

type Session struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	ClientID    string    `json:"client_id"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	Version     string    `json:"version"`
	RemoteIP    string    `json:"remote_ip"`
	ConnectedAt time.Time `json:"connected_at"`
	State       string    `json:"state"`
	Tunnels     []Tunnel  `json:"tunnels"`
}

type Client struct {
	Fingerprint        string     `json:"fingerprint"`
	Note               string     `json:"note"`
	FirstSeenAt        time.Time  `json:"first_seen_at"`
	LastSeenAt         time.Time  `json:"last_seen_at"`
	LastIP             string     `json:"last_ip"`
	LastClientID       string     `json:"last_client_id"`
	LastReportedName   string     `json:"last_reported_name"`
	LastRole           string     `json:"last_role"`
	LastVersion        string     `json:"last_version"`
	Online             bool       `json:"online"`
	ActiveSessionCount int        `json:"active_session_count"`
	Authorized         bool       `json:"authorized"`
	Blocked            bool       `json:"blocked"`
	EffectiveAccess    bool       `json:"effective_access"`
	BlockReason        string     `json:"block_reason,omitempty"`
	BlockExpiresAt     *time.Time `json:"block_expires_at,omitempty"`
}

type AuditEvent struct {
	ID                int64     `json:"id"`
	Time              time.Time `json:"time"`
	Kind              string    `json:"kind"`
	SessionID         string    `json:"session_id,omitempty"`
	Fingerprint       string    `json:"fingerprint,omitempty"`
	ClientID          string    `json:"client_id,omitempty"`
	RemoteIP          string    `json:"remote_ip,omitempty"`
	TargetSessionID   string    `json:"target_session_id,omitempty"`
	TargetFingerprint string    `json:"target_fingerprint,omitempty"`
	TargetClientID    string    `json:"target_client_id,omitempty"`
	TargetTunnelID    string    `json:"target_tunnel_id,omitempty"`
	TargetTunnelName  string    `json:"target_tunnel_name,omitempty"`
	Result            string    `json:"result"`
	Reason            string    `json:"reason,omitempty"`
	BytesSent         int64     `json:"bytes_sent,omitempty"`
	BytesReceived     int64     `json:"bytes_received,omitempty"`
}

type AdminAction struct {
	ID            int64     `json:"id"`
	Action        string    `json:"action"`
	TargetType    string    `json:"target_type"`
	TargetID      string    `json:"target_id"`
	Reason        string    `json:"reason"`
	Result        string    `json:"result"`
	Error         string    `json:"error,omitempty"`
	Operator      string    `json:"operator"`
	TransportPeer string    `json:"transport_peer"`
	SourceIP      *string   `json:"source_ip"`
	CreatedAt     time.Time `json:"created_at"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type ListQuery struct {
	Limit       int
	Cursor      *Cursor
	Fingerprint string
	ClientID    string
	Result      string
	From        *time.Time
	To          *time.Time
}

type BlockRequest struct {
	Reason    string
	ExpiresAt *time.Time
}

// Backend is the intentionally narrow boundary between HTTP and server state.
// Implementations must treat Cursor as an exclusive (time,id) ordering key.
type Backend interface {
	Overview(context.Context) (Overview, error)
	ListSessions(context.Context, ListQuery) (Page[Session], error)
	GetSession(context.Context, string) (Session, error)
	DisconnectSession(context.Context, string, string) error
	ListClients(context.Context, ListQuery) (Page[Client], error)
	GetClient(context.Context, string) (Client, error)
	UpdateClientNote(context.Context, string, string, string) error
	BlockClient(context.Context, string, BlockRequest) error
	UnblockClient(context.Context, string, string) error
	ListAuditEvents(context.Context, ListQuery) (Page[AuditEvent], error)
	ListAdminActions(context.Context, ListQuery) (Page[AdminAction], error)
}

type Cursor struct {
	Time time.Time `json:"t"`
	ID   int64     `json:"id"`
}

func EncodeCursor(c Cursor) string {
	b, _ := json.Marshal(struct {
		Time int64 `json:"t"`
		ID   int64 `json:"id"`
	}{c.Time.UTC().UnixMicro(), c.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeCursor(s string) (Cursor, error) {
	if s == "" || len(s) > 256 {
		return Cursor{}, errors.New("invalid cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, errors.New("invalid cursor")
	}
	var v struct {
		Time int64 `json:"t"`
		ID   int64 `json:"id"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil || v.Time <= 0 || v.ID <= 0 {
		return Cursor{}, errors.New("invalid cursor")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Cursor{}, errors.New("invalid cursor")
	}
	return Cursor{Time: time.UnixMicro(v.Time).UTC(), ID: v.ID}, nil
}

type BackendError struct {
	Code    string
	Message string
}

func (e *BackendError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
