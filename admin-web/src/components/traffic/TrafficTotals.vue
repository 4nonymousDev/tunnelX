<template>
  <article class="panel totals" :style="{ '--bar-color': color }">
    <header class="heading"><div><p class="eyebrow">{{ title }}</p><h3>{{ formatBytes(total) }}</h3></div><span class="muted">{{ items.length }} 台设备</span></header>
    <label class="search-label">筛选设备<input v-model="query" type="search" placeholder="设备名称或 ID" /></label>
    <div ref="viewport" class="bar-viewport">
    <p v-if="!filtered.length" class="empty">{{ items.length ? '没有匹配的设备' : '尚无累计流量' }}</p>
    <ol v-else class="bars" :aria-label="`${title} 累计流量排名`">
      <li v-for="item in visible" :key="item.id" class="bar-row">
        <div class="bar-label"><span class="name" :title="`${item.name} · ${item.id}`">{{ item.name }}</span><strong>{{ formatBytes(item.bytes) }}</strong></div>
        <div class="bar-track"><div class="bar" :style="{ width: `${item.bytes / maximum * 100}%` }"></div></div>
        <span class="device-id">{{ item.id }}</span>
      </li>
    </ol>
    </div>
    <footer class="pagination"><button class="button secondary" :disabled="page === 0" @click="page--">上一页</button><span>{{ page + 1 }} / {{ pages }}</span><button class="button secondary" :disabled="page + 1 >= pages" @click="page++">下一页</button></footer>
    <p class="scale-note muted">横轴：累计字节数 · 0 — {{ formatBytes(maximum) }} · 每页 {{ pageSize }} 台</p>
  </article>
</template>

<script setup lang="ts">
import { computed, shallowRef, useTemplateRef, watch } from 'vue'
import { useElementSize } from '../../composables/useElementSize'
import type { TrafficTotal } from '../../types/traffic'
import { formatBytes } from '../../utils/traffic'

const props = defineProps<{ title: string; items: readonly TrafficTotal[]; color: string }>()
const query = shallowRef('')
const page = shallowRef(0)
const viewport = useTemplateRef<HTMLDivElement>('viewport')
const { height } = useElementSize(viewport)
const pageSize = computed(() => Math.max(1, Math.floor(height.value / 64)))
const total = computed(() => props.items.reduce((sum, item) => sum + item.bytes, 0))
const filtered = computed(() => {
  const q = query.value.trim().toLocaleLowerCase()
  return [...props.items].filter(item => `${item.name} ${item.id}`.toLocaleLowerCase().includes(q)).sort((a, b) => b.bytes - a.bytes || a.id.localeCompare(b.id))
})
const maximum = computed(() => filtered.value.reduce((max, item) => Math.max(max, item.bytes), 1))
const pages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize.value)))
const visible = computed(() => filtered.value.slice(page.value * pageSize.value, (page.value + 1) * pageSize.value))
watch(query, () => { page.value = 0 })
watch(pages, value => { page.value = Math.min(page.value, value - 1) })
</script>

<style scoped>
.totals { min-width: 0; }.heading { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }.heading h3 { font-size: 1.7rem; margin: .3rem 0 1rem; }.heading span { font-size: .8rem; }.search-label { display: grid; gap: .5rem; font-size: .8rem; color: var(--muted); }.bars { list-style: none; padding: 0; margin: 1.2rem 0; display: grid; gap: 1rem; }.bar-label { display: flex; justify-content: space-between; gap: 1rem; font-size: .85rem; }.name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.bar-label strong { white-space: nowrap; font-variant-numeric: tabular-nums; }.bar-track { height: .6rem; background: var(--panel-soft); border-radius: 3px; margin: .45rem 0 .25rem; }.bar { height: 100%; background: var(--bar-color); border-radius: inherit; transition: width .3s; }.device-id { display: block; color: var(--muted); font-size: .68rem; overflow-wrap: anywhere; }.scale-note { font-size: .72rem; }.pagination { display: flex; align-items: center; justify-content: space-between; gap: .6rem; font-size: .8rem; }
.totals { height: 100%; min-height: 0; display: flex; flex-direction: column; gap: .65rem; overflow: hidden; }
.heading,.search-label,.pagination,.scale-note { flex: none; }
.heading h3 { margin: .3rem 0 0; font-size: 1.5rem; }
.search-label { display: flex; align-items: center; gap: .75rem; white-space: nowrap; }
.search-label input { min-width: 0; font-size: .8rem; }
.bar-viewport { flex: 1; min-height: 0; overflow: hidden; }
.bars { display: block; margin: 0; }
.bar-row { height: 64px; padding: 6px 0; }
.bar-label { font-size: .8rem; line-height: 1.2rem; }
.device-id { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; line-height: 1rem; }
.scale-note { margin: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.pagination .button { padding: .4rem .65rem; }
.empty { padding: 1rem; }
@media (max-width: 600px), (max-height: 760px) { .totals { padding: .75rem; gap: .5rem; }.heading h3 { font-size: 1.25rem; }.search-label input { padding: .4rem .6rem; } }
@media (prefers-reduced-motion: reduce) { .bar { transition: none; } }
</style>
