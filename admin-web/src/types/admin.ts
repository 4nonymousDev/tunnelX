export type SessionState = 'handshaking' | 'online' | 'closing' | string

export interface TunnelDto {
  id: string
  name: string
  remote_port: number
  target?: string
}

export interface SessionDto {
  id: string
  fingerprint: string
  client_id: string
  name: string
  role: string
  version: string
  remote_ip: string
  connected_at: string | number
  state: SessionState
  tunnels: readonly TunnelDto[]
}

export interface ClientDto {
  fingerprint: string
  note: string
  username?: string
  email?: string
  computer_name?: string
  first_seen_at: string | number
  last_seen_at: string | number
  last_ip: string
  last_client_id: string
  last_reported_name: string
  last_role: string
  last_version: string
  online: boolean
  active_session_count: number
  authorized: boolean
  blocked: boolean
  effective_access: boolean
  identity_verified: boolean
  block_reason?: string
  block_expires_at?: string | number | null
}

export interface ImportPublicKeyRequestDto {
  client_id: string
  public_key: string
  username: string
  email: string
  computer_name: string
  reason: string
}

export interface ImportPublicKeyResultDto {
  client_id: string
  fingerprint: string
  username: string
  email: string
  computer_name: string
}

export interface OverviewDto {
  storage_healthy: boolean
  storage_status: string
  online_users: number
  importers: number
  active_exporters: number
  active_tunnels: number
  rejected_today: number
  timezone: string
  period_start: string | number
}

export interface AuditEventDto {
  id: string | number
  time: string | number
  kind: string
  session_id?: string
  client_id?: string
  fingerprint?: string
  remote_ip?: string
  target_session_id?: string
  target_fingerprint?: string
  target_client_id?: string
  target_tunnel_id?: string
  target_tunnel_name?: string
  result: string
  reason?: string
  bytes_sent?: number
  bytes_received?: number
}

export interface AdminActionDto {
  id: string | number
  action: string
  target_type: string
  target_id: string
  reason: string
  result: string
  error?: string
  operator: string
  transport_peer: string
  source_ip: string | null
  created_at: string | number
}

export interface ClientDetailDto {
  client: ClientDto
  sessions: readonly SessionDto[]
  access_events: readonly AuditEventDto[]
  admin_actions: readonly AdminActionDto[]
}

export interface AuditQuery {
  from?: string
  to?: string
  client_id?: string
  fingerprint?: string
  result?: string
  cursor?: string
  limit?: number
}

export interface PageDto<T> {
  items: T[]
  next_cursor: string | null
}

export interface ApiErrorBody {
  error?: string | { code?: string; message?: string }
  message?: string
  request_id?: string
}

export interface IdentityBindingDto {
  client_id: string
  fingerprint: string
  generation: number
  revoked: boolean
  created_at: string
  updated_at: string
}
export interface IdentityClaimDto {
  client_id: string
  fingerprint: string
  reported_name: string
  remote_ip: string
  last_seen_at: string
}
export interface AdminOperationDto {
  id: string
  action: string
  target_id: string
  state: 'pending' | 'applied' | 'failed' | 'needs_reconcile'
  created_at: string
}
export interface BindIdentityRequestDto {
  client_id: string
  fingerprint: string
  expected_generation: number
  reason: string
}

export interface AccountDto {
  username: string
  enabled: boolean
  is_admin: boolean
  generation: number
  max_devices: number
  created_at: string
  updated_at: string
}
export interface AccountDeviceDto {
  client_id: string
  fingerprint: string
  generation: number
  active: boolean
  name: string
  created_at: string
  updated_at: string
}
export interface CreateAccountRequestDto {
  username: string
  password: string
  max_devices: number
  is_admin?: boolean
  reason: string
}
export interface UpdateAccountRequestDto {
  password?: string
  enabled?: boolean
  is_admin?: boolean
  expected_generation: number
  reason: string
}

export interface AdminSessionDto {
  username: string
  csrf_token: string
  expires_at: string
}
