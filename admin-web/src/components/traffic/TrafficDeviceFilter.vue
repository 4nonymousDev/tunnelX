<template>
  <div class="device-filter" :class="{ importer: side === 'importer' }">
    <button ref="trigger" class="filter-trigger" type="button" :class="{ expanded: opened, filtered: selectedCount !== options.length }" :popovertarget="menuId" :aria-controls="menuId" :aria-expanded="opened" aria-haspopup="dialog" :aria-label="`${label} 设备开关，已开启 ${selectedCount} / ${options.length} 台`">
      <span class="role-icon" aria-hidden="true">{{ side === 'exporter' ? 'E' : 'I' }}</span>
      <span class="trigger-copy"><strong>{{ label }}</strong><span>{{ summary }}</span></span>
      <span class="count-badge">{{ selectedCount }}<span> / {{ options.length }}</span></span>
      <svg class="chevron" :class="{ rotated: opened }" width="16" height="16" viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="m5 7 5 5 5-5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
    </button>
    <div :id="menuId" ref="menu" class="filter-menu" popover="auto" role="dialog" :aria-label="`${label} 设备开关`" :style="menuStyle" @beforetoggle="onBeforeToggle" @toggle="onToggle">
      <header class="menu-heading"><div><strong>{{ label }} 设备</strong><span>选择要显示的设备</span></div><button class="close-button" type="button" :aria-label="`关闭 ${label} 设备菜单`" @click="closeMenu"><svg width="16" height="16" viewBox="0 0 20 20" fill="none" aria-hidden="true"><path d="m5 5 10 10M15 5 5 15" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" /></svg></button></header>
      <div class="search-wrap"><svg width="16" height="16" viewBox="0 0 20 20" fill="none" aria-hidden="true"><circle cx="8.5" cy="8.5" r="5.5" stroke="currentColor" stroke-width="1.5" /><path d="m13 13 4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg><input v-model="query" class="search-input" type="search" :aria-label="`搜索 ${label} 设备`" placeholder="搜索名称或设备 ID" autocomplete="off" autofocus /></div>
      <div class="menu-actions"><span>{{ selectedCount }} / {{ options.length }} 已开启</span><div><button type="button" @click="setAll(true)">全部开启</button><span aria-hidden="true">·</span><button type="button" @click="setAll(false)">全部关闭</button></div></div>
      <div class="device-list" role="group" :aria-label="`${label} 设备列表`">
        <label v-for="option in visibleOptions" :key="option.id" class="device-option" :class="{ enabled: isEnabled(option.id) }">
          <input class="device-checkbox" type="checkbox" :checked="isEnabled(option.id)" :aria-label="`${option.name} (${option.id})`" @change="toggleDevice(option.id)" />
          <span class="device-copy"><strong :title="option.name">{{ option.name }}</strong><span :title="option.id">{{ option.id }}</span></span>
          <span class="device-rate">{{ formatRate(option.rate) }}</span>
        </label>
        <p v-if="!visibleOptions.length" class="menu-empty">{{ options.length ? '没有找到匹配的设备' : '当前没有活跃连接设备' }}</p>
      </div>
      <footer class="menu-footer">设备带宽为筛选前的合计 · 更改立即生效</footer>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, shallowRef, useId, useTemplateRef } from 'vue'
import type { CSSProperties } from 'vue'
import { formatRate } from '../../utils/traffic'
import type { TrafficNode } from '../../utils/traffic'
import { toggleTrafficDevice, trafficDeviceEnabled } from '../../utils/trafficSelection'
import type { TrafficSelection } from '../../utils/trafficSelection'

