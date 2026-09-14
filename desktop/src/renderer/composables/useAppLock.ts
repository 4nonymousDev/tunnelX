import { computed, onMounted, shallowReadonly, shallowRef } from 'vue'

import type { TunnelXDesktopAPI } from '@shared/ipc'

const BRIDGE_UNAVAILABLE = '桌面通信桥未加载，无法读取界面锁定状态。'

export function useAppLock() {
  const initialized = shallowRef(false)
  const locked = shallowRef(false)
  const busy = shallowRef(false)
  const error = shallowRef('')
  const initializationError = shallowRef('')
  let desktop: TunnelXDesktopAPI | undefined

  const ready = computed(() => initialized.value && !initializationError.value)

  onMounted(async () => {
    desktop = window.tunnelx
    if (!desktop) {
      error.value = BRIDGE_UNAVAILABLE
      initializationError.value = BRIDGE_UNAVAILABLE
      initialized.value = true
      return
    }
    try {
      const state = await desktop.getLockState()
      locked.value = state.locked
    } catch (reason) {
      error.value = message(reason)
      initializationError.value = error.value
    } finally {
      initialized.value = true
    }
  })

  async function lock(password: string): Promise<boolean> {
    return changeState(() => bridge().lockInterface(password))
  }

  async function unlock(password: string): Promise<boolean> {
    return changeState(() => bridge().unlockInterface(password))
  }

  function clearError(): void {
    error.value = ''
  }

  function bridge(): TunnelXDesktopAPI {
    desktop ??= window.tunnelx
    if (!desktop) throw new Error(BRIDGE_UNAVAILABLE)
    return desktop
  }

  async function changeState(operation: () => ReturnType<TunnelXDesktopAPI['getLockState']>): Promise<boolean> {
    busy.value = true
    error.value = ''
    try {
      const state = await operation()
      locked.value = state.locked
      return true
    } catch (reason) {
      error.value = message(reason)
      return false
    } finally {
      busy.value = false
    }
  }

  return {
    initialized: shallowReadonly(initialized),
    ready,
    locked: shallowReadonly(locked),
    busy: shallowReadonly(busy),
    error: shallowReadonly(error),
    initializationError: shallowReadonly(initializationError),
    lock,
    unlock,
    clearError,
  }
}

function message(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason)
}
