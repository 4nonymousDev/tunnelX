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
      <span class="status-pill" :class="statusClass">
        <span class="status-dot" />{{ stateLabel }}
      </span>
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
}>()

const emit = defineEmits<{
  connect: []
  disconnect: []
  settings: []
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
.connection-header { display: flex; align-items: center; justify-content: space-between; gap: 20px; padding: 22px 28px; border-bottom: 1px solid var(--line); background: rgba(12, 18, 35, .86); backdrop-filter: blur(20px); }
.brand { display: flex; align-items: center; gap: 13px; min-width: 0; }
.brand-mark { display: grid; place-items: center; width: 42px; height: 42px; flex: 0 0 auto; border-radius: 13px; color: white; font-size: 13px; font-weight: 800; letter-spacing: -.04em; background: linear-gradient(145deg, #6f8cff, #4a62dc); box-shadow: 0 8px 30px rgba(91, 124, 250, .35); }
.brand-title { margin: 0; font-size: 18px; letter-spacing: -.02em; }
.brand-subtitle { margin: 3px 0 0; overflow: hidden; color: var(--muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.header-actions { display: flex; align-items: center; gap: 10px; }
.status-pill { display: inline-flex; align-items: center; gap: 8px; margin-right: 4px; padding: 8px 12px; border: 1px solid var(--line); border-radius: 999px; color: var(--muted); font-size: 12px; }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; box-shadow: 0 0 12px currentColor; }
.status-connected { color: var(--success); }
.status-connecting, .status-retrying, .status-starting, .status-reconnecting { color: var(--warning); }
.status-failed, .status-error { color: var(--danger); }
</style>
