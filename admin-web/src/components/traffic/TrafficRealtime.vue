<template>
  <article class="panel realtime">
    <header class="heading"><div><h2>实时连接流量</h2><p class="muted">双向有效载荷 · 节点高度为设备总带宽 · 连线宽度为设备对带宽</p></div><div class="live-total"><strong>{{ formatRate(totalRate) }}</strong><span class="muted">{{ connections }} 个转发连接</span></div></header>
    <div class="toolbar"><label>Exporter<select v-model="exporter"><option value="">全部设备</option><option v-for="node in allExporters" :key="node.id" :value="node.id">{{ node.name }} · {{ node.id }}</option></select></label><span class="muted">左、右纵轴独立缩放，请按各自刻度读数。</span></div>
    <p v-if="!links.length" class="empty">暂无转发连接。设备建立隧道传输后，将在这里显示实时连线。</p>
    <p v-else-if="!displayed.length" class="empty">所选 Exporter 当前没有转发连接。</p>
    <template v-else>
      <p v-if="filtered.length > limit" class="limit-note">当前绘制带宽最高的 {{ limit }} 对连接，共 {{ filtered.length }} 对。筛选 Exporter 可查看其连接；节点数值始终为该设备的全部连接合计。</p>
      <div class="chart-scroll" :class="{ faded: stale }">
        <svg class="chart" :viewBox="`0 0 1000 ${height}`" role="img" aria-label="Exporter 与 Importer 实时带宽关系图，左右纵轴分别缩放。完整数值见下方连线明细。">
          <text x="115" y="24" class="side-title exporter-color">EXPORTER</text><text x="885" y="24" text-anchor="end" class="side-title importer-color">IMPORTER</text>
          <text x="115" y="43" class="axis-label">{{ leftNodes.length }} 台 · {{ formatRate(leftScale.max) }} 满量程</text><text x="885" y="43" text-anchor="end" class="axis-label">{{ rightNodes.length }} 台 · {{ formatRate(rightScale.max) }} 满量程</text>
          <line x1="95" :y1="66" x2="95" :y2="height - 62" class="axis" /><line x1="905" :y1="66" x2="905" :y2="height - 62" class="axis" />
          <g v-for="tick in leftScale.ticks" :key="`l${tick}`"><line x1="89" :y1="axisY(tick, leftScale.max)" x2="103" :y2="axisY(tick, leftScale.max)" class="axis" /><text x="83" :y="axisY(tick, leftScale.max) + 4" text-anchor="end" class="tick">{{ formatRate(tick) }}</text></g>
          <g v-for="tick in rightScale.ticks" :key="`r${tick}`"><line x1="897" :y1="axisY(tick, rightScale.max)" x2="911" :y2="axisY(tick, rightScale.max)" class="axis" /><text x="917" :y="axisY(tick, rightScale.max) + 4" class="tick">{{ formatRate(tick) }}</text></g>
          <g v-for="edge in edges" :key="edge.key" :class="{ dimmed: selected && selected !== edge.key }">
            <path :d="edge.path" fill="none" :stroke="edge.color" :stroke-width="edge.width" :stroke-dasharray="edge.bytes_per_second === 0 ? '5 5' : undefined" class="edge" />
            <path :d="edge.path" fill="none" stroke="transparent" :stroke-width="Math.max(14, edge.width)" tabindex="0" role="button" :aria-label="edge.label" :aria-pressed="selected === edge.key" class="edge-hit" @click="toggle(edge.key)" @keydown.enter.prevent="toggle(edge.key)" @keydown.space.prevent="toggle(edge.key)"><title>{{ edge.label }}</title></path>
          </g>
          <g v-for="node in leftNodes" :key="`l${node.id}`"><path :d="`M 242 ${node.labelY} L ${node.x} ${node.y}`" class="leader" /><circle :cx="node.x" :cy="node.y" r="5" class="exporter-dot"><title>{{ node.name }} ({{ node.id }})：{{ formatRate(node.rate) }}</title></circle><text x="236" :y="node.labelY - 5" text-anchor="end" class="node-name">{{ shortName(node.name) }}<title>{{ node.name }} · {{ node.id }}</title></text><text x="236" :y="node.labelY + 10" text-anchor="end" class="node-rate exporter-color">{{ formatRate(node.rate) }}</text></g>
          <g v-for="node in rightNodes" :key="`r${node.id}`"><path :d="`M 758 ${node.labelY} L ${node.x} ${node.y}`" class="leader" /><circle :cx="node.x" :cy="node.y" r="5" class="importer-dot"><title>{{ node.name }} ({{ node.id }})：{{ formatRate(node.rate) }}</title></circle><text x="764" :y="node.labelY - 5" class="node-name">{{ shortName(node.name) }}<title>{{ node.name }} · {{ node.id }}</title></text><text x="764" :y="node.labelY + 10" class="node-rate importer-color">{{ formatRate(node.rate) }}</text></g>
          <text x="500" :y="height - 16" text-anchor="middle" class="axis-label">线宽线性映射：0 — {{ formatRate(maxLink) }}；非零最细 0.75 px，零速率显示虚线</text>
        </svg>
      </div>
      <p v-if="selectedEdge" class="selection" role="status">{{ selectedEdge.label }} <button class="clear-selection" type="button" @click="selected = ''">取消选择</button></p>
      <details class="details"><summary>连线明细（{{ filtered.length }} 对）</summary><div class="table-scroll"><table><thead><tr><th>Exporter</th><th>Importer</th><th>双向带宽</th><th>连接数</th></tr></thead><tbody><tr v-for="link in filtered" :key="linkKey(link)"><td>{{ link.exporter.name }}<small>{{ link.exporter.id }}</small></td><td>{{ link.importer.name }}<small>{{ link.importer.id }}</small></td><td>{{ formatRate(link.bytes_per_second) }}</td><td>{{ link.connections }}</td></tr></tbody></table></div></details>
    </template>
  </article>
