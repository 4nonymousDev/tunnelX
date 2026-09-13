import type { CoreEventDTO, EndpointFile, SnapshotDTO } from '../shared/dto'
import { normalizeSnapshot, type SnapshotWireDTO } from '../shared/snapshot'

const API_VERSION = 1

export class CoreApiClient {
  constructor(private readonly endpoint: EndpointFile) {
    const url = new URL(endpoint.address)
    if (endpoint.version !== API_VERSION) {
      throw new Error(`不兼容的核心 API 版本：${endpoint.version}`)
    }
    if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1') {
      throw new Error('核心控制端点必须使用 127.0.0.1 上的 HTTP 地址')
    }
  }

  async snapshot(signal?: AbortSignal): Promise<SnapshotDTO> {
    const value = await this.request<SnapshotWireDTO>('GET', '/v1/snapshot', undefined, signal)
    return normalizeSnapshot(value)
  }

  async connect(): Promise<void> {
    await this.request<void>('POST', '/v1/connect')
  }

  async disconnect(): Promise<void> {
    await this.request<void>('POST', '/v1/disconnect')
  }

  async shutdown(signal?: AbortSignal): Promise<void> {
    await this.request<void>('POST', '/v1/shutdown', undefined, signal)
  }

  async addTunnel(tunnel: unknown): Promise<void> {
    await this.request<void>('POST', '/v1/tunnels', tunnel)
  }

  async addTunnels(tunnels: unknown[]): Promise<void> {
    await this.request<void>('POST', '/v1/tunnels/batch', tunnels)
  }

  async updateTunnel(id: string, tunnel: unknown): Promise<void> {
    await this.request<void>('PUT', `/v1/tunnels/${encodeURIComponent(id)}`, tunnel)
  }

  async deleteTunnel(id: string): Promise<void> {
    await this.request<void>('DELETE', `/v1/tunnels/${encodeURIComponent(id)}`)
  }

  async updateSettings(settings: unknown): Promise<void> {
    await this.request<void>('PUT', '/v1/settings', settings)
  }

  async confirm(id: number, accept: boolean): Promise<void> {
    await this.request<void>('POST', `/v1/confirm/${id}`, { accept })
  }

  async streamEvents(
    signal: AbortSignal,
    onEvent: (event: CoreEventDTO) => void | Promise<void>,
  ): Promise<void> {
    const response = await fetch(`${this.endpoint.address}/v1/events`, {
      headers: this.headers(),
      signal,
    })
    if (!response.ok || !response.body) {
      throw await this.responseError(response)
    }

    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffered = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffered += decoder.decode(value, { stream: true })
      const lines = buffered.split('\n')
      buffered = lines.pop() ?? ''
      for (const line of lines) {
        if (line.trim()) await onEvent(JSON.parse(line) as CoreEventDTO)
      }
    }
    if (!signal.aborted) throw new Error('核心事件流已关闭')
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    signal?: AbortSignal,
  ): Promise<T> {
    const response = await fetch(`${this.endpoint.address}${path}`, {
      method,
      headers: this.headers(),
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
    if (!response.ok) throw await this.responseError(response)
    if (response.status === 204) return undefined as T
    return await response.json() as T
  }

  private headers(): Record<string, string> {
    return {
      Authorization: `Bearer ${this.endpoint.token}`,
      'Content-Type': 'application/json',
    }
  }

  private async responseError(response: Response): Promise<Error> {
    try {
      const body = await response.json() as { error?: string }
      return new Error(body.error || `核心请求失败：HTTP ${response.status}`)
    } catch {
      return new Error(`核心请求失败：HTTP ${response.status}`)
    }
  }
}
