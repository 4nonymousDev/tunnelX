import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { mkdir, readFile, unlink } from 'node:fs/promises'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import { app } from 'electron'

import type {
  CoreEventDTO,
  CoreStatus,
  EndpointFile,
  SettingsDTO,
  SnapshotDTO,
  TunnelConfigDTO,
} from '../shared/dto'
import type { DesktopEvent } from '../shared/ipc'
import { CoreApiClient } from './api-client'

const ENDPOINT_FILE = '.tunnelx-control.json'

export class CoreSupervisor {
  private client?: CoreApiClient
  private status: CoreStatus = { phase: 'starting' }
  private initializePromise?: Promise<SnapshotDTO>
  private eventsAbort?: AbortController
  private eventLoopStarted = false
  private lastSequence = 0
  private snapshotRefreshQueue: Promise<void> = Promise.resolve()
  private shutdownPromise?: Promise<void>
  private corePID?: number
  private readonly listeners = new Set<(event: DesktopEvent) => void>()

  private readonly configPath: string
  private readonly stateDir: string
  private readonly endpointPath: string

  constructor() {
    const userData = app.getPath('userData')
    this.configPath = process.env.TUNNELX_CONFIG_PATH || path.join(userData, 'config.json')
    this.stateDir = process.env.TUNNELX_STATE_DIR || path.join(userData, 'state')
    this.endpointPath = process.env.TUNNELX_ENDPOINT_PATH || path.join(this.stateDir, ENDPOINT_FILE)
  }

  getStatus(): CoreStatus {
    return this.status
  }

  subscribe(listener: (event: DesktopEvent) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  async initialize(): Promise<SnapshotDTO> {
    if (!this.initializePromise) {
      this.initializePromise = this.initializeInternal().catch(error => {
        this.initializePromise = undefined
        throw error
      })
    }
    return this.initializePromise
  }

  async connect(): Promise<SnapshotDTO> {
    const client = await this.getClient()
    await client.connect()
    return this.refreshSnapshot()
  }

  refresh(): Promise<SnapshotDTO> {
    return this.refreshSnapshot()
  }

  async disconnect(): Promise<SnapshotDTO> {
    const client = await this.getClient()
    await client.disconnect()
    return this.refreshSnapshot()
  }

  async addTunnel(tunnel: TunnelConfigDTO): Promise<SnapshotDTO> {
    const client = await this.getClient()
    await client.addTunnel(tunnel)
    return this.refreshSnapshot()
  }

  async addTunnels(tunnels: TunnelConfigDTO[]): Promise<SnapshotDTO> {
    const client = await this.getClient()
    await client.addTunnels(tunnels)
    return this.refreshSnapshot()
  }

  async updateTunnel(id: string, tunnel: TunnelConfigDTO): Promise<SnapshotDTO> {
    const client = await this.getClient()
    await client.updateTunnel(id, tunnel)
    return this.refreshSnapshot()
  }

  async deleteTunnel(id: string): Promise<SnapshotDTO> {
    const client = await this.getClient()
    await client.deleteTunnel(id)
    return this.refreshSnapshot()
  }

  async updateSettings(settings: SettingsDTO): Promise<SnapshotDTO> {
    const client = await this.getClient()
    await client.updateSettings(settings)
    return this.refreshSnapshot()
  }

  async confirm(id: number, accept: boolean): Promise<SnapshotDTO> {
    const client = await this.getClient()
    await client.confirm(id, accept)
    return this.refreshSnapshot()
  }

  shutdown(): Promise<void> {
    if (!this.shutdownPromise) {
      this.shutdownPromise = this.shutdownInternal()
    }
    return this.shutdownPromise
  }

  closeFrontend(): void {
    this.eventsAbort?.abort()
    this.eventsAbort = undefined
    this.eventLoopStarted = false
    this.listeners.clear()
    // The core is intentionally not terminated. It is an independent process.
  }

  private async initializeInternal(): Promise<SnapshotDTO> {
    this.updateStatus({ phase: 'starting', message: '正在连接 TunnelX 核心…' })
    await mkdir(path.dirname(this.configPath), { recursive: true })
    await mkdir(this.stateDir, { recursive: true })

    const attached = await this.tryEndpoint()
    if (attached) {
      this.updateStatus({ phase: 'running', source: 'attached' })
      this.startEventLoop()
      return attached
    }

    const corePath = this.resolveCorePath()
    if (!existsSync(corePath)) {
      throw this.fail(`找不到核心程序：${corePath}。可通过 TUNNELX_CORE_PATH 指定。`)
    }
    const mirrorCoreLogs = process.env.TUNNELX_CORE_STDIO === 'inherit'
    const child = spawn(corePath, [
      'run',
      '--config', this.configPath,
      '--state-dir', this.stateDir,
      '--endpoint', this.endpointPath,
      '--confirm-via-api',
    ], {
      detached: true,
      stdio: mirrorCoreLogs ? 'inherit' : 'ignore',
      windowsHide: true,
    })
    this.corePID = child.pid
    child.unref()

    for (let attempt = 0; attempt < 80; attempt += 1) {
      const snapshot = await this.tryEndpoint()
      if (snapshot) {
        this.updateStatus({ phase: 'running', source: 'started' })
        this.startEventLoop()
        return snapshot
      }
      await delay(150)
    }
    throw this.fail('TunnelX 核心启动超时，请检查配置与 tunnelx.log。')
  }

  private async tryEndpoint(): Promise<SnapshotDTO | undefined> {
    try {
      const raw = await readFile(this.endpointPath, 'utf8')
      const endpoint = JSON.parse(raw) as EndpointFile
      const client = new CoreApiClient(endpoint)
      const controller = new AbortController()
      const timeout = setTimeout(() => controller.abort(), 600)
      try {
        const snapshot = await client.snapshot(controller.signal)
        this.client = client
        this.corePID = endpoint.pid
        return snapshot
      } finally {
        clearTimeout(timeout)
      }
    } catch {
      return undefined
    }
  }

  private resolveCorePath(): string {
    if (process.env.TUNNELX_CORE_PATH) return path.resolve(process.env.TUNNELX_CORE_PATH)
    if (app.isPackaged) return path.join(process.resourcesPath, 'core', 'tunnelx-cli.exe')
    return path.resolve(app.getAppPath(), '..', 'tunnelx-cli.exe')
  }

  private async getClient(): Promise<CoreApiClient> {
    if (!this.client) await this.initialize()
    if (!this.client) throw new Error('TunnelX 核心尚未就绪')
    return this.client
  }

  private async shutdownInternal(): Promise<void> {
    this.eventsAbort?.abort()
    this.eventsAbort = undefined
    this.eventLoopStarted = false

    const pid = this.corePID
    if (this.client) {
      const controller = new AbortController()
      const timeout = setTimeout(() => controller.abort(), 2000)
      try {
        await this.client.shutdown(controller.signal)
      } catch {
        // Older cores do not expose /v1/shutdown. The process fallback below
        // still ensures an explicit tray exit releases the executable.
      } finally {
        clearTimeout(timeout)
      }
    }

    let exited = await this.waitForCoreExit(pid, 4000)
    if (!exited && pid && existsSync(this.endpointPath)) {
      try {
        process.kill(pid)
      } catch {
        // It may have exited between the liveness check and termination.
      }
      exited = await this.waitForCoreExit(pid, 2000)
    }
    if (exited && existsSync(this.endpointPath)) {
      await unlink(this.endpointPath).catch(() => undefined)
    }

    this.client = undefined
    this.corePID = undefined
    this.initializePromise = undefined
    this.closeFrontend()

    if (!exited && existsSync(this.endpointPath)) {
      throw new Error('TunnelX 核心未能在退出超时内停止')
    }
  }

  private async waitForCoreExit(pid: number | undefined, timeoutMs: number): Promise<boolean> {
    if (!pid) return !existsSync(this.endpointPath)
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      if (!this.processExists(pid)) return true
      await delay(100)
    }
    return !this.processExists(pid)
  }

