import { app } from 'electron'
import { autoUpdater, type ProgressInfo, type UpdateInfo } from 'electron-updater'

import type { UpdateState } from '../shared/ipc'
import { releaseNoteAsPlainText } from '../shared/release-notes'

const FIRST_CHECK_DELAY_MS = 15_000
const CHECK_INTERVAL_MS = 4 * 60 * 60 * 1000

export class UpdateManager {
  private state: UpdateState = {
    phase: app.isPackaged ? 'idle' : 'unsupported',
    currentGuiVersion: app.getVersion(),
    currentCliVersion: '未知',
    message: app.isPackaged ? undefined : '开发模式不检查更新，请安装发布版后使用。',
  }

  private readonly listeners = new Set<(state: UpdateState) => void>()
  private firstCheckTimer?: NodeJS.Timeout
  private intervalTimer?: NodeJS.Timeout
  private checkPromise?: Promise<UpdateState>
  private updateReady = false

  constructor(private readonly prepareInstall: () => Promise<void>) {
    autoUpdater.autoDownload = false
    autoUpdater.autoInstallOnAppQuit = false
    autoUpdater.allowPrerelease = false
    autoUpdater.logger = console

    autoUpdater.on('checking-for-update', () => {
      this.patch({ phase: 'checking', message: '正在检查更新…' })
    })
    autoUpdater.on('update-available', info => {
      this.updateReady = false
      this.patch({
        phase: 'available',
        latestGuiVersion: info.version,
        releaseName: info.releaseName || undefined,
        releaseNotes: releaseNotes(info),
        checkedAt: new Date().toISOString(),
        message: '发现新版本',
        percent: undefined,
        bytesPerSecond: undefined,
        transferred: undefined,
        total: undefined,
      })
    })
    autoUpdater.on('update-not-available', () => {
      this.updateReady = false
      this.patch({
        phase: 'not-available',
        latestGuiVersion: undefined,
        releaseName: undefined,
        releaseNotes: undefined,
        checkedAt: new Date().toISOString(),
        message: '当前已是最新版本。',
        percent: undefined,
        bytesPerSecond: undefined,
        transferred: undefined,
        total: undefined,
      })
    })
    autoUpdater.on('download-progress', progress => this.onProgress(progress))
    autoUpdater.on('update-downloaded', info => {
      this.updateReady = true
      this.patch({
        phase: 'downloaded',
        latestGuiVersion: info.version,
        percent: 100,
        message: '更新已下载，正在准备安装…',
      })
    })
    autoUpdater.on('error', error => {
      this.patch({ phase: 'error', message: message(error) })
    })
  }

  start(): void {
    if (!app.isPackaged) return
    this.firstCheckTimer = setTimeout(() => void this.checkForUpdates(), FIRST_CHECK_DELAY_MS)
    this.firstCheckTimer.unref()
    this.intervalTimer = setInterval(() => void this.checkForUpdates(), CHECK_INTERVAL_MS)
    this.intervalTimer.unref()
  }

  stop(): void {
    if (this.firstCheckTimer) clearTimeout(this.firstCheckTimer)
    if (this.intervalTimer) clearInterval(this.intervalTimer)
    this.firstCheckTimer = undefined
    this.intervalTimer = undefined
  }

  subscribe(listener: (state: UpdateState) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  getState(): UpdateState {
    return { ...this.state }
  }

  setCoreVersion(version: string | undefined): void {
    const normalized = version?.trim() || '未知'
    if (normalized === this.state.currentCliVersion) return
    this.patch({ currentCliVersion: normalized })
  }

  checkForUpdates(): Promise<UpdateState> {
    if (!app.isPackaged) return Promise.resolve(this.getState())
    if (this.state.phase === 'downloading' || this.state.phase === 'installing') {
      return Promise.resolve(this.getState())
    }
    if (!this.checkPromise) {
      this.checkPromise = autoUpdater.checkForUpdates()
        .then(() => this.getState())
        .catch(error => {
          this.patch({ phase: 'error', message: message(error) })
          return this.getState()
        })
        .finally(() => { this.checkPromise = undefined })
    }
    return this.checkPromise
  }

  async downloadUpdate(): Promise<UpdateState> {
    if (!app.isPackaged) return this.getState()
    if (this.updateReady) {
      this.patch({ phase: 'downloaded', percent: 100, message: '更新已下载，正在准备安装…' })
      return this.getState()
    }
    if (this.state.phase !== 'available' && this.state.phase !== 'error') {
      throw new Error('当前没有可下载的更新')
    }
    this.patch({
      phase: 'downloading',
      percent: 0,
      bytesPerSecond: 0,
      transferred: 0,
      total: undefined,
      message: '正在下载更新…',
    })
    this.updateReady = false
    try {
      await autoUpdater.downloadUpdate()
    } catch (error) {
      this.patch({ phase: 'error', message: message(error) })
    }
    return this.getState()
  }

  async installUpdate(): Promise<void> {
    if (!this.updateReady) throw new Error('更新尚未下载完成')
    this.stop()
    this.patch({ phase: 'installing', message: '正在停止核心并启动安装程序…' })
    try {
      await this.prepareInstall()
      autoUpdater.quitAndInstall(false, true)
    } catch (error) {
      this.patch({ phase: 'error', message: `无法开始安装：${message(error)}` })
      throw error
    }
  }

  private onProgress(progress: ProgressInfo): void {
    this.patch({
      phase: 'downloading',
      percent: Math.max(0, Math.min(100, progress.percent)),
      bytesPerSecond: progress.bytesPerSecond,
      transferred: progress.transferred,
      total: progress.total,
      message: '正在下载更新…',
    })
  }

  private patch(value: Partial<UpdateState>): void {
    this.state = { ...this.state, ...value }
    const snapshot = this.getState()
    for (const listener of this.listeners) {
      try {
        listener(snapshot)
      } catch (error) {
        console.error('处理更新状态失败:', error)
      }
    }
  }
}

function releaseNotes(info: UpdateInfo): string | undefined {
  if (typeof info.releaseNotes === 'string') return releaseNoteAsPlainText(info.releaseNotes) || undefined
  if (!Array.isArray(info.releaseNotes)) return undefined
  const notes = info.releaseNotes
    .map(item => [item.version, item.note ? releaseNoteAsPlainText(item.note) : ''].filter(Boolean).join('\n'))
    .filter(Boolean)
  return notes.length ? notes.join('\n\n') : undefined
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}
