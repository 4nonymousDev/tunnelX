function toMilliseconds(value: string | number): number {
  if (typeof value === 'string') return new Date(value).getTime()
  if (value < 10_000_000_000) return value * 1000
  if (value > 10_000_000_000_000_000) return Math.floor(value / 1_000_000)
  if (value > 10_000_000_000_000) return Math.floor(value / 1000)
  return value
}

export function formatDate(value?: string | number | null): string {
  if (value === undefined || value === null || value === '') return '—'
  const date = new Date(toMilliseconds(value))
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString()
}

export function formatDuration(value: string | number): string {
  const start = toMilliseconds(value)
  if (Number.isNaN(start)) return '—'
  const seconds = Math.max(0, Math.floor((Date.now() - start) / 1000))
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  return [days ? `${days}天` : '', hours ? `${hours}时` : '', `${minutes}分`].filter(Boolean).join(' ')
}

export function shortFingerprint(value: string): string {
  return value.length > 28 ? `${value.slice(0, 18)}…${value.slice(-8)}` : value
}
