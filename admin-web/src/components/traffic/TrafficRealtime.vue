<template>
  <article class="panel realtime">
    <header class="heading"><div><h2>实时流量桑基图</h2><p class="muted">Exporter → Importer · 流带越宽，双向带宽越大</p></div><div class="live-total"><strong>{{ formatRate(totalRate) }}</strong><span class="muted">{{ connections }} 个转发连接{{ exporter ? ' · 当前筛选' : '' }}</span></div></header>
    <div class="toolbar"><label>Exporter<select v-model="exporter"><option value="">全部设备</option><option v-for="node in allExporters" :key="node.id" :value="node.id">{{ node.name }} · {{ node.id }}</option></select></label><span class="muted">节点与流带使用统一比例；节点数值为当前筛选内的带宽合计。</span></div>
    <p v-if="!links.length" class="empty">暂无转发连接。设备建立隧道传输后，将在这里显示实时连线。</p>
    <p v-else-if="!filtered.length" class="empty">所选 Exporter 当前没有转发连接。</p>
    <template v-else>
      <div class="chart-scroll" :class="{ faded: stale }">
        <svg class="chart" :viewBox="`0 0 1000 ${layout.height}`" role="group" aria-label="Exporter 与 Importer 实时流量桑基图。流带宽度表示双向带宽，可选择流带查看数值。">
          <defs><linearGradient v-for="(edge, index) in edges" :id="`${gradientId}-${index}`" :key="edge.key" gradientUnits="userSpaceOnUse" x1="238" x2="762" y1="0" y2="0"><stop offset="0%" :stop-color="edge.color" /><stop offset="100%" stop-color="#91b8ff" /></linearGradient></defs>
          <text x="220" y="24" class="side-title exporter-color">EXPORTER</text><text x="780" y="24" text-anchor="end" class="side-title importer-color">IMPORTER</text>
          <text x="220" y="43" class="axis-label">{{ layout.exporters.length }} 台设备</text><text x="780" y="43" text-anchor="end" class="axis-label">{{ layout.importers.length }} 台设备</text>
          <g v-for="(edge, index) in edges" :key="edge.key" :class="{ dimmed: activeKey && activeKey !== edge.key, highlighted: activeKey === edge.key }">
            <path v-if="edge.width > 0" :d="edge.path" :fill="`url(#${gradientId}-${index})`" class="edge" />
            <path v-else :d="edge.centerPath" fill="none" :stroke="edge.color" stroke-width="1" stroke-dasharray="5 5" class="edge" />
            <path :d="edge.centerPath" fill="none" stroke="transparent" :stroke-width="Math.max(12, edge.width)" tabindex="0" role="button" :aria-label="edge.label" :aria-pressed="selected === edge.key" class="edge-hit" @mouseenter="hovered = edge.key" @mouseleave="hovered = ''" @focus="hovered = edge.key" @blur="hovered = ''" @click="toggle(edge.key)" @keydown.enter.prevent="toggle(edge.key)" @keydown.space.prevent="toggle(edge.key)"><title>{{ edge.label }}</title></path>
          </g>
          <g v-for="node in layout.exporters" :key="`l${node.id}`"><title>{{ node.name }} ({{ node.id }})：{{ formatRate(node.rate) }}</title><rect :x="node.x" :y="node.height ? node.y : node.labelY - 3" width="18" :height="node.height || 6" :fill="nodeColor(node.id)" :class="{ idle: !node.height }" /><text x="204" :y="node.labelY - 5" text-anchor="end" class="node-name">{{ shortName(node.name) }}</text><text x="204" :y="node.labelY + 12" text-anchor="end" class="node-rate exporter-color">{{ formatRate(node.rate) }}</text></g>
          <g v-for="node in layout.importers" :key="`r${node.id}`"><title>{{ node.name }} ({{ node.id }})：{{ formatRate(node.rate) }}</title><rect :x="node.x" :y="node.height ? node.y : node.labelY - 3" width="18" :height="node.height || 6" fill="#91b8ff" :class="{ idle: !node.height }" /><text x="796" :y="node.labelY - 5" class="node-name">{{ shortName(node.name) }}</text><text x="796" :y="node.labelY + 12" class="node-rate importer-color">{{ formatRate(node.rate) }}</text></g>
        </svg>
      </div>
      <p class="legend muted">流带表示双向流量合计，左右位置表示设备角色。零带宽连接以虚线和空心节点标记。</p>
      <p class="selection" role="status">{{ activeEdge?.label ?? '悬停、聚焦或点击流带，查看设备对的带宽和连接数。' }} <button v-if="selected" class="clear-selection" type="button" @click="selected = ''">取消选择</button></p>
      <details class="details"><summary>连线明细（{{ filtered.length }} 对）</summary><div class="table-scroll"><table><thead><tr><th>Exporter</th><th>Importer</th><th>双向带宽</th><th>连接数</th></tr></thead><tbody><tr v-for="link in filtered" :key="linkKey(link)"><td>{{ link.exporter.name }}<small>{{ link.exporter.id }}</small></td><td>{{ link.importer.name }}<small>{{ link.importer.id }}</small></td><td>{{ formatRate(link.bytes_per_second) }}</td><td>{{ link.connections }}</td></tr></tbody></table></div></details>
    </template>
  </article>
