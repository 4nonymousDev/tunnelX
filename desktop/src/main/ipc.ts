import path from 'node:path'

import { clipboard, dialog, ipcMain } from 'electron'

import type { KeyGenerationRequestDTO, SettingsDTO, TunnelConfigDTO } from '../shared/dto'
import {
  IPC,
  type ConfirmationRequest,
  type UpdateTunnelRequest,
} from '../shared/ipc'
import type { CoreSupervisor } from './core-supervisor'

export function registerIpc(supervisor: CoreSupervisor): void {
  ipcMain.handle(IPC.bootstrap, async () => {
    const snapshot = await supervisor.initialize()
    return { status: supervisor.getStatus(), snapshot }
  })
  ipcMain.handle(IPC.refresh, () => supervisor.refresh())
  ipcMain.handle(IPC.connect, () => supervisor.connect())
  ipcMain.handle(IPC.disconnect, () => supervisor.disconnect())
  ipcMain.handle(IPC.addTunnel, (_event, value: unknown) => supervisor.addTunnel(tunnel(value)))
  ipcMain.handle(IPC.addTunnels, (_event, value: unknown) => supervisor.addTunnels(tunnelBatch(value)))
  ipcMain.handle(IPC.updateTunnel, (_event, value: unknown) => {
    const request = record(value) as Partial<UpdateTunnelRequest>
    return supervisor.updateTunnel(text(request.id, '隧道 ID'), tunnel(request.tunnel))
  })
  ipcMain.handle(IPC.deleteTunnel, (_event, value: unknown) => {
    return supervisor.deleteTunnel(text(value, '隧道 ID'))
  })
  ipcMain.handle(IPC.updateSettings, (_event, value: unknown) => supervisor.updateSettings(settings(value)))
  ipcMain.handle(IPC.selectKeyDirectory, async (_event, value: unknown) => {
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
  ipcMain.handle(IPC.generateKey, (_event, value: unknown) => supervisor.generateKey(keyGeneration(value)))
  ipcMain.handle(IPC.copyText, (_event, value: unknown) => {
    if (typeof value !== 'string' || value.length > 1_000_000) throw new Error('诊断文本无效或过大')
    clipboard.writeText(value)
  })
  ipcMain.handle(IPC.confirm, (_event, value: unknown) => {
    const request = record(value) as Partial<ConfirmationRequest>
    if (!Number.isSafeInteger(request.id) || Number(request.id) <= 0) throw new Error('无效的确认请求 ID')
    if (typeof request.accept !== 'boolean') throw new Error('无效的确认结果')
    return supervisor.confirm(Number(request.id), request.accept)
  })
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
    username: text(input.username, '用户名').trim(),
    email: text(input.email, '邮箱').trim(),
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
