export type ConnectionState = 'idle' | 'connecting' | 'connected' | 'retrying' | 'failed' | 'unknown'
export type TunnelKind = 'export' | 'import'

export interface EndpointFile {
  version: number
  address: string
  token: string
  pid: number
}

export interface ConnectionDTO {
  state: ConnectionState
  reason?: string
  retry_at?: string
}

export interface TunnelConfigDTO {
  id: string
  kind: TunnelKind
  name: string
  enabled: boolean
  local_host?: string
  local_port?: number
  peer_id?: string
  peer_tunnel_id?: string
  peer_name?: string
  peer_src_port?: number
  listen_port?: number
}

export interface TunnelDTO {
  index: number
  config: TunnelConfigDTO
  state: string
  reason?: string
  retry_at?: string
  remote_port?: number
}

export interface LogDTO {
  time: string
  level: string
  source: string
  message: string
}

export interface ConfirmationDTO {
  id: number
  kind: string
  host?: string
  fingerprint?: string
  path?: string
  readers?: string[]
  message: string
}

export interface RegistryDTO {
  id: string
  name: string
  tunnel_id?: string
  source_host?: string
  source_port: number
  tunnel_name: string
  remote_port: number
  since: string
  client_version: string
}

export interface SnapshotDTO {
  version: number
  client_version?: string
  id: string
  name: string
  server_addr: string
  key_path: string
  connection: ConnectionDTO
  tunnels: TunnelDTO[]
  registry: RegistryDTO[]
  logs?: LogDTO[]
  pending_confirmations?: ConfirmationDTO[]
}

export interface CoreEventDTO {
  version: number
  seq: number
  time: string
  kind: string
  log?: LogDTO
  confirmation?: ConfirmationDTO
}

export interface SettingsDTO {
  name: string
  server_addr: string
  key_path: string
}

export interface KeyGenerationRequestDTO {
  key_path: string
  username: string
  email: string
}

export interface KeyGenerationResultDTO {
  key_path: string
  pub_path: string
  public_key: string
  fingerprint: string
  metadata: { username: string; email: string; computer_name: string }
  permission_ok: boolean
}

export interface CoreStatus {
  phase: 'starting' | 'running' | 'reconnecting' | 'error'
  source?: 'attached' | 'started'
  message?: string
}
