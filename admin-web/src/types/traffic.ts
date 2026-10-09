export interface TrafficDevice { id: string; name: string }
export interface TrafficLink {
  exporter: TrafficDevice
  importer: TrafficDevice
  bytes_per_second: number
  connections: number
}
export interface TrafficTotal extends TrafficDevice { bytes: number }
export interface TrafficSnapshot {
  sampled_at: string
  since: string
  links: TrafficLink[]
  exporters: TrafficTotal[]
  importers: TrafficTotal[]
}