</template>

<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'
import type { TrafficLink } from '../../types/traffic'
import { formatRate, positionNodes, trafficNodes, trafficScale } from '../../utils/traffic'

const props = defineProps<{ links: readonly TrafficLink[]; stale: boolean }>()
const exporter = shallowRef('')
const selected = shallowRef('')
const limit = 16
const palette = ['#67d6bf', '#91b8ff', '#e8bf82', '#d7a7ef', '#7dd5eb', '#ef9f9f']
const allExporters = computed(() => trafficNodes(props.links, 'exporter'))
const allImporters = computed(() => trafficNodes(props.links, 'importer'))
const totalRate = computed(() => props.links.reduce((sum, link) => sum + link.bytes_per_second, 0))
const connections = computed(() => props.links.reduce((sum, link) => sum + link.connections, 0))
const filtered = computed(() => props.links.filter(link => !exporter.value || link.exporter.id === exporter.value).sort((a, b) => b.bytes_per_second - a.bytes_per_second || linkKey(a).localeCompare(linkKey(b))))
const displayed = computed(() => filtered.value.slice(0, limit))
const visibleExporters = computed(() => { const ids = new Set(displayed.value.map(link => link.exporter.id)); return allExporters.value.filter(node => ids.has(node.id)) })
const visibleImporters = computed(() => { const ids = new Set(displayed.value.map(link => link.importer.id)); return allImporters.value.filter(node => ids.has(node.id)) })
const height = computed(() => Math.max(440, Math.max(visibleExporters.value.length, visibleImporters.value.length) * 32 + 140))
const leftScale = computed(() => trafficScale(Math.max(0, ...visibleExporters.value.map(node => node.rate))))
const rightScale = computed(() => trafficScale(Math.max(0, ...visibleImporters.value.map(node => node.rate))))
const leftNodes = computed(() => positionNodes(visibleExporters.value, leftScale.value.max, height.value, true))
const rightNodes = computed(() => positionNodes(visibleImporters.value, rightScale.value.max, height.value, false))
const maxLink = computed(() => Math.max(1, ...displayed.value.map(link => link.bytes_per_second)))
const edges = computed(() => {
  const left = new Map(leftNodes.value.map(node => [node.id, node]))
  const right = new Map(rightNodes.value.map(node => [node.id, node]))
  return displayed.value.map(link => {
    const a = left.get(link.exporter.id)!, b = right.get(link.importer.id)!
    let hash = 0
    for (const char of a.id) hash = (hash * 31 + char.charCodeAt(0)) >>> 0
    return { ...link, key: linkKey(link), path: `M ${a.x} ${a.y} C 450 ${a.y}, 550 ${b.y}, ${b.x} ${b.y}`, width: link.bytes_per_second ? Math.max(.75, 18 * link.bytes_per_second / maxLink.value) : 1, color: palette[hash % palette.length], label: `${a.name} (${a.id}) ↔ ${b.name} (${b.id})：${formatRate(link.bytes_per_second)}，${link.connections} 个连接` }
  })
})
const selectedEdge = computed(() => edges.value.find(edge => edge.key === selected.value))
watch(allExporters, nodes => { if (exporter.value && !nodes.some(node => node.id === exporter.value)) exporter.value = '' })
watch(edges, value => { if (selected.value && !value.some(edge => edge.key === selected.value)) selected.value = '' })
function linkKey(link: TrafficLink) { return JSON.stringify([link.exporter.id, link.importer.id]) }
function toggle(key: string) { selected.value = selected.value === key ? '' : key }
function axisY(value: number, max: number) { return height.value - 62 - value / max * (height.value - 128) }
function shortName(value: string) { const chars = Array.from(value); return chars.length > 12 ? `${chars.slice(0, 11).join('')}…` : value }
</script>

