<template>
  <section class="log-panel">
    <div class="log-toolbar">
      <span>{{ logs.length }} 条日志</span>
      <div class="toolbar-actions">
        <button class="text-button" type="button" @click="emit('copyDiagnostics')">
          {{ diagnosticsCopied ? '诊断信息已复制' : '复制诊断信息' }}
        </button>
        <button class="text-button" type="button" @click="follow = !follow">{{ follow ? '自动滚动：开' : '自动滚动：关' }}</button>
      </div>
    </div>
    <div ref="logOutput" class="log-output">
      <div v-for="(entry, index) in logs" :key="`${entry.time}-${index}`" class="log-line">
        <time class="log-time">{{ formatTime(entry.time) }}</time>
        <span class="log-level" :class="`level-${entry.level.toLowerCase()}`">{{ entry.level }}</span>
        <span class="log-source">[{{ entry.source }}]</span>
        <span class="log-message">{{ entry.message }}</span>
      </div>
      <p v-if="!logs.length" class="log-empty">暂无日志</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { nextTick, shallowRef, useTemplateRef, watch } from 'vue'

import type { LogDTO } from '@shared/dto'

const props = defineProps<{ logs: LogDTO[]; diagnosticsCopied: boolean }>()
const emit = defineEmits<{ copyDiagnostics: [] }>()
const follow = shallowRef(true)
const logOutput = useTemplateRef<HTMLDivElement>('logOutput')

watch(
  () => props.logs.length,
  async () => {
    if (!follow.value) return
    await nextTick()
    if (logOutput.value) logOutput.value.scrollTop = logOutput.value.scrollHeight
  },
)

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleTimeString('zh-CN', { hour12: false })
}
</script>

<style scoped>
.log-panel { overflow: hidden; border: 1px solid var(--line); border-radius: 16px; background: #070b14; }
.log-toolbar { display: flex; justify-content: space-between; padding: 11px 14px; border-bottom: 1px solid var(--line); color: var(--muted); font-size: 11px; }
.toolbar-actions { display: flex; gap: 14px; }
.text-button { border: 0; color: var(--accent-light); font: inherit; background: transparent; cursor: pointer; }
.log-output { height: 410px; overflow: auto; padding: 12px 14px; font: 11px/1.7 ui-monospace, SFMono-Regular, Consolas, monospace; }
.log-line { display: grid; grid-template-columns: 74px 44px minmax(75px, auto) 1fr; gap: 8px; }
.log-time, .log-source { color: #657087; }
.log-level { color: #9aa5bb; }
.level-error, .level-fatal { color: var(--danger); }
.level-warn, .level-warning { color: var(--warning); }
.level-info { color: var(--accent-light); }
.log-message { min-width: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
.log-empty { color: var(--muted); text-align: center; }
</style>
