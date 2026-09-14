<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="close">
    <section class="dialog" role="dialog" aria-modal="true" aria-labelledby="update-title">
      <div class="dialog-heading">
        <div>
          <p class="eyebrow">SOFTWARE UPDATE</p>
          <h2 id="update-title" class="dialog-title">{{ title }}</h2>
        </div>
        <button v-if="canClose" class="dialog-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </div>

      <template v-if="state.phase === 'available'">
        <p class="description">TunnelX {{ state.latestGuiVersion }} 已发布，是否现在下载并安装？安装完成后应用会自动重新启动。</p>
        <div class="version-grid">
          <span>当前 GUI</span><strong>{{ state.currentGuiVersion }}</strong>
          <span>最新 GUI</span><strong class="latest">{{ state.latestGuiVersion }}</strong>
          <span>当前 CLI</span><strong>{{ state.currentCliVersion }}</strong>
        </div>
        <div v-if="state.releaseNotes" class="release-notes">
          <strong>版本说明</strong>
          <p>{{ state.releaseNotes }}</p>
        </div>
        <div class="dialog-actions">
          <button class="button button-secondary" type="button" @click="emit('close')">稍后</button>
          <button class="button button-primary" type="button" @click="emit('confirm')">立即更新</button>
        </div>
      </template>

      <template v-else-if="state.phase === 'downloading' || state.phase === 'downloaded' || state.phase === 'installing'">
        <p class="stage-copy">{{ stageMessage }}</p>
        <div class="progress-track" role="progressbar" aria-label="更新进度" :aria-valuenow="progress" aria-valuemin="0" aria-valuemax="100">
          <span class="progress-value" :style="{ width: `${progress}%` }" />
        </div>
        <div class="progress-meta">
          <strong>{{ progress.toFixed(1) }}%</strong>
          <span>{{ transferLabel }}</span>
        </div>
        <p class="install-hint">下载期间可以继续使用现有隧道。开始安装时核心会安全退出，随后由安装程序替换文件。</p>
      </template>

      <template v-else-if="state.phase === 'error'">
        <p class="error-message">{{ state.message || '更新失败，请检查网络后重试。' }}</p>
        <div class="dialog-actions">
          <button class="button button-secondary" type="button" @click="emit('close')">关闭</button>
          <button v-if="state.latestGuiVersion" class="button button-primary" type="button" @click="emit('confirm')">{{ state.percent === 100 ? '重试安装' : '重新下载' }}</button>
        </div>
      </template>

      <template v-else>
        <p class="stage-copy">{{ state.message || '正在检查是否有新版本…' }}</p>
      </template>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { UpdateState } from '@shared/ipc'

const props = defineProps<{
  open: boolean
  state: UpdateState
}>()

const emit = defineEmits<{
  close: []
  confirm: []
}>()

const progress = computed(() => Math.max(0, Math.min(100, props.state.percent ?? 0)))
const canClose = computed(() => !['downloading', 'downloaded', 'installing'].includes(props.state.phase))
const title = computed(() => {
  if (props.state.phase === 'available') return '发现新版本'
  if (props.state.phase === 'downloading') return '正在下载更新'
  if (props.state.phase === 'downloaded') return '下载完成'
  if (props.state.phase === 'installing') return '正在准备安装'
  if (props.state.phase === 'error') return '更新未完成'
  return '检查更新'
})
const stageMessage = computed(() => {
  if (props.state.phase === 'downloaded') return '更新包下载完成，正在进入安装阶段…'
  if (props.state.phase === 'installing') return '正在停止 TunnelX 核心并启动安装程序，请勿关闭应用…'
  return props.state.message || '正在从 GitHub Releases 下载更新…'
})
const transferLabel = computed(() => {
  if (props.state.phase !== 'downloading') return props.state.phase === 'installing' ? '即将重新启动' : '下载已完成'
  const transferred = formatBytes(props.state.transferred)
  const total = formatBytes(props.state.total)
  const speed = formatBytes(props.state.bytesPerSecond)
  return `${transferred} / ${total}${speed ? ` · ${speed}/s` : ''}`
})

function close(): void {
  if (canClose.value) emit('close')
}

function formatBytes(value: number | undefined): string {
  if (!value || value < 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB']
  let amount = value
  let unit = 0
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024
    unit += 1
  }
  return `${amount.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 80; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2, 5, 13, .76); backdrop-filter: blur(8px); }
.dialog { width: min(540px, 100%); padding: 24px; border: 1px solid var(--line-strong); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px rgba(0,0,0,.55); }
.dialog-heading { display: flex; align-items: flex-start; justify-content: space-between; margin-bottom: 20px; }
.eyebrow { margin: 0 0 5px; color: #67d8f3; font-size: 10px; font-weight: 800; letter-spacing: .14em; }
.dialog-title { margin: 0; font-size: 20px; }
.dialog-close { border: 0; color: var(--muted); font-size: 26px; line-height: 1; background: transparent; cursor: pointer; }
.description, .stage-copy, .install-hint { color: #b9c3d7; font-size: 12px; line-height: 1.7; }
.version-grid { display: grid; grid-template-columns: 1fr auto; gap: 10px 18px; margin: 20px 0; padding: 15px; border: 1px solid var(--line); border-radius: 12px; color: var(--muted); font-size: 12px; background: rgba(255,255,255,.025); }
.version-grid strong { color: var(--text); font-family: Consolas, monospace; }
.version-grid .latest { color: #67d8f3; }
.release-notes { max-height: 160px; padding: 14px; overflow: auto; border: 1px solid var(--line); border-radius: 12px; background: rgba(255,255,255,.02); }
.release-notes strong { font-size: 12px; }
.release-notes p { margin: 8px 0 0; color: var(--muted); font-size: 11px; line-height: 1.65; white-space: pre-wrap; }
.progress-track { height: 9px; margin-top: 22px; overflow: hidden; border-radius: 999px; background: rgba(255,255,255,.08); }
.progress-value { display: block; height: 100%; border-radius: inherit; background: linear-gradient(90deg, #5b7cfa, #67d8f3); transition: width .2s ease; }
.progress-meta { display: flex; justify-content: space-between; margin-top: 9px; color: var(--muted); font-size: 11px; }
.progress-meta strong { color: #9ee9fa; }
.install-hint { margin: 20px 0 0; color: var(--muted); font-size: 11px; }
.error-message { padding: 12px 14px; border: 1px solid rgba(255,104,117,.25); border-radius: 10px; color: #ffb0b8; font-size: 12px; background: rgba(255,104,117,.08); }
.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 22px; }
</style>
