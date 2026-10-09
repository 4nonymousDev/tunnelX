export interface TrafficDevice { id: string; name: string }
export interface TrafficLink {
  exporter: TrafficDevice
  importer: TrafficDevice
  bytes_per_second: number
  connections: number
}
export interface TrafficTotal extends TrafficDevice { bytes: number }
export interface TrafficHistory {
  from: string
  to: string
  persisted_at: string
  exporters: TrafficTotal[]
  importers: TrafficTotal[]
}
export interface TrafficRange { from: string; to: string; label: string }
export interface TrafficSnapshot {
  persistent: boolean
  persistence_healthy: boolean
  persisted_at: string | null
  sampled_at: string
  since: string
  links: TrafficLink[]
  exporters: TrafficTotal[]
  importers: TrafficTotal[]
}
