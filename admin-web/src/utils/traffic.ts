import type { TrafficDevice, TrafficLink } from '../types/traffic'

export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  const index = Math.max(0, Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024))))
  return `${(value / 1024 ** index).toLocaleString(undefined, { maximumFractionDigits: index ? 2 : 1 })} ${units[index]}`
}
export function formatRate(value: number): string { return `${formatBytes(value)}/s` }

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

export function trafficLinkKey(link: TrafficLink): string {
  return JSON.stringify([link.exporter.id, link.importer.id])
}

export interface SankeyNode extends TrafficNode {
  x: number
  y: number
  height: number
  labelY: number
}

// Two-column Sankey: one shared linear scale, and disjoint source/target
// slots whose heights sum to their node's throughput. Label space is kept
// outside the measured flow area; zero-rate links never acquire fake volume.
export function trafficSankey(links: readonly TrafficLink[], viewport?: { width: number; height: number }) {
  const left = trafficNodes(links, 'exporter').sort((a, b) => a.id.localeCompare(b.id))
  const right = trafficNodes(links, 'importer').sort((a, b) => a.id.localeCompare(b.id))
  const width = Math.max(300, viewport?.width ?? 1000)
  const height = Math.max(152, viewport?.height ?? Math.max(420, Math.max(left.length, right.length) * 52 + 100))
  const leftX = Math.min(220, width * .25), rightX = width - leftX - 18
  const top = 64, available = height - top - 36, gap = 20, minSlot = 32
  const total = left.reduce((sum, node) => sum + node.rate, 0)
  function used(nodes: TrafficNode[], scale: number) {
    return nodes.reduce((sum, node) => sum + Math.max(minSlot, node.rate * scale), 0) + Math.max(0, nodes.length - 1) * gap
  }
  let low = 0, high = total > 0 ? available / total : 0
  for (let i = 0; i < 48; i++) {
    const mid = (low + high) / 2
    if (used(left, mid) <= available && used(right, mid) <= available) low = mid
    else high = mid
  }
  const scale = low
  function position(nodes: TrafficNode[], x: number): SankeyNode[] {
    let cursor = top + (available - used(nodes, scale)) / 2
    return nodes.map(node => {
      const flowHeight = node.rate * scale
      const slot = Math.max(minSlot, flowHeight)
      const labelY = cursor + slot / 2
      cursor += slot + gap
      return { ...node, x, y: labelY - flowHeight / 2, height: flowHeight, labelY }
    })
  }
  const exporters = position(left, leftX), importers = position(right, rightX)
  const sources = new Map(exporters.map(node => [node.id, node]))
  const targets = new Map(importers.map(node => [node.id, node]))
  const sourceOffsets = new Map<string, number>(), targetOffsets = new Map<string, number>()
  const sorted = [...links].sort((a, b) => a.exporter.id.localeCompare(b.exporter.id) || a.importer.id.localeCompare(b.importer.id))
  const edges = sorted.map(link => {
    const source = sources.get(link.exporter.id)!, target = targets.get(link.importer.id)!
    const width = link.bytes_per_second * scale
    const sy = width ? source.y + (sourceOffsets.get(source.id) ?? 0) : source.labelY
    const ty = width ? target.y + (targetOffsets.get(target.id) ?? 0) : target.labelY
    sourceOffsets.set(source.id, (sourceOffsets.get(source.id) ?? 0) + width)
    targetOffsets.set(target.id, (targetOffsets.get(target.id) ?? 0) + width)
    const x1 = source.x + 18, x2 = target.x
    const bend1 = x1 + (x2 - x1) * .37, bend2 = x2 - (x2 - x1) * .37
    const centerPath = `M ${x1} ${sy + width / 2} C ${bend1} ${sy + width / 2}, ${bend2} ${ty + width / 2}, ${x2} ${ty + width / 2}`
    const path = `M ${x1} ${sy} C ${bend1} ${sy}, ${bend2} ${ty}, ${x2} ${ty} L ${x2} ${ty + width} C ${bend2} ${ty + width}, ${bend1} ${sy + width}, ${x1} ${sy + width} Z`
    return { ...link, key: trafficLinkKey(link), width, sourceY: sy, targetY: ty, path, centerPath }
  })
  return { width, height, leftX, rightX, scale, exporters, importers, edges }
}
