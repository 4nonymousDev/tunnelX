import path from 'node:path'

import { clipboard, dialog, ipcMain, type BrowserWindow, type IpcMainInvokeEvent } from 'electron'

import type { KeyGenerationRequestDTO, SettingsDTO, TunnelConfigDTO } from '../shared/dto'
import {
  IPC,
  type ConfirmationRequest,
  type UpdateTunnelRequest,
} from '../shared/ipc'
import type { CoreSupervisor } from './core-supervisor'
import type { InterfaceLockManager } from './lock-manager'
import type { UpdateManager } from './update-manager'

export function registerIpc(supervisor: CoreSupervisor, updates: UpdateManager, interfaceLock: InterfaceLockManager, window: () => BrowserWindow | undefined): void {
  const handle = (channel: string, listener: (event: IpcMainInvokeEvent, value: unknown) => unknown, requiresUnlock = true): void => {
    ipcMain.handle(channel, (event, value: unknown) => {
      if (event.sender !== window()?.webContents || event.senderFrame !== event.sender.mainFrame) throw new Error('无效的桌面请求来源')
      if (requiresUnlock) interfaceLock.assertUnlocked()
      return listener(event, value)
    })
  }
  handle(IPC.bootstrap, async () => {
    const snapshot = await supervisor.initialize()
    return { status: supervisor.getStatus(), snapshot }
  })
  handle(IPC.refresh, () => supervisor.refresh())
  handle(IPC.connect, () => supervisor.connect())
  handle(IPC.disconnect, () => supervisor.disconnect())
  handle(IPC.addTunnel, (_event, value: unknown) => supervisor.addTunnel(tunnel(value)))
  handle(IPC.addTunnels, (_event, value: unknown) => supervisor.addTunnels(tunnelBatch(value)))
  handle(IPC.updateTunnel, (_event, value: unknown) => {
    const request = record(value) as Partial<UpdateTunnelRequest>
    return supervisor.updateTunnel(text(request.id, '隧道 ID'), tunnel(request.tunnel))
  })
  handle(IPC.deleteTunnel, (_event, value: unknown) => {
    return supervisor.deleteTunnel(text(value, '隧道 ID'))
  })
  handle(IPC.updateSettings, (_event, value: unknown) => supervisor.updateSettings(settings(value)))
  handle(IPC.selectKeyDirectory, async (_event, value: unknown) => {
    if (typeof value !== 'string') throw new Error('私钥路径无效')
    const currentKeyPath = value.trim()
    const defaultPath = currentKeyPath && path.isAbsolute(currentKeyPath)
      ? path.dirname(currentKeyPath)
      : undefined
    const fileName = currentKeyPath ? path.basename(currentKeyPath) : 'tunnel_key'
    const result = await dialog.showOpenDialog({
      title: '选择 SSH Key 生成目录',
      defaultPath,
      properties: ['openDirectory', 'createDirectory'],
    })
    if (result.canceled || !result.filePaths[0]) return undefined
    return path.join(result.filePaths[0], fileName || 'tunnel_key')
  })
  handle(IPC.generateKey, (_event, value: unknown) => supervisor.generateKey(keyGeneration(value)))
  handle(IPC.copyText, (_event, value: unknown) => {
    if (typeof value !== 'string' || value.length > 1_000_000) throw new Error('诊断文本无效或过大')
    clipboard.writeText(value)
  })
  handle(IPC.confirm, (_event, value: unknown) => {
    const request = record(value) as Partial<ConfirmationRequest>
    if (!Number.isSafeInteger(request.id) || Number(request.id) <= 0) throw new Error('无效的确认请求 ID')
    if (typeof request.accept !== 'boolean') throw new Error('无效的确认结果')
    return supervisor.confirm(Number(request.id), request.accept)
  })
  handle(IPC.getUpdateState, () => updates.getState())
  handle(IPC.checkForUpdates, () => updates.checkForUpdates())
  handle(IPC.downloadUpdate, () => updates.downloadUpdate())
  handle(IPC.installUpdate, () => updates.installUpdate())
  handle(IPC.getLockState, () => interfaceLock.getState(), false)
  handle(IPC.lockInterface, (_event, value: unknown) => interfaceLock.lock(lockPassword(value)))
  handle(IPC.unlockInterface, (_event, value: unknown) => interfaceLock.unlock(lockPassword(value)), false)
}

function lockPassword(value: unknown): string {
  if (typeof value !== 'string') throw new Error('密码格式无效')
  return value
}

function tunnelBatch(value: unknown): TunnelConfigDTO[] {
  if (!Array.isArray(value) || value.length === 0) throw new Error('至少需要一条隧道')
  return value.map(tunnel)
}

function tunnel(value: unknown): TunnelConfigDTO {
  const input = record(value) as Partial<TunnelConfigDTO>
  const kind = input.kind === 'export' || input.kind === 'import' ? input.kind : undefined
  if (!kind) throw new Error('隧道类型必须是 export 或 import')
  return {
    id: optionalText(input.id),
    kind,
    name: optionalText(input.name),
    enabled: Boolean(input.enabled),
    local_host: optionalText(input.local_host),
    local_port: optionalPort(input.local_port),
    peer_id: optionalText(input.peer_id),
    peer_tunnel_id: optionalText(input.peer_tunnel_id),
    peer_name: optionalText(input.peer_name),
    peer_src_port: optionalPort(input.peer_src_port),
    listen_port: optionalPort(input.listen_port),
  }
}

function settings(value: unknown): SettingsDTO {
  const input = record(value) as Partial<SettingsDTO>
  return {
    name: optionalText(input.name),
    server_addr: optionalText(input.server_addr),
    key_path: optionalText(input.key_path),
  }
}

function keyGeneration(value: unknown): KeyGenerationRequestDTO {
  const input = record(value) as Partial<KeyGenerationRequestDTO>
  return {
    key_path: text(input.key_path, '私钥路径').trim(),
    username: optionalText(input.username),
    email: optionalText(input.email),
  }
}

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('无效的请求数据')
  return value as Record<string, unknown>
}

function text(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${label}不能为空`)
  return value
}

function optionalText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function optionalPort(value: unknown): number {
  if (value === undefined || value === null || value === '') return 0
  const port = Number(value)
  if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error('端口必须是 0 到 65535 的整数')
  return port
}
