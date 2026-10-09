<template>
  <article class="panel realtime">
    <header class="heading"><div><h2>实时流量桑基图</h2><p class="muted">Exporter → Importer · 流带越宽，双向带宽越大</p></div><div class="live-total"><strong>{{ formatRate(totalRate) }}</strong><span>{{ connections }} 个转发连接{{ isFiltered ? ' · 当前筛选' : '' }}</span></div></header>
    <div class="toolbar">
      <TrafficDeviceFilter v-model="exporterSelection" side="exporter" :options="allExporters" />
      <TrafficDeviceFilter v-model="importerSelection" side="importer" :options="allImporters" />
      <p class="filter-hint muted">自由开关两侧设备，仅显示两端均开启的连接。</p>
    </div>
      <div ref="viewport" class="chart-scroll" :class="{ faded: stale }" aria-label="实时流量桑基图">
        <p v-if="!links.length" class="empty">暂无转发连接。设备建立隧道传输后，将在这里显示实时连线。</p>
        <div v-else-if="!filtered.length" class="empty filter-empty"><p>当前选择下没有可显示的连接。请开启两侧设备，或恢复全部显示。</p><button class="button secondary" type="button" @click="resetFilters">显示全部设备</button></div>
        <svg v-else class="chart" :viewBox="`0 0 ${layout.width} ${layout.height}`" role="group" aria-label="Exporter 与 Importer 实时流量桑基图。流带宽度表示本页双向带宽，可选择流带查看数值。">
          <defs><linearGradient v-for="(edge, index) in edges" :id="`${gradientId}-${index}`" :key="edge.key" gradientUnits="userSpaceOnUse" :x1="layout.leftX + 18" :x2="layout.rightX" y1="0" y2="0"><stop offset="0%" :stop-color="edge.color" /><stop offset="100%" stop-color="#91b8ff" /></linearGradient></defs>
          <text :x="layout.leftX" y="24" class="side-title exporter-color">EXPORTER</text><text :x="layout.rightX + 18" y="24" text-anchor="end" class="side-title importer-color">IMPORTER</text>
          <text :x="layout.leftX" y="43" class="axis-label">{{ layout.exporters.length }} 台设备</text><text :x="layout.rightX + 18" y="43" text-anchor="end" class="axis-label">{{ layout.importers.length }} 台设备</text>
          <g v-for="(edge, index) in edges" :key="edge.key" :class="{ dimmed: activeKey && activeKey !== edge.key, highlighted: activeKey === edge.key }">
            <path v-if="edge.width > 0" :d="edge.path" :fill="`url(#${gradientId}-${index})`" class="edge" />
            <path v-else :d="edge.centerPath" fill="none" :stroke="edge.color" stroke-width="1" stroke-dasharray="5 5" class="edge" />
            <path :d="edge.centerPath" fill="none" stroke="transparent" :stroke-width="Math.max(12, edge.width)" tabindex="0" role="button" :aria-label="edge.label" :aria-pressed="selected === edge.key" class="edge-hit" @mouseenter="hovered = edge.key" @mouseleave="hovered = ''" @focus="hovered = edge.key" @blur="hovered = ''" @click="toggle(edge.key)" @keydown.enter.prevent="toggle(edge.key)" @keydown.space.prevent="toggle(edge.key)"><title>{{ edge.label }}</title></path>
          </g>
          <g v-for="node in layout.exporters" :key="`l${node.id}`"><title>{{ node.name }} ({{ node.id }})：{{ formatRate(node.rate) }}</title><rect :x="node.x" :y="node.height ? node.y : node.labelY - 3" width="18" :height="node.height || 6" :fill="nodeColor(node.id)" :class="{ idle: !node.height }" /><text :x="node.x - 10" :y="node.labelY - 5" text-anchor="end" class="node-name">{{ shortName(node.name) }}</text><text :x="node.x - 10" :y="node.labelY + 12" text-anchor="end" class="node-rate exporter-color">{{ formatRate(node.rate) }}</text></g>
          <g v-for="node in layout.importers" :key="`r${node.id}`"><title>{{ node.name }} ({{ node.id }})：{{ formatRate(node.rate) }}</title><rect :x="node.x" :y="node.height ? node.y : node.labelY - 3" width="18" :height="node.height || 6" fill="#91b8ff" :class="{ idle: !node.height }" /><text :x="node.x + 28" :y="node.labelY - 5" class="node-name">{{ shortName(node.name) }}</text><text :x="node.x + 28" :y="node.labelY + 12" class="node-rate importer-color">{{ formatRate(node.rate) }}</text></g>
        </svg>
      </div>
      <p class="legend muted">节点与流带统计本页连接 · 零带宽为虚线 · 顶部带宽为全部筛选结果</p>
      <p class="selection" role="status" :title="activeEdge?.label"><span>{{ activeEdge?.label ?? '悬停、聚焦或点击流带，查看设备对的带宽和连接数。' }}</span><button v-if="selected" class="clear-selection" type="button" @click="selected = ''">取消</button></p>
      <footer class="chart-footer"><TrafficConnections :links="filtered" /><div class="pagination"><button class="button secondary" type="button" :disabled="page === 0" @click="page--">上一页</button><span>{{ page + 1 }} / {{ pages }}</span><button class="button secondary" type="button" :disabled="page + 1 >= pages" @click="page++">下一页</button></div></footer>
  </article>
