import type { TrafficDevice, TrafficLink } from '../types/traffic'

export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  const index = Math.max(0, Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024))))
  return `${(value / 1024 ** index).toLocaleString(undefined, { maximumFractionDigits: index ? 2 : 1 })} ${units[index]}`
}
export function formatRate(value: number): string { return `${formatBytes(value)}/s` }

export function trafficScale(maximum: number) {
  const unit = 1024 ** Math.max(0, Math.floor(Math.log(Math.max(1, maximum)) / Math.log(1024)))
  const raw = Math.max(1, maximum) / unit / 4
  const power = 10 ** Math.floor(Math.log10(raw))
  const factor = raw / power
  const step = (factor <= 1 ? 1 : factor <= 2 ? 2 : factor <= 5 ? 5 : 10) * power * unit
  const max = Math.ceil(Math.max(1, maximum) / step) * step
  return { max, ticks: Array.from({ length: Math.round(max / step) + 1 }, (_, i) => i * step) }
}

export interface TrafficNode extends TrafficDevice { rate: number; connections: number }
export function trafficNodes(links: readonly TrafficLink[], side: 'exporter' | 'importer'): TrafficNode[] {
  const nodes = new Map<string, TrafficNode>()
  for (const link of links) {
    const device = link[side]
    const node = nodes.get(device.id) ?? { ...device, rate: 0, connections: 0 }
    node.rate += link.bytes_per_second
    node.connections += link.connections
    nodes.set(device.id, node)
  }
  return [...nodes.values()].sort((a, b) => b.rate - a.rate || a.id.localeCompare(b.id))
}

// Preserve each node's actual Y value. Only labels are spread vertically, with
// leaders back to their points; equal-value points use different X positions.
export function positionNodes(nodes: TrafficNode[], max: number, height: number, left: boolean) {
  const top = 66, bottom = height - 62
  const occupied: number[] = []
  const positioned = nodes.map(node => {
    const y = bottom - node.rate / max * (bottom - top)
    let lane = 0
    while (occupied[lane] !== undefined && y - occupied[lane]! < 14) lane++
    occupied[lane] = y
    return { ...node, y, x: left ? 260 + lane * 14 : 740 - lane * 14, labelY: y }
  })
  for (let i = 0; i < positioned.length; i++) {
    positioned[i]!.labelY = Math.max(positioned[i]!.y, i ? positioned[i - 1]!.labelY + 32 : top)
  }
  for (let i = positioned.length - 1; i >= 0; i--) {
    positioned[i]!.labelY = Math.min(positioned[i]!.labelY, i === positioned.length - 1 ? bottom : positioned[i + 1]!.labelY - 32)
  }
  return positioned
}
