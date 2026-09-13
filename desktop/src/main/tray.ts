import { deflateSync } from 'node:zlib'

import { nativeImage, type NativeImage } from 'electron'

import type { CoreStatus, SnapshotDTO, TunnelDTO } from '../shared/dto'

export type TrayState = 'idle' | 'ok' | 'warn' | 'error'

export interface TrayPresentation {
  state: TrayState
  tooltip: string
}

const ICON_SIZE = 32
const TOOLTIP_LIMIT = 120
const COLORS: Record<TrayState, readonly [number, number, number]> = {
  idle: [0x9e, 0x9e, 0x9e],
  ok: [0x4c, 0xaf, 0x50],
  warn: [0xff, 0xb3, 0x00],
  error: [0xf4, 0x43, 0x36],
}
const iconCache = new Map<TrayState, NativeImage>()

export function trayIcon(state: TrayState): NativeImage {
  const cached = iconCache.get(state)
  if (cached) return cached

  const icon = nativeImage.createFromBuffer(renderDotPng(COLORS[state]))
  if (icon.isEmpty()) throw new Error(`无法生成托盘图标: ${state}`)
  iconCache.set(state, icon)
  return icon
}

export function trayPresentation(
  snapshot: SnapshotDTO | undefined,
  coreStatus: CoreStatus,
): TrayPresentation {
  return {
    state: overallState(snapshot, coreStatus),
    tooltip: truncateTooltip([
      connectionLine(snapshot, coreStatus),
      tunnelLine(snapshot?.tunnels ?? []),
    ].join('\n')),
  }
}

export function overallState(
  snapshot: SnapshotDTO | undefined,
  coreStatus: CoreStatus,
): TrayState {
  if (coreStatus.phase === 'error') return 'error'
  if (coreStatus.phase === 'starting' || coreStatus.phase === 'reconnecting') return 'warn'
  if (!snapshot || snapshot.connection.state === 'idle') return 'idle'

  switch (snapshot.connection.state) {
    case 'failed':
      return 'error'
    case 'connecting':
    case 'retrying':
    case 'unknown':
      return 'warn'
  }

  let running = false
  let state: TrayState = 'ok'
  for (const tunnel of snapshot.tunnels ?? []) {
    if (!tunnel.config.enabled) continue
    if (tunnel.state === 'error') return 'error'
    if (tunnel.state === 'reconnecting' || tunnel.state === 'peer_offline') state = 'warn'
    if (tunnel.state === 'running') running = true
  }
  return !running && state === 'ok' ? 'idle' : state
}

function connectionLine(snapshot: SnapshotDTO | undefined, coreStatus: CoreStatus): string {
  if (coreStatus.phase === 'starting') return 'TunnelX — 核心启动中'
  if (coreStatus.phase === 'reconnecting') return 'TunnelX — 核心重连中'
  if (coreStatus.phase === 'error') return reasonSuffix('TunnelX — 核心异常', coreStatus.message)
  if (!snapshot) return 'TunnelX — 未连接'

  const server = snapshot.server_user
    ? `${snapshot.server_user}@${snapshot.server_addr}`
    : snapshot.server_addr
  switch (snapshot.connection.state) {
    case 'connecting':
      return `TunnelX — 正在连接 ${server}`
    case 'connected':
      return `TunnelX — 已连接 ${server}`
    case 'retrying':
      return reasonSuffix('TunnelX — 重连中', snapshot.connection.reason)
    case 'failed':
      return reasonSuffix('TunnelX — 连接失败', snapshot.connection.reason)
    default:
      return 'TunnelX — 未连接'
  }
}

function tunnelLine(tunnels: TunnelDTO[]): string {
  const enabled = tunnels.filter(tunnel => tunnel.config.enabled)
  if (enabled.length === 0) return '未配置隧道'

  const order = ['running', 'reconnecting', 'peer_offline', 'error', 'stopped'] as const
  const labels: Record<(typeof order)[number], string> = {
    running: '运行中',
    reconnecting: '重连中',
    peer_offline: '对端离线',
    error: '错误',
    stopped: '已停止',
  }
  const parts = order.flatMap(state => {
    const count = enabled.filter(tunnel => tunnel.state === state).length
    return count > 0 ? [`${count} ${labels[state]}`] : []
  })
  const known = parts.reduce((sum, part) => sum + Number.parseInt(part, 10), 0)
  if (known < enabled.length) parts.push(`${enabled.length - known} 未知`)
  if (parts.length === 1) return `隧道 ${enabled.length} 条：全部${parts[0].replace(/^\d+\s/, '')}`
  return `隧道 ${enabled.length} 条：${parts.join(' / ')}`
}

function reasonSuffix(prefix: string, reason: string | undefined): string {
  return reason ? `${prefix}：${reason}` : prefix
}

function truncateTooltip(value: string): string {
  if (value.length <= TOOLTIP_LIMIT) return value
  let end = TOOLTIP_LIMIT - 1
  const code = value.charCodeAt(end - 1)
  if (code >= 0xd800 && code <= 0xdbff) end -= 1
  return `${value.slice(0, end)}…`
}

function renderDotPng(color: readonly [number, number, number]): Buffer {
  const stride = 1 + ICON_SIZE * 4
  const raw = Buffer.alloc(stride * ICON_SIZE)
  const center = ICON_SIZE / 2
  const radiusSquared = (ICON_SIZE / 2 - 2) ** 2

  for (let y = 0; y < ICON_SIZE; y += 1) {
    const row = y * stride
    raw[row] = 0
    for (let x = 0; x < ICON_SIZE; x += 1) {
      const offset = row + 1 + x * 4
      const dx = x + 0.5 - center
      const dy = y + 0.5 - center
      if (dx * dx + dy * dy > radiusSquared) continue
      raw[offset] = color[0]
      raw[offset + 1] = color[1]
      raw[offset + 2] = color[2]
      raw[offset + 3] = 0xff
    }
  }

  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(ICON_SIZE, 0)
  ihdr.writeUInt32BE(ICON_SIZE, 4)
  ihdr[8] = 8
  ihdr[9] = 6
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    pngChunk('IHDR', ihdr),
    pngChunk('IDAT', deflateSync(raw)),
    pngChunk('IEND', Buffer.alloc(0)),
  ])
}

function pngChunk(type: string, data: Buffer): Buffer {
  const name = Buffer.from(type, 'ascii')
  const chunk = Buffer.alloc(12 + data.length)
  chunk.writeUInt32BE(data.length, 0)
  name.copy(chunk, 4)
  data.copy(chunk, 8)
  chunk.writeUInt32BE(crc32(Buffer.concat([name, data])), 8 + data.length)
  return chunk
}

function crc32(data: Buffer): number {
  let crc = 0xffffffff
  for (const byte of data) {
    crc ^= byte
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0)
    }
  }
  return (crc ^ 0xffffffff) >>> 0
}