</template>

<script setup lang="ts">
import { computed, shallowRef, useId, useTemplateRef, watch } from 'vue'
import { useElementSize } from '../../composables/useElementSize'
import type { TrafficLink } from '../../types/traffic'
import { formatRate, trafficLinkKey as linkKey, trafficNodes, trafficSankey } from '../../utils/traffic'
import { trafficDeviceEnabled } from '../../utils/trafficSelection'
import type { TrafficSelection } from '../../utils/trafficSelection'
import TrafficDeviceFilter from './TrafficDeviceFilter.vue'
import TrafficConnections from './TrafficConnections.vue'

const props = defineProps<{ links: readonly TrafficLink[]; stale: boolean }>()
const exporterSelection = shallowRef<TrafficSelection>({ defaultEnabled: true, exceptions: [] })
const importerSelection = shallowRef<TrafficSelection>({ defaultEnabled: true, exceptions: [] })
const selected = shallowRef('')
const hovered = shallowRef('')
const gradientId = `traffic-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`
const palette = ['#67d6bf', '#91b8ff', '#e8bf82', '#d7a7ef', '#7dd5eb', '#ef9f9f']
const allExporters = computed(() => trafficNodes(props.links, 'exporter'))
const allImporters = computed(() => trafficNodes(props.links, 'importer'))
const isFiltered = computed(() => filtered.value.length !== props.links.length)
const totalRate = computed(() => filtered.value.reduce((sum, link) => sum + link.bytes_per_second, 0))
const connections = computed(() => filtered.value.reduce((sum, link) => sum + link.connections, 0))
const filtered = computed(() => props.links.filter(link => trafficDeviceEnabled(exporterSelection.value, link.exporter.id) && trafficDeviceEnabled(importerSelection.value, link.importer.id)).sort((a, b) => linkKey(a).localeCompare(linkKey(b))))
const viewport = useTemplateRef<HTMLDivElement>('viewport')
const { width, height } = useElementSize(viewport)
const page = shallowRef(0)
const pageSize = computed(() => Math.max(1, Math.floor((height.value - 80) / 52)))
const pages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize.value)))
const visible = computed(() => filtered.value.slice(page.value * pageSize.value, (page.value + 1) * pageSize.value))
const layout = computed(() => trafficSankey(visible.value, { width: width.value, height: height.value }))
watch([exporterSelection, importerSelection], () => { page.value = 0 })
watch(pages, value => { page.value = Math.min(page.value, value - 1) })
const edges = computed(() => layout.value.edges.map(edge => ({ ...edge, color: nodeColor(edge.exporter.id), label: `${edge.exporter.name} (${edge.exporter.id}) ↔ ${edge.importer.name} (${edge.importer.id})：${formatRate(edge.bytes_per_second)}，${edge.connections} 个连接` })))
const activeKey = computed(() => hovered.value || selected.value)
const activeEdge = computed(() => edges.value.find(edge => edge.key === activeKey.value))
function resetFilters() {
  exporterSelection.value = { defaultEnabled: true, exceptions: [] }
  importerSelection.value = { defaultEnabled: true, exceptions: [] }
}
watch(edges, value => {
  if (selected.value && !value.some(edge => edge.key === selected.value)) selected.value = ''
  if (hovered.value && !value.some(edge => edge.key === hovered.value)) hovered.value = ''
})
function nodeColor(id: string) {
  let hash = 0
  for (const char of id) hash = (hash * 31 + char.charCodeAt(0)) >>> 0
  return palette[hash % palette.length]
}
function toggle(key: string) { selected.value = selected.value === key ? '' : key }
function shortName(value: string) {
  const limit = Math.max(3, Math.floor((layout.value.leftX - 12) / 12))
  const chars = Array.from(value)
  return chars.length > limit ? `${chars.slice(0, limit - 1).join('')}…` : value
}
</script>