const props = defineProps<{ side: 'exporter' | 'importer'; options: readonly TrafficNode[]; modelValue: TrafficSelection }>()
const emit = defineEmits<{ 'update:modelValue': [value: TrafficSelection] }>()
const label = computed(() => props.side === 'exporter' ? 'Exporter' : 'Importer')
const menuId = `traffic-filter-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`
const trigger = useTemplateRef<HTMLButtonElement>('trigger')
const menu = useTemplateRef<HTMLDivElement>('menu')
const opened = shallowRef(false)
const query = shallowRef('')
const menuStyle = shallowRef<CSSProperties>({})
const selectedCount = computed(() => props.options.filter(option => isEnabled(option.id)).length)
const summary = computed(() => {
  if (!props.options.length) return '等待设备连接'
  if (!selectedCount.value) return '全部已关闭'
  return selectedCount.value === props.options.length ? '全部设备已开启' : '部分设备已开启'
})
const visibleOptions = computed(() => {
  const search = query.value.trim().toLocaleLowerCase()
  return props.options.filter(option => `${option.name} ${option.id}`.toLocaleLowerCase().includes(search)).sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id))
})
function isEnabled(id: string) { return trafficDeviceEnabled(props.modelValue, id) }
function toggleDevice(id: string) { emit('update:modelValue', toggleTrafficDevice(props.modelValue, id)) }
function setAll(defaultEnabled: boolean) { emit('update:modelValue', { defaultEnabled, exceptions: [] }) }
function positionMenu() {
  if (!trigger.value) return
  const rect = trigger.value.getBoundingClientRect()
  const margin = 12, gap = 8
  const width = Math.min(380, window.innerWidth - margin * 2)
  const below = window.innerHeight - rect.bottom - gap - margin
  const above = rect.top - gap - margin
  const fullHeight = Math.max(below, above) < 240
  const upwards = !fullHeight && below < 340 && above > below
  menuStyle.value = {
    width: `${width}px`,
    left: `${Math.max(margin, Math.min(rect.left, window.innerWidth - width - margin))}px`,
    top: fullHeight ? `${margin}px` : upwards ? 'auto' : `${rect.bottom + gap}px`,
    bottom: upwards ? `${window.innerHeight - rect.top + gap}px` : 'auto',
    maxHeight: `${Math.min(460, fullHeight ? window.innerHeight - margin * 2 : upwards ? above : below)}px`,
  }
}
function onBeforeToggle(event: Event) {
  if ((event as ToggleEvent).newState === 'open') { query.value = ''; positionMenu() }
}
function onToggle(event: Event) { opened.value = (event as ToggleEvent).newState === 'open' }
function closeMenu() { menu.value?.hidePopover(); trigger.value?.focus() }
function onViewportChange(event: Event) {
  if (!opened.value || (event.target instanceof Node && menu.value?.contains(event.target))) return
  positionMenu()
}
onMounted(() => {
  window.addEventListener('resize', onViewportChange)
  window.addEventListener('scroll', onViewportChange, true)
})
onUnmounted(() => {
  window.removeEventListener('resize', onViewportChange)
  window.removeEventListener('scroll', onViewportChange, true)
})
</script>

