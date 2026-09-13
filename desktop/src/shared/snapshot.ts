import type {
  ConfirmationDTO,
  LogDTO,
  RegistryDTO,
  SnapshotDTO,
  TunnelDTO,
} from './dto'

export type SnapshotWireDTO = Omit<
  SnapshotDTO,
  'tunnels' | 'registry' | 'logs' | 'pending_confirmations'
> & {
  tunnels?: TunnelDTO[] | null
  registry?: RegistryDTO[] | null
  logs?: LogDTO[] | null
  pending_confirmations?: ConfirmationDTO[] | null
}

// Go encodes a nil slice as null. Normalize the wire response at the boundary
// so every desktop consumer can rely on iterable collections, including when
// it attaches to an older core.
export function normalizeSnapshot(value: SnapshotWireDTO): SnapshotDTO {
  return {
    ...value,
    tunnels: Array.isArray(value.tunnels) ? value.tunnels : [],
    registry: Array.isArray(value.registry) ? value.registry : [],
    logs: Array.isArray(value.logs) ? value.logs : [],
    pending_confirmations: Array.isArray(value.pending_confirmations)
      ? value.pending_confirmations
      : [],
  }
}