<style scoped>
.realtime { height: 100%; min-height: 0; display: flex; flex-direction: column; overflow: hidden; }
.heading { display: flex; justify-content: space-between; gap: 1rem; flex-wrap: wrap; flex: none; }
.heading h2 { font-size: 1.15rem; margin: 0; }
.heading p { font-size: .8rem; line-height: 1.6; }
.live-total { display: grid; gap: .3rem; text-align: right; min-width: 13rem; font-variant-numeric: tabular-nums; }
.live-total strong { font-size: 1.5rem; color: var(--accent); }
.live-total span { font-size: .75rem; color: var(--muted); }
.toolbar { display: grid; grid-template-columns: repeat(2, minmax(0, 19rem)); gap: .7rem; margin: .8rem 0; flex: none; }
.filter-hint { grid-column: 1 / -1; margin: 0; font-size: .72rem; line-height: 1.6; }
.chart-scroll { flex: 1; min-height: 0; overflow: hidden; }
.chart-scroll:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
.empty { min-height: 100%; display: grid; place-items: center; margin: 0; }
.filter-empty { align-content: center; gap: .8rem; padding: 1.5rem; }
.filter-empty p { margin: 0; line-height: 1.7; }
.chart { display: block; width: 100%; height: 100%; }
.faded { opacity: .45; }
.axis-label { fill: var(--muted); font-size: 11px; }
.side-title { font-size: 13px; letter-spacing: 2px; font-weight: 700; }
.exporter-color { fill: var(--accent); }
.importer-color { fill: #91b8ff; }
.edge { opacity: .5; pointer-events: none; }
.highlighted .edge { opacity: .85; }
.edge-hit { cursor: pointer; outline: none; }
.edge-hit:focus-visible { stroke: var(--text); stroke-opacity: .2; }
.dimmed { opacity: .12; }
.idle { fill: transparent; stroke: var(--muted); stroke-dasharray: 3 2; }
.node-name { fill: var(--text); font-size: 12px; }
.node-rate { font-size: 11px; }
.legend { font-size: .75rem; line-height: 1.7; flex: none; }
.selection { height: 2.4rem; display: flex; align-items: center; gap: .5rem; flex: none; margin: 0 0 .6rem; padding: .5rem .7rem; border-radius: 6px; background: var(--panel-soft); font-size: .75rem; }
.selection > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.selection button { flex: none; margin-left: auto; }
.clear-selection { border: 0; background: none; color: var(--accent); cursor: pointer; }
.chart-footer,.pagination { display: flex; align-items: center; justify-content: space-between; gap: .6rem; font-size: .75rem; flex: none; }
.pagination .button { padding: .4rem .6rem; font-size: .75rem; }
.legend { margin: .45rem 0; white-space: nowrap; text-overflow: ellipsis; overflow: hidden; }
@media (max-width: 600px), (max-height: 760px) { .realtime { padding: .75rem; }.heading { align-items: center; gap: .5rem; flex-wrap: nowrap; }.heading h2 { font-size: .9rem; }.heading p,.filter-hint { display: none; }.live-total { min-width: 0; }.live-total strong { font-size: 1.15rem; }.live-total span { font-size: .65rem; }.toolbar { margin: .5rem 0; gap: .5rem; }.legend { font-size: .65rem; }.side-title { font-size: 11px; letter-spacing: 1px; }.node-rate { font-size: 9px; } }
</style>