<style scoped>
.heading { display: flex; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }.heading h2 { font-size: 1.15rem; margin: 0; }.heading p { font-size: .8rem; line-height: 1.6; }.live-total { display: grid; gap: .3rem; text-align: right; }.live-total strong { font-size: 1.5rem; color: var(--accent); }.live-total span { font-size: .75rem; }.toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 1rem; font-size: .78rem; margin: 1rem 0; }.toolbar label { display: flex; align-items: center; gap: .7rem; }.toolbar select { max-width: min(22rem, 60vw); }.limit-note { color: #edc186; font-size: .8rem; line-height: 1.6; }.chart-scroll { overflow-x: auto; }.chart { display: block; width: 100%; min-width: 850px; }.faded { opacity: .45; }.axis { stroke: var(--border); }.tick,.axis-label { fill: var(--muted); font-size: 10px; }.side-title { font-size: 13px; letter-spacing: 2px; font-weight: 700; }.exporter-color { fill: var(--accent); }.importer-color { fill: #91b8ff; }.edge { opacity: .55; pointer-events: none; }.edge-hit { cursor: pointer; outline: none; }.edge-hit:focus-visible { stroke: var(--text); stroke-opacity: .3; }.dimmed { opacity: .12; }.leader { fill: none; stroke: var(--muted); stroke-opacity: .3; }.exporter-dot { fill: var(--accent); }.importer-dot { fill: #91b8ff; }.node-name { fill: var(--text); font-size: 12px; }.node-rate { font-size: 10px; }.selection { padding: .8rem; border-radius: 6px; background: var(--panel-soft); font-size: .8rem; overflow-wrap: anywhere; }.clear-selection { border: 0; background: none; color: var(--accent); cursor: pointer; }.details { margin-top: 1rem; font-size: .8rem; }.details summary { cursor: pointer; color: var(--muted); }.table-scroll { overflow: auto; max-height: 28rem; margin-top: 1rem; }.table-scroll table { width: 100%; border-collapse: collapse; }.table-scroll th,.table-scroll td { padding: .7rem; text-align: left; border-bottom: 1px solid var(--border); }.table-scroll small { display: block; color: var(--muted); margin-top: .3rem; overflow-wrap: anywhere; }
</style>
