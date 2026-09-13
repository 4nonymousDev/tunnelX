import type { RegistryDTO, SnapshotDTO, TunnelDTO } from '@shared/dto'

export interface ImportEntryAvailability {
  entry: RegistryDTO
  available: boolean
  reason?: string
}

export function registryEntryKey(entry: RegistryDTO): string {
  return entry.tunnel_id
    ? `${entry.id}/${entry.tunnel_id}`
    : `${entry.id}/legacy/${entry.source_port}`
}

export function availableImportEntries(snapshot?: SnapshotDTO): RegistryDTO[] {
  return classifyImportEntries(snapshot)
    .filter(item => item.available)
    .map(item => item.entry)
}

export function unavailableImportEntries(snapshot?: SnapshotDTO): ImportEntryAvailability[] {
  return classifyImportEntries(snapshot).filter(item => !item.available)
}

export function classifyImportEntries(snapshot?: SnapshotDTO): ImportEntryAvailability[] {
  if (!snapshot) return []

  const imports = (snapshot.tunnels ?? []).filter(item => item.config.kind === 'import')
  const stable = new Set<string>()
  const legacy = new Set<string>()
  const importedPorts = new Set<string>()
  for (const item of imports) {
    const config = item.config
    const portKey = peerPortKey(config.peer_id, config.peer_src_port)
    importedPorts.add(portKey)
    if (config.peer_tunnel_id) stable.add(`${config.peer_id}/${config.peer_tunnel_id}`)
    else legacy.add(portKey)
  }

  return (snapshot.registry ?? [])
    .map((entry): ImportEntryAvailability => {
      if (entry.id === snapshot.id) {
        return { entry, available: false, reason: '这是本机导出的端口' }
      }
      const portKey = peerPortKey(entry.id, entry.source_port)
      if (entry.tunnel_id && stable.has(`${entry.id}/${entry.tunnel_id}`)) {
        return { entry, available: false, reason: '已添加为导入隧道' }
      }
      if (legacy.has(portKey) || (!entry.tunnel_id && importedPorts.has(portKey))) {
        return { entry, available: false, reason: '已有相同对端和源端口的导入隧道' }
      }
      return { entry, available: true }
    })
    .sort((left, right) => compareRegistryEntries(left.entry, right.entry))
}

export function usedImportPorts(tunnels: TunnelDTO[]): number[] {
  return tunnels
    .filter(item => item.config.kind === 'import' && validPort(item.config.listen_port))
    .map(item => Number(item.config.listen_port))
}

export function allocateImportPort(preferred: number, usedPorts: Iterable<number>): number {
  const used = new Set(usedPorts)
  let port = validPort(preferred) ? preferred : 10000
  for (let attempts = 0; attempts < 65535; attempts += 1) {
    if (!used.has(port)) return port
    port = port === 65535 ? 1024 : port + 1
  }
  return 0
}

export function importName(entry: RegistryDTO): string {
  return entry.tunnel_name || `${entry.name}:${entry.source_port}`
}

function peerPortKey(peerID?: string, sourcePort?: number): string {
  return `${peerID ?? ''}/${sourcePort ?? 0}`
}

function compareRegistryEntries(left: RegistryDTO, right: RegistryDTO): number {
  const byPeer = (left.name || left.id).localeCompare(right.name || right.id, 'zh-CN')
  if (byPeer !== 0) return byPeer
  if (left.source_port !== right.source_port) return left.source_port - right.source_port
  return (left.tunnel_id ?? '').localeCompare(right.tunnel_id ?? '')
}

function validPort(value?: number): boolean {
  return Number.isInteger(value) && Number(value) >= 1 && Number(value) <= 65535
}
