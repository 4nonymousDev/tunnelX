import { computed, onMounted, onUnmounted, shallowReadonly, shallowRef } from 'vue'

import type {
  CoreStatus,
  SettingsDTO,
  SnapshotDTO,
  TunnelConfigDTO,
} from '@shared/dto'
import type { DesktopEvent, TunnelXDesktopAPI } from '@shared/ipc'
import { normalizeSnapshot } from '@shared/snapshot'

const BRIDGE_UNAVAILABLE = '桌面通信桥未加载。请通过 Electron 启动 TunnelX（开发环境请使用 npm run dev），不要直接在浏览器打开 Vite 页面。'

export function useTunnelX() {
  const snapshot = shallowRef<SnapshotDTO>()
  const status = shallowRef<CoreStatus>({ phase: 'starting' })
  const logs = shallowRef<NonNullable<SnapshotDTO['logs']>>([])
  const busy = shallowRef(false)
  const error = shallowRef('')
  let unsubscribe: (() => void) | undefined
  let desktop: TunnelXDesktopAPI | undefined

  const connection = computed(() => snapshot.value?.connection ?? { state: 'unknown' as const })
  const tunnels = computed(() => snapshot.value?.tunnels ?? [])
  const pendingConfirmation = computed(() => snapshot.value?.pending_confirmations?.[0])

  onMounted(async () => {
    desktop = window.tunnelx
    if (!desktop) {
      status.value = { phase: 'error', message: BRIDGE_UNAVAILABLE }
      error.value = BRIDGE_UNAVAILABLE
      return
    }
    unsubscribe = desktop.onEvent(handleEvent)
    await execute(async () => {
      const result = await bridge().bootstrap()
      status.value = result.status
      applySnapshot(result.snapshot)
    })
  })

  onUnmounted(() => unsubscribe?.())

  function handleEvent(event: DesktopEvent): void {
    if (event.type === 'snapshot') applySnapshot(event.snapshot)
    if (event.type === 'log') logs.value = [...logs.value, event.log].slice(-1000)
    if (event.type === 'core-status') status.value = event.status
    if (event.type === 'error') error.value = event.message
  }

  function applySnapshot(value: SnapshotDTO): void {
    const normalized = normalizeSnapshot(value)
    snapshot.value = normalized
    logs.value = normalized.logs ?? logs.value
  }

  async function refresh(): Promise<void> {
    await invoke(() => bridge().refresh())
  }

  async function connect(): Promise<void> {
    await invoke(() => bridge().connect())
  }

  async function disconnect(): Promise<void> {
    await invoke(() => bridge().disconnect())
  }

  async function addTunnel(tunnel: TunnelConfigDTO): Promise<void> {
    await invoke(() => bridge().addTunnel(tunnel))
  }

  async function addTunnels(tunnels: TunnelConfigDTO[]): Promise<void> {
    await invoke(() => bridge().addTunnels(tunnels))
  }

  async function updateTunnel(id: string, tunnel: TunnelConfigDTO): Promise<void> {
    await invoke(() => bridge().updateTunnel(id, tunnel))
  }

  async function deleteTunnel(id: string): Promise<void> {
    await invoke(() => bridge().deleteTunnel(id))
  }

  async function updateSettings(settings: SettingsDTO): Promise<void> {
    await invoke(() => bridge().updateSettings(settings))
  }

  async function copyText(text: string): Promise<void> {
    await execute(() => bridge().copyText(text))
  }

  async function confirm(id: number, accept: boolean): Promise<void> {
    await invoke(async () => {
      const next = await bridge().confirm(id, accept)
      applySnapshot({
        ...next,
        pending_confirmations: (next.pending_confirmations ?? []).filter(item => item.id !== id),
      })
      return next
    }, false)
  }

  function clearError(): void {
    error.value = ''
  }

  function bridge(): TunnelXDesktopAPI {
    desktop ??= window.tunnelx
    if (!desktop) throw new Error(BRIDGE_UNAVAILABLE)
    return desktop
  }

  async function invoke(operation: () => Promise<SnapshotDTO>, apply = true): Promise<void> {
    await execute(async () => {
      const next = await operation()
      if (apply) applySnapshot(next)
    })
  }

  async function execute(operation: () => Promise<void>): Promise<void> {
    busy.value = true
    error.value = ''
    try {
      await operation()
    } catch (reason) {
      error.value = reason instanceof Error ? reason.message : String(reason)
    } finally {
      busy.value = false
    }
  }

  return {
    snapshot: shallowReadonly(snapshot),
    status: shallowReadonly(status),
    logs: shallowReadonly(logs),
    busy: shallowReadonly(busy),
    error: shallowReadonly(error),
    connection,
    tunnels,
    pendingConfirmation,
    refresh,
    connect,
    disconnect,
    addTunnel,
    addTunnels,
    updateTunnel,
    deleteTunnel,
    updateSettings,
    copyText,
    confirm,
    clearError,
  }
}
