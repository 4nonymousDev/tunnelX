<template>
  <div v-if="!initialized" class="lock-bootstrap" aria-label="正在读取界面锁定状态">
    <span class="loading-mark">TX</span>
    <span>正在启动…</span>
  </div>
  <div v-else-if="!ready" class="lock-bootstrap lock-error" role="alert">
    <span class="loading-mark">!</span>
    <span>{{ initializationError }}</span>
  </div>
  <LockScreen v-else-if="locked" :busy="busy" :error="error" @unlock="unlock" />
  <TunnelWorkspace v-else @lock="openLockDialog" />
  <LockPasswordDialog
    :open="lockDialogOpen"
    :busy="busy"
    :error="error"
    @close="closeLockDialog"
    @submit="setPasswordAndLock"
  />
</template>

<script setup lang="ts">
import { shallowRef } from 'vue'

import LockPasswordDialog from './components/LockPasswordDialog.vue'
import LockScreen from './components/LockScreen.vue'
import TunnelWorkspace from './components/TunnelWorkspace.vue'
import { useAppLock } from './composables/useAppLock'

const { initialized, ready, locked, busy, error, initializationError, lock, unlock, clearError } = useAppLock()
const lockDialogOpen = shallowRef(false)

function openLockDialog(): void {
  clearError()
  lockDialogOpen.value = true
}

function closeLockDialog(): void {
  if (busy.value) return
  lockDialogOpen.value = false
  clearError()
}

async function setPasswordAndLock(password: string): Promise<void> {
  if (await lock(password)) lockDialogOpen.value = false
}
</script>

<style scoped>
.lock-bootstrap { display: grid; width: 100%; height: 100%; place-content: center; justify-items: center; gap: 14px; color: var(--muted); font-size: 12px; user-select: none; -webkit-app-region: drag; }
.loading-mark { display: grid; width: 44px; height: 44px; place-items: center; border-radius: 14px; color: white; font-size: 13px; font-weight: 800; background: linear-gradient(145deg, #6f8cff, #4a62dc); box-shadow: 0 8px 30px rgba(91,124,250,.3); }
.lock-error { color: #ffb0b8; }
.lock-error .loading-mark { background: var(--danger); box-shadow: 0 8px 30px rgba(255,104,117,.2); }
</style>