<style scoped>
.device-filter { --filter-accent: #61d4bd; --filter-tint: #153b41; min-width: 0; }
.device-filter.importer { --filter-accent: #91b8ff; --filter-tint: #1b304b; }
.filter-trigger { display: flex; align-items: center; gap: .7rem; width: 100%; min-height: 4rem; padding: .65rem .8rem; border: 1px solid var(--border); border-radius: 12px; background: var(--panel-soft); color: var(--text); text-align: left; cursor: pointer; transition: border-color .15s, background-color .15s; }
.filter-trigger:hover,.filter-trigger.expanded { border-color: var(--filter-accent); background: var(--filter-tint); }
.filter-trigger:focus-visible,.close-button:focus-visible,.menu-actions button:focus-visible { outline: 2px solid var(--filter-accent); outline-offset: 3px; }
.role-icon { display: grid; place-items: center; flex: none; width: 2rem; height: 2rem; border-radius: 9px; background: var(--filter-tint); color: var(--filter-accent); font-size: .8rem; font-weight: 800; }
.trigger-copy { display: grid; gap: .25rem; min-width: 0; }
.trigger-copy strong { font-size: .82rem; }
.trigger-copy > span { color: var(--muted); font-size: .68rem; white-space: nowrap; }
.count-badge { margin-left: auto; padding: .25rem .4rem; border-radius: 6px; background: #07101366; color: var(--filter-accent); white-space: nowrap; font-size: .74rem; font-variant-numeric: tabular-nums; }
.count-badge span { color: var(--muted); }
.chevron { flex: none; color: var(--muted); transition: transform .15s; }
.chevron.rotated { transform: rotate(180deg); }
.filter-menu { position: fixed; inset: auto; margin: 0; padding: 0; display: none; overflow: auto; border: 1px solid #315052; border-radius: 16px; background: #0d1d21; color: var(--text); box-shadow: 0 20px 60px #0009, 0 0 0 1px #ffffff05; }
.filter-menu:popover-open { display: flex; flex-direction: column; }
.filter-menu::backdrop { background: transparent; }
.menu-heading { display: flex; align-items: center; justify-content: space-between; gap: .75rem; padding: 1rem 1rem .8rem; flex: none; }
.menu-heading > div { display: grid; gap: .3rem; }
.menu-heading strong { font-size: .88rem; }
.menu-heading span { color: var(--muted); font-size: .72rem; }
.close-button { display: grid; place-items: center; width: 1.8rem; height: 1.8rem; padding: 0; border: 0; border-radius: 7px; background: var(--panel-soft); color: var(--muted); cursor: pointer; }
.close-button:hover { color: var(--text); }
.search-wrap { position: relative; margin: 0 1rem; flex: none; }
.search-wrap > svg { position: absolute; top: 50%; left: .7rem; transform: translateY(-50%); color: var(--muted); pointer-events: none; }
.search-input { padding: .65rem .65rem .65rem 2.1rem; border-radius: 9px; font-size: .78rem; }
.search-input:focus { border-color: var(--filter-accent); }
.menu-actions { display: flex; align-items: center; justify-content: space-between; gap: .6rem; padding: .85rem 1rem .6rem; color: var(--muted); font-size: .7rem; flex: none; }
.menu-actions > div { display: flex; align-items: center; gap: .5rem; }
.menu-actions button { padding: .15rem 0; border: 0; background: none; color: var(--filter-accent); font-size: inherit; cursor: pointer; }
.device-list { flex: 1; min-height: 0; overflow: auto; scrollbar-gutter: stable; padding: .15rem .45rem .45rem; }
.device-option { display: flex; align-items: center; gap: .65rem; padding: .65rem .55rem; border-radius: 9px; cursor: pointer; }
.device-option:hover,.device-option:focus-within { background: var(--panel-soft); }
.device-option.enabled { background: #ffffff03; }
.device-checkbox { flex: none; width: 1rem; height: 1rem; margin: 0; padding: 0; accent-color: var(--filter-accent); cursor: pointer; }
.device-checkbox:focus-visible { outline: 2px solid var(--filter-accent); outline-offset: 3px; box-shadow: none; }
.device-copy { display: grid; gap: .3rem; flex: 1; min-width: 0; }
.device-copy strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: .8rem; font-weight: 500; }
.device-copy > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); font-size: .66rem; }
.device-rate { flex: none; color: var(--filter-accent); font-size: .68rem; font-variant-numeric: tabular-nums; }
.menu-empty { padding: 1.5rem .5rem; text-align: center; color: var(--muted); font-size: .8rem; }
.menu-footer { flex: none; padding: .75rem 1rem; border-top: 1px solid var(--border); color: var(--muted); font-size: .65rem; }
@media (max-width: 600px), (max-height: 760px) { .filter-trigger { min-height: 2.6rem; padding: .45rem .6rem; gap: .4rem; }.trigger-copy > span,.role-icon { display: none; }.chevron { width: 12px; }.count-badge { font-size: .68rem; padding: .2rem; } }
@media (prefers-reduced-motion: reduce) { .filter-trigger,.chevron { transition: none; } }
</style>
