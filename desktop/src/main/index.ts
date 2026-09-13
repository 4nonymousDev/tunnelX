import path from 'node:path'

import {
  app,
  BrowserWindow,
  Menu,
  Tray,
} from 'electron'

import { IPC } from '../shared/ipc'
import type { CoreStatus, SnapshotDTO } from '../shared/dto'
import { CoreSupervisor } from './core-supervisor'
import { registerIpc } from './ipc'
import { trayIcon, trayPresentation, type TrayState } from './tray'

let mainWindow: BrowserWindow | undefined
let tray: Tray | undefined
let supervisor: CoreSupervisor | undefined
let isQuitting = false
let quitRequested = false
let coreShutdownComplete = false
let latestSnapshot: SnapshotDTO | undefined
let latestCoreStatus: CoreStatus = { phase: 'starting' }
let currentTrayState: TrayState | undefined

const hasLock = app.requestSingleInstanceLock()
if (!hasLock) {
  app.quit()
} else {
  app.on('second-instance', showWindow)
  void app.whenReady().then(startDesktop)
}

async function startDesktop(): Promise<void> {
  supervisor = new CoreSupervisor()
  registerIpc(supervisor)
  supervisor.subscribe(event => {
    if (event.type === 'snapshot') latestSnapshot = event.snapshot
    if (event.type === 'core-status') latestCoreStatus = event.status
    updateTrayAppearance()
    if (mainWindow && !mainWindow.isDestroyed()) {
      mainWindow.webContents.send(IPC.event, event)
    }
  })
  createWindow()
  createTray()
  void supervisor.initialize().then(snapshot => {
    latestSnapshot = snapshot
    latestCoreStatus = supervisor?.getStatus() ?? latestCoreStatus
    updateTrayAppearance()
  }).catch(() => {
    latestCoreStatus = supervisor?.getStatus() ?? { phase: 'error', message: '核心启动失败' }
    updateTrayAppearance()
  })

  app.on('activate', showWindow)
}

function createWindow(): void {
  mainWindow = new BrowserWindow({
    width: 1160,
    height: 760,
    minWidth: 900,
    minHeight: 620,
    show: false,
    title: 'TunnelX',
    backgroundColor: '#0b1020',
    autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, '..', 'preload', 'index.js'),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webSecurity: true,
    },
  })

  mainWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }))
  mainWindow.webContents.on('will-navigate', event => event.preventDefault())
  mainWindow.once('ready-to-show', () => mainWindow?.show())
  mainWindow.on('close', event => {
    if (!isQuitting) {
      event.preventDefault()
      mainWindow?.hide()
    }
  })

  const devUrl = process.env.VITE_DEV_SERVER_URL
  if (devUrl) {
    const parsed = new URL(devUrl)
    if (parsed.protocol !== 'http:' || parsed.hostname !== '127.0.0.1' || parsed.port !== '5173') {
      throw new Error('VITE_DEV_SERVER_URL 必须是 http://127.0.0.1:5173')
    }
    void mainWindow.loadURL(parsed.toString())
  } else {
    void mainWindow.loadFile(path.join(__dirname, '..', '..', 'dist', 'index.html'))
  }
}

function createTray(): void {
  tray = new Tray(trayIcon('idle'))
  tray.setContextMenu(Menu.buildFromTemplate([
    { label: '显示 TunnelX', click: showWindow },
    { type: 'separator' },
    {
      label: '退出 TunnelX',
      click: requestQuit,
    },
  ]))
  tray.on('double-click', showWindow)
  updateTrayAppearance()
}

function updateTrayAppearance(): void {
  if (!tray || tray.isDestroyed()) return
  const presentation = trayPresentation(latestSnapshot, latestCoreStatus)
  if (presentation.state !== currentTrayState) {
    tray.setImage(trayIcon(presentation.state))
    currentTrayState = presentation.state
  }
  tray.setToolTip(presentation.tooltip)
}

function showWindow(): void {
  if (!mainWindow || mainWindow.isDestroyed()) return
  if (mainWindow.isMinimized()) mainWindow.restore()
  mainWindow.show()
  mainWindow.focus()
}

function requestQuit(): void {
  if (quitRequested) return
  quitRequested = true
  isQuitting = true
  tray?.setToolTip('TunnelX — 正在停止核心…')
  void (async () => {
    try {
      await supervisor?.shutdown()
    } catch (error) {
      console.error('停止 TunnelX 核心失败:', error)
    } finally {
      coreShutdownComplete = true
      app.quit()
    }
  })()
}

app.on('before-quit', (event) => {
  isQuitting = true
  if (supervisor && !coreShutdownComplete) {
    event.preventDefault()
    requestQuit()
    return
  }
  supervisor?.closeFrontend()
  tray?.destroy()
})

app.on('window-all-closed', () => {
  // Keep the tray process alive. The user exits explicitly from the tray menu.
})