  private processExists(pid: number): boolean {
    try {
      process.kill(pid, 0)
      return true
    } catch (error) {
      return (error as NodeJS.ErrnoException).code === 'EPERM'
    }
  }

  private async refreshSnapshot(): Promise<SnapshotDTO> {
    let result: SnapshotDTO | undefined
    const refresh = this.snapshotRefreshQueue.then(async () => {
      const client = await this.getClient()
      const snapshot = await client.snapshot()
      result = snapshot
      this.emit({ type: 'snapshot', snapshot })
    })
    this.snapshotRefreshQueue = refresh.catch(() => undefined)
    await refresh
    if (!result) throw new Error('核心未返回状态快照')
    return result
  }

  private startEventLoop(): void {
    if (this.eventLoopStarted) return
    this.eventLoopStarted = true
    this.eventsAbort = new AbortController()
    void this.consumeEvents(this.eventsAbort.signal)
  }

  private async consumeEvents(signal: AbortSignal): Promise<void> {
    while (!signal.aborted) {
      try {
        const client = await this.getClient()
        await client.streamEvents(signal, event => this.handleCoreEvent(event))
      } catch (error) {
        if (signal.aborted) return
        this.updateStatus({ phase: 'reconnecting', message: this.message(error) })
        await delay(1000, undefined, { signal }).catch(() => undefined)
        const snapshot = await this.tryEndpoint()
        if (snapshot) {
          this.updateStatus({ phase: 'running', source: 'attached' })
          this.emit({ type: 'snapshot', snapshot })
        }
      }
    }
  }

  private async handleCoreEvent(event: CoreEventDTO): Promise<void> {
    const hasGap = this.lastSequence !== 0 && event.seq !== this.lastSequence + 1
    this.lastSequence = event.seq
    if (event.kind === 'log.appended' && event.log && !hasGap) {
      this.emit({ type: 'log', log: event.log })
      return
    }
    try {
      await this.refreshSnapshot()
    } catch (error) {
      this.emit({ type: 'error', message: this.message(error) })
    }
  }

  private updateStatus(status: CoreStatus): void {
    this.status = status
    this.emit({ type: 'core-status', status })
  }

  private fail(message: string): Error {
    this.updateStatus({ phase: 'error', message })
    return new Error(message)
  }

  private emit(event: DesktopEvent): void {
    for (const listener of this.listeners) {
      try {
        listener(event)
      } catch (error) {
        // A presentation listener (tray/window) must never turn a successful
        // core operation into a failed IPC request.
        console.error(`处理桌面事件 ${event.type} 失败:`, error)
      }
    }
  }

  private message(error: unknown): string {
    return error instanceof Error ? error.message : String(error)
  }
}
