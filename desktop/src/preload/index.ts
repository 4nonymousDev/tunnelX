import { contextBridge, ipcRenderer } from 'electron'

import type { KeyGenerationRequestDTO, SettingsDTO, TunnelConfigDTO } from '../shared/dto'
import type { DesktopEvent, TunnelXDesktopAPI } from '../shared/ipc'

// Sandboxed preload scripts only have Electron's limited require polyfill and
// cannot load ../shared/ipc at runtime. Keep the channel literals local so the
// emitted preload is a single self-contained file; shared imports above are
// type-only and disappear during compilation.
const IPC = {
  bootstrap: 'tunnelx:bootstrap',
  refresh: 'tunnelx:refresh',
  connect: 'tunnelx:connect',
  disconnect: 'tunnelx:disconnect',
  addTunnel: 'tunnelx:tunnel:add',
  addTunnels: 'tunnelx:tunnel:add-batch',
  updateTunnel: 'tunnelx:tunnel:update',
  deleteTunnel: 'tunnelx:tunnel:delete',
  updateSettings: 'tunnelx:settings:update',
  selectKeyDirectory: 'tunnelx:key:select-directory',
  generateKey: 'tunnelx:key:generate',
  copyText: 'tunnelx:clipboard:write',
  confirm: 'tunnelx:confirm',
  getUpdateState: 'tunnelx:update:get-state',
  checkForUpdates: 'tunnelx:update:check',
  downloadUpdate: 'tunnelx:update:download',
  installUpdate: 'tunnelx:update:install',
  event: 'tunnelx:event',
} as const

const api: TunnelXDesktopAPI = {
  bootstrap: () => ipcRenderer.invoke(IPC.bootstrap),
  refresh: () => ipcRenderer.invoke(IPC.refresh),
  connect: () => ipcRenderer.invoke(IPC.connect),
  disconnect: () => ipcRenderer.invoke(IPC.disconnect),
  addTunnel: (tunnel: TunnelConfigDTO) => ipcRenderer.invoke(IPC.addTunnel, tunnel),
  addTunnels: (tunnels: TunnelConfigDTO[]) => ipcRenderer.invoke(IPC.addTunnels, tunnels),
  updateTunnel: (id: string, tunnel: TunnelConfigDTO) => ipcRenderer.invoke(IPC.updateTunnel, { id, tunnel }),
  deleteTunnel: (id: string) => ipcRenderer.invoke(IPC.deleteTunnel, id),
  updateSettings: (settings: SettingsDTO) => ipcRenderer.invoke(IPC.updateSettings, settings),
  selectKeyDirectory: (keyPath: string) => ipcRenderer.invoke(IPC.selectKeyDirectory, keyPath),
  generateKey: (request: KeyGenerationRequestDTO) => ipcRenderer.invoke(IPC.generateKey, request),
  copyText: (text: string) => ipcRenderer.invoke(IPC.copyText, text),
  confirm: (id: number, accept: boolean) => ipcRenderer.invoke(IPC.confirm, { id, accept }),
  getUpdateState: () => ipcRenderer.invoke(IPC.getUpdateState),
  checkForUpdates: () => ipcRenderer.invoke(IPC.checkForUpdates),
  downloadUpdate: () => ipcRenderer.invoke(IPC.downloadUpdate),
  installUpdate: () => ipcRenderer.invoke(IPC.installUpdate),
  onEvent: (listener: (event: DesktopEvent) => void) => {
    const handler = (_event: Electron.IpcRendererEvent, payload: DesktopEvent) => listener(payload)
    ipcRenderer.on(IPC.event, handler)
    return () => ipcRenderer.removeListener(IPC.event, handler)
  },
}

contextBridge.exposeInMainWorld('tunnelx', api)
