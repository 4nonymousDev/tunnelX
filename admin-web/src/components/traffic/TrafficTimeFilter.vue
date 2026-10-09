<template>
  <div class="time-filter">
    <div class="presets" role="group" aria-label="累计流量时间范围"><button v-for="item in presets" :key="item.id" type="button" :aria-pressed="preset === item.id" @click="select(item.id)">{{ item.label }}</button></div>
    <form v-if="preset === 'custom'" class="custom-range" @submit.prevent="applyCustom">
      <label>开始日期<input v-model="start" type="date" min="1970-01-01" max="9998-12-31" required /></label><span aria-hidden="true">—</span><label>结束日期<input v-model="end" type="date" :min="start || '1970-01-01'" max="9998-12-31" required /></label><button class="button secondary" type="submit">应用</button>
      <span v-if="error" class="range-error" role="alert">{{ error }}</span>
    </form>
  </div>
</template>

<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'
import type { TrafficRange } from '../../types/traffic'
const props = defineProps<{ sampledAt: string }>()
const emit = defineEmits<{ change: [range: TrafficRange | null] }>()
const presets = [{ id: 'all', label: '全部历史' }, { id: 'today', label: '今天' }, { id: 'week', label: '近 7 天' }, { id: 'month', label: '近 30 天' }, { id: 'custom', label: '自定义' }] as const
type Preset = typeof presets[number]['id']
const preset = shallowRef<Preset>('all')
function dateText(date: Date) { return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}` }
const today = computed(() => dateText(new Date(props.sampledAt)))
const start = shallowRef(today.value)
const end = shallowRef(today.value)
const error = shallowRef('')
function localDate(value: string) {
  const parts = value.split('-').map(Number)
  const date = new Date(parts[0]!, parts[1]! - 1, parts[2]!)
  return Number.isFinite(date.getTime()) && dateText(date) === value && date.getFullYear() >= 1970 && date.getFullYear() <= 9998 ? date : null
}
function emitRange(first: Date, last: Date, label: string) {
  const until = new Date(last)
  until.setDate(until.getDate() + 1)
  emit('change', { from: first.toISOString(), to: until.toISOString(), label })
}
function select(value: Preset) {
  preset.value = value
  error.value = ''
  if (value === 'all') { emit('change', null); return }
  if (value === 'custom') return
  const last = localDate(today.value)!
  const first = new Date(last)
  first.setDate(first.getDate() - (value === 'week' ? 6 : value === 'month' ? 29 : 0))
  start.value = dateText(first)
  end.value = dateText(last)
  emitRange(first, last, presets.find(item => item.id === value)!.label)
}
function applyCustom() {
  const first = localDate(start.value), last = localDate(end.value)
  if (!first || !last || first > last) { error.value = '请选择有效日期，结束日期不得早于开始日期'; return }
  error.value = ''
  emitRange(first, last, `${start.value} 至 ${end.value}`)
}
watch(today, () => { if (preset.value !== 'custom') select(preset.value) })
</script>

<style scoped>
.time-filter { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem .8rem; flex: none; }
.presets { display: flex; gap: .2rem; padding: .2rem; border: 1px solid var(--border); border-radius: 10px; background: var(--panel); }
.presets button { padding: .45rem .65rem; border: 0; border-radius: 6px; background: transparent; color: var(--muted); cursor: pointer; font-size: .75rem; white-space: nowrap; }
.presets button[aria-pressed="true"] { background: var(--accent-dark); color: var(--accent); }
.custom-range { display: flex; flex-wrap: wrap; align-items: center; gap: .4rem; color: var(--muted); font-size: .72rem; }
.custom-range label { display: flex; align-items: center; gap: .4rem; }
.custom-range input { width: 8.5rem; padding: .35rem .4rem; font-size: .72rem; }
.custom-range .button { padding: .4rem .6rem; font-size: .75rem; }
.range-error { color: var(--danger); }
@media (max-width: 600px) { .presets { width: 100%; justify-content: space-between; }.presets button { padding: .4rem .45rem; }.custom-range label { display: grid; gap: .2rem; }.custom-range input { width: 8rem; }.custom-range > span[aria-hidden] { display: none; }.custom-range .button { align-self: flex-end; } }
</style>
