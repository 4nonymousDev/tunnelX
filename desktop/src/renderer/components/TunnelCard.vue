<template>
  <article class="tunnel-card" :class="{ disabled: !tunnel.config.enabled }">
    <div class="card-main">
      <div class="card-icon">{{ tunnel.config.kind === 'export' ? '↑' : '↓' }}</div>
      <div class="card-copy">
        <div class="card-title-row">
          <h3 class="card-title">{{ displayName }}</h3>
          <span class="state-badge" :class="`state-${tunnel.state}`">{{ stateLabel }}</span>
        </div>
        <p class="route">{{ route }}</p>
        <p v-if="tunnel.reason" class="reason">{{ tunnel.reason }}</p>
      </div>
    </div>
    <div class="card-actions">
      <label class="switch" title="启用或停用">
        <input :checked="tunnel.config.enabled" type="checkbox" @change="emit('toggle', tunnel)" />
        <span class="switch-track" />
      </label>
      <button class="icon-button" type="button" title="编辑" @click="emit('edit', tunnel)">编辑</button>
      <button class="icon-button danger" type="button" title="删除" @click="emit('delete', tunnel)">删除</button>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { TunnelDTO } from '@shared/dto'

const props = defineProps<{ tunnel: TunnelDTO }>()
const emit = defineEmits<{
  edit: [tunnel: TunnelDTO]
  delete: [tunnel: TunnelDTO]
  toggle: [tunnel: TunnelDTO]
}>()

const displayName = computed(() => props.tunnel.config.name || `隧道 ${props.tunnel.index + 1}`)
const route = computed(() => {
  const config = props.tunnel.config
  if (config.kind === 'export') {
    const destination = props.tunnel.remote_port ? `服务端 :${props.tunnel.remote_port}` : '服务端动态端口'
    return `${config.local_host || '127.0.0.1'}:${config.local_port || '—'} → ${destination}`
  }
  const peer = config.peer_name || config.peer_id || '未选择对端'
  const server = props.tunnel.remote_port ? `服务端 :${props.tunnel.remote_port} → ` : ''
  return `本机 :${config.listen_port || '—'} → ${server}${peer}:${config.peer_src_port || '—'}`
})
const stateLabel = computed(() => ({
  stopped: '已停止',
  running: '运行中',
  reconnecting: '重连中',
  error: '错误',
  peer_offline: '对端离线',
  unknown: '未知',
}[props.tunnel.state] ?? props.tunnel.state))
</script>

<style scoped>
.tunnel-card { display: flex; align-items: center; justify-content: space-between; gap: 20px; padding: 18px; border: 1px solid var(--line); border-radius: 16px; background: var(--surface); transition: border-color .18s, transform .18s, opacity .18s; }
.tunnel-card:hover { border-color: rgba(111, 140, 255, .4); transform: translateY(-1px); }
.tunnel-card.disabled { opacity: .58; }
.card-main { display: flex; align-items: center; gap: 14px; min-width: 0; }
.card-icon { display: grid; place-items: center; width: 38px; height: 38px; flex: 0 0 auto; border-radius: 11px; color: var(--accent-light); font-size: 20px; background: rgba(91, 124, 250, .12); }
.card-copy { min-width: 0; }
.card-title-row { display: flex; align-items: center; gap: 9px; }
.card-title { margin: 0; overflow: hidden; font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
.route, .reason { margin: 6px 0 0; color: var(--muted); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 12px; }
.reason { color: var(--danger); font-family: inherit; }
.state-badge { padding: 3px 7px; border-radius: 999px; color: var(--muted); font-size: 10px; background: rgba(255,255,255,.05); }
.state-running { color: var(--success); background: rgba(56, 203, 137, .1); }
.state-error { color: var(--danger); background: rgba(255, 104, 117, .1); }
.state-peer_offline, .state-reconnecting { color: var(--warning); }
.card-actions { display: flex; align-items: center; gap: 7px; flex: 0 0 auto; }
.icon-button { padding: 7px 9px; border: 0; border-radius: 8px; color: var(--muted); font: inherit; font-size: 11px; background: transparent; cursor: pointer; }
.icon-button:hover { color: var(--text); background: rgba(255,255,255,.06); }
.icon-button.danger:hover { color: var(--danger); }
.switch { position: relative; width: 34px; height: 19px; margin-right: 3px; cursor: pointer; }
.switch input { position: absolute; opacity: 0; }
.switch-track { position: absolute; inset: 0; border-radius: 999px; background: #2c3449; transition: .18s; }
.switch-track::after { position: absolute; top: 3px; left: 3px; width: 13px; height: 13px; border-radius: 50%; background: #c7cede; content: ''; transition: .18s; }
.switch input:checked + .switch-track { background: var(--accent); }
.switch input:checked + .switch-track::after { left: 18px; background: white; }
</style>