</template>

<script setup lang="ts">
import { computed, shallowRef, useId, watch } from 'vue'
import type { TrafficLink } from '../../types/traffic'
import { formatRate, trafficLinkKey as linkKey, trafficNodes, trafficSankey } from '../../utils/traffic'

const props = defineProps<{ links: readonly TrafficLink[]; stale: boolean }>()
const exporter = shallowRef('')
const selected = shallowRef('')
const hovered = shallowRef('')
const gradientId = `traffic-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`
const palette = ['#67d6bf', '#91b8ff', '#e8bf82', '#d7a7ef', '#7dd5eb', '#ef9f9f']
const allExporters = computed(() => trafficNodes(props.links, 'exporter'))
const totalRate = computed(() => filtered.value.reduce((sum, link) => sum + link.bytes_per_second, 0))
const connections = computed(() => filtered.value.reduce((sum, link) => sum + link.connections, 0))
const filtered = computed(() => props.links.filter(link => !exporter.value || link.exporter.id === exporter.value).sort((a, b) => b.bytes_per_second - a.bytes_per_second || linkKey(a).localeCompare(linkKey(b))))
const layout = computed(() => trafficSankey(filtered.value))
const edges = computed(() => layout.value.edges.map(edge => ({ ...edge, color: nodeColor(edge.exporter.id), label: `${edge.exporter.name} (${edge.exporter.id}) ↔ ${edge.importer.name} (${edge.importer.id})：${formatRate(edge.bytes_per_second)}，${edge.connections} 个连接` })))
const activeKey = computed(() => hovered.value || selected.value)
const activeEdge = computed(() => edges.value.find(edge => edge.key === activeKey.value))
watch(allExporters, nodes => { if (exporter.value && !nodes.some(node => node.id === exporter.value)) exporter.value = '' })
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
function shortName(value: string) { const chars = Array.from(value); return chars.length > 12 ? `${chars.slice(0, 11).join('')}…` : value }
</script>

<style scoped>
.heading { display: flex; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
.heading h2 { font-size: 1.15rem; margin: 0; }
.heading p { font-size: .8rem; line-height: 1.6; }
.live-total { display: grid; gap: .3rem; text-align: right; }
.live-total strong { font-size: 1.5rem; color: var(--accent); }
.live-total span { font-size: .75rem; }
.toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 1rem; font-size: .78rem; margin: 1rem 0; }
.toolbar label { display: flex; align-items: center; gap: .7rem; }
.toolbar select { max-width: min(22rem, 60vw); }
.chart-scroll { overflow: auto; max-height: 44rem; }
.chart { display: block; width: 100%; min-width: 850px; }
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
.legend { font-size: .75rem; line-height: 1.7; }
.selection { min-height: 2.8rem; padding: .8rem; border-radius: 6px; background: var(--panel-soft); font-size: .8rem; overflow-wrap: anywhere; }
.clear-selection { border: 0; background: none; color: var(--accent); cursor: pointer; }
.details { margin-top: 1rem; font-size: .8rem; }
.details summary { cursor: pointer; color: var(--muted); }
.table-scroll { overflow: auto; max-height: 28rem; margin-top: 1rem; }
.table-scroll table { width: 100%; border-collapse: collapse; }
.table-scroll th,.table-scroll td { padding: .7rem; text-align: left; border-bottom: 1px solid var(--border); }
.table-scroll small { display: block; color: var(--muted); margin-top: .3rem; overflow-wrap: anywhere; }
</style>
