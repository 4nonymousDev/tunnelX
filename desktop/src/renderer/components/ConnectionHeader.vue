<template>
  <header class="connection-header">
    <div class="brand">
      <span class="brand-mark">TX</span>
      <h1 class="brand-title">TunnelX</h1>
    </div>
    <div class="header-actions">
      <button v-if="updateAvailable" class="update-notice" type="button" @click="emit('update')">
        <span class="update-dot" />发现新版本
      </button>
      <span class="status-indicator" :class="statusClass" role="status" :title="stateLabel">
        <span class="sr-only">{{ stateLabel }}</span>
        <svg v-if="isConnecting" class="status-icon status-spinner" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <circle cx="12" cy="12" r="8" stroke="currentColor" stroke-width="2" opacity=".2" />
          <path d="M12 4a8 8 0 0 1 8 8" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
        </svg>
        <svg v-else class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <circle cx="12" cy="12" r="8" />
          <path v-if="statusState === 'connected'" d="m8 12 2.5 2.5L16 9" />
          <path v-else-if="statusState === 'failed' || statusState === 'error'" d="m9 9 6 6m0-6-6 6" />
          <path v-else-if="statusState === 'unknown'" d="M10 9a2 2 0 0 1 4 0c0 1.5-2 1.5-2 3m0 3h.01" />
          <path v-else d="M8 12h8" />
        </svg>
      </span>
      <button class="icon-button" type="button" :disabled="busy" aria-label="锁定界面" title="锁定界面" @click="emit('lock')">
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M7.5 10V7.5a4.5 4.5 0 0 1 9 0V10M6 10h12v10H6V10Z" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </button>
      <button class="icon-button" type="button" :disabled="busy" aria-label="设置" title="设置" @click="emit('settings')">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="m9 3-.6 2.4-1.2.7-2.4-.7-3 5.2 1.8 1.7v1.4l-1.8 1.7 3 5.2 2.4-.7 1.2.7L9 23h6l.6-2.4 1.2-.7 2.4.7 3-5.2-1.8-1.7v-1.4l1.8-1.7-3-5.2-2.4.7-1.2-.7L15 3Z" transform="translate(1.2 .3) scale(.9)" />
          <circle cx="12" cy="12" r="3" />
        </svg>
      </button>
      <button
        v-if="connection.state === 'idle' || connection.state === 'failed' || connection.state === 'unknown'"
        class="icon-button icon-button-connect"
        type="button"
        aria-label="连接"
        title="连接"
        :disabled="busy || corePhase !== 'running'"
        @click="emit('connect')"
      >
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="m9 5 10 7-10 7V5Z" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" /></svg>
      </button>
      <button v-else class="icon-button icon-button-disconnect" type="button" :disabled="busy" aria-label="断开连接" title="断开连接" @click="emit('disconnect')">
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M12 3v9m-5.7-6a8 8 0 1 0 11.4 0" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
      </button>
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { ConnectionDTO, CoreStatus } from '@shared/dto'

const props = defineProps<{
  connection: ConnectionDTO
  coreStatus: CoreStatus
  busy: boolean
  updateAvailable?: boolean
}>()

const emit = defineEmits<{
  connect: []
  disconnect: []
  settings: []
  lock: []
  update: []
}>()

const corePhase = computed(() => props.coreStatus.phase)
const stateLabel = computed(() => {
  if (props.coreStatus.phase === 'starting') return '核心启动中'
  if (props.coreStatus.phase === 'reconnecting') return '核心重连中'
  if (props.coreStatus.phase === 'error') return '核心异常'
  return {
    idle: '未连接',
    connecting: '连接中',
    connected: '已连接',
    retrying: '正在重试',
    failed: '连接失败',
    unknown: '状态未知',
  }[props.connection.state]
})
const statusState = computed(() => props.coreStatus.phase === 'running' ? props.connection.state : props.coreStatus.phase)
const statusClass = computed(() => `status-${statusState.value}`)
const isConnecting = computed(() => ['connecting', 'retrying', 'starting', 'reconnecting'].includes(statusState.value))
</script>

<style scoped>
.connection-header { display: flex; flex: 0 0 auto; align-items: center; justify-content: space-between; min-height: 48px; gap: 20px; padding: 7px 150px 7px 16px; border-bottom: 1px solid var(--line); background: #0c1223; user-select: none; -webkit-app-region: drag; }
.brand { display: flex; align-items: center; gap: 9px; min-width: 0; }
.brand-mark { display: grid; place-items: center; width: 28px; height: 28px; flex: 0 0 auto; border-radius: 8px; color: white; font-size: 10px; font-weight: 800; letter-spacing: -.04em; background: linear-gradient(145deg, #6f8cff, #4a62dc); box-shadow: 0 4px 16px rgba(91, 124, 250, .25); }
.brand-title { margin: 0; font-size: 16px; letter-spacing: -.02em; }
.header-actions { display: flex; align-items: center; gap: 8px; -webkit-app-region: no-drag; }
.update-notice { display: inline-flex; align-items: center; gap: 7px; padding: 6px 4px; border: 0; color: #67d8f3; font: inherit; font-size: 12px; font-weight: 700; background: transparent; cursor: pointer; }
.update-notice:hover { color: #a5efff; }
.update-dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; box-shadow: 0 0 12px currentColor; }
.status-indicator { display: grid; place-items: center; width: 30px; height: 30px; margin-right: 2px; color: var(--muted); }
.status-icon { width: 19px; height: 19px; }
.status-spinner { animation: status-spin .9s linear infinite; }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; border: 0; }
@keyframes status-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .status-spinner { animation: none; } }
.status-connected { color: var(--success); }
.status-connecting, .status-retrying, .status-starting, .status-reconnecting { color: var(--warning); }
.status-failed, .status-error { color: var(--danger); }
.icon-button { display: grid; width: 30px; height: 30px; place-items: center; padding: 0; border: 1px solid var(--line-strong); border-radius: 8px; color: var(--text); background: rgba(255,255,255,.035); cursor: pointer; transition: filter .15s, background .15s; }
.icon-button-connect { color: var(--success); }
.icon-button-disconnect { color: var(--danger); }
.icon-button:focus-visible { outline: 2px solid var(--text); outline-offset: 2px; }
.icon-button:hover:not(:disabled) { filter: brightness(1.15); background: rgba(255,255,255,.06); }
.icon-button:disabled { opacity: .45; cursor: not-allowed; }
.icon-button svg { width: 17px; height: 17px; }
</style>
