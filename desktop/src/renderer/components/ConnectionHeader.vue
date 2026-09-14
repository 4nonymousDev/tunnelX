<template>
  <header class="connection-header">
    <div class="brand">
      <span class="brand-mark">TX</span>
      <div>
        <h1 class="brand-title">TunnelX</h1>
        <p class="brand-subtitle">{{ subtitle }}</p>
      </div>
    </div>
    <div class="header-actions">
      <button v-if="updateAvailable" class="update-notice" type="button" @click="emit('update')">
        <span class="update-dot" />发现新版本
      </button>
      <span class="status-pill" :class="statusClass">
        <span class="status-dot" />{{ stateLabel }}
      </span>
      <button class="icon-button" type="button" :disabled="busy" aria-label="锁定界面" title="锁定界面" @click="emit('lock')">
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M7.5 10V7.5a4.5 4.5 0 0 1 9 0V10M6 10h12v10H6V10Z" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </button>
      <button class="button button-secondary" type="button" :disabled="busy" @click="emit('settings')">
        设置
      </button>
      <button
        v-if="connection.state === 'idle' || connection.state === 'failed' || connection.state === 'unknown'"
        class="button button-primary"
        type="button"
        :disabled="busy || corePhase !== 'running'"
        @click="emit('connect')"
      >
        连接
      </button>
      <button v-else class="button button-danger" type="button" :disabled="busy" @click="emit('disconnect')">
        断开
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
  serverAddress?: string
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
const statusClass = computed(() => `status-${props.coreStatus.phase === 'running' ? props.connection.state : props.coreStatus.phase}`)
const subtitle = computed(() => props.serverAddress || '尚未配置服务器')
</script>

<style scoped>
.connection-header { display: flex; flex: 0 0 auto; align-items: center; justify-content: space-between; min-height: 64px; gap: 20px; padding: 10px 150px 10px 20px; border-bottom: 1px solid var(--line); background: #0c1223; user-select: none; -webkit-app-region: drag; }
.brand { display: flex; align-items: center; gap: 13px; min-width: 0; }
.brand-mark { display: grid; place-items: center; width: 42px; height: 42px; flex: 0 0 auto; border-radius: 13px; color: white; font-size: 13px; font-weight: 800; letter-spacing: -.04em; background: linear-gradient(145deg, #6f8cff, #4a62dc); box-shadow: 0 8px 30px rgba(91, 124, 250, .35); }
.brand-title { margin: 0; font-size: 18px; letter-spacing: -.02em; }
.brand-subtitle { margin: 3px 0 0; overflow: hidden; color: var(--muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.header-actions { display: flex; align-items: center; gap: 10px; -webkit-app-region: no-drag; }
.update-notice { display: inline-flex; align-items: center; gap: 7px; padding: 6px 4px; border: 0; color: #67d8f3; font: inherit; font-size: 12px; font-weight: 700; background: transparent; cursor: pointer; }
.update-notice:hover { color: #a5efff; }
.update-dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; box-shadow: 0 0 12px currentColor; }
.status-pill { display: inline-flex; align-items: center; gap: 8px; margin-right: 4px; padding: 8px 12px; border: 1px solid var(--line); border-radius: 999px; color: var(--muted); font-size: 12px; }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; box-shadow: 0 0 12px currentColor; }
.status-connected { color: var(--success); }
.status-connecting, .status-retrying, .status-starting, .status-reconnecting { color: var(--warning); }
.status-failed, .status-error { color: var(--danger); }
.icon-button { display: grid; width: 34px; height: 34px; place-items: center; padding: 0; border: 1px solid var(--line-strong); border-radius: 9px; color: var(--text); background: rgba(255,255,255,.035); cursor: pointer; transition: filter .15s, background .15s; }
.icon-button:hover:not(:disabled) { filter: brightness(1.15); background: rgba(255,255,255,.06); }
.icon-button:disabled { opacity: .45; cursor: not-allowed; }
.icon-button svg { width: 17px; height: 17px; }
</style>
