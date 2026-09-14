import type {
  CoreStatus,
  KeyGenerationRequestDTO,
  KeyGenerationResultDTO,
  LogDTO,
  SettingsDTO,
  SnapshotDTO,
  TunnelConfigDTO,
} from './dto'

export const IPC = {
  bootstrap: 'tunnelx:bootstrap',
  refresh: 'tunnelx:refresh',
  connect: 'tunnelx:connect',
  disconnect: 'tunnelx:disconnect',
  addTunnel: 'tunnelx:tunnel:add',
  addTunnels: 'tunnelx:tunnel:add-batch',
  updateTunnel: 'tunnelx:tunnel:update',
  deleteTunnel: 'tunnelx:tunnel:delete',
  updateSettings: 'tunnelx:settings:update',
  selectKeyDirectory: 'tunnelx:key:select-directory',
  generateKey: 'tunnelx:key:generate',
  copyText: 'tunnelx:clipboard:write',
  confirm: 'tunnelx:confirm',
  event: 'tunnelx:event',
} as const

export type DesktopEvent =
  | { type: 'snapshot'; snapshot: SnapshotDTO }
  | { type: 'log'; log: LogDTO }
  | { type: 'core-status'; status: CoreStatus }
  | { type: 'error'; message: string }

export interface BootstrapResult {
  status: CoreStatus
  snapshot: SnapshotDTO
}

export interface UpdateTunnelRequest {
  id: string
  tunnel: TunnelConfigDTO
}

export interface ConfirmationRequest {
  id: number
  accept: boolean
}

export interface TunnelXDesktopAPI {
  bootstrap(): Promise<BootstrapResult>
  refresh(): Promise<SnapshotDTO>
  connect(): Promise<SnapshotDTO>
  disconnect(): Promise<SnapshotDTO>
  addTunnel(tunnel: TunnelConfigDTO): Promise<SnapshotDTO>
  addTunnels(tunnels: TunnelConfigDTO[]): Promise<SnapshotDTO>
  updateTunnel(id: string, tunnel: TunnelConfigDTO): Promise<SnapshotDTO>
  deleteTunnel(id: string): Promise<SnapshotDTO>
  updateSettings(settings: SettingsDTO): Promise<SnapshotDTO>
  selectKeyDirectory(keyPath: string): Promise<string | undefined>
  generateKey(request: KeyGenerationRequestDTO): Promise<KeyGenerationResultDTO>
  copyText(text: string): Promise<void>
  confirm(id: number, accept: boolean): Promise<SnapshotDTO>
  onEvent(listener: (event: DesktopEvent) => void): () => void
}
