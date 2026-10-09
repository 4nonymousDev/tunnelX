<template>
  <article class="panel totals" :style="{ '--bar-color': color }">
    <header class="heading"><div><p class="eyebrow">{{ title }}</p><h3>{{ formatBytes(total) }}</h3></div><span class="muted">{{ items.length }} 台设备</span></header>
    <label class="search-label">筛选设备<input v-model="query" type="search" placeholder="设备名称或 ID" /></label>
    <p v-if="!filtered.length" class="empty">{{ items.length ? '没有匹配的设备' : '尚无累计流量' }}</p>
    <ol v-else class="bars" :aria-label="`${title} 累计流量排名`">
      <li v-for="item in visible" :key="item.id" class="bar-row">
        <div class="bar-label"><span class="name" :title="`${item.name} · ${item.id}`">{{ item.name }}</span><strong>{{ formatBytes(item.bytes) }}</strong></div>
        <div class="bar-track"><div class="bar" :style="{ width: `${item.bytes / maximum * 100}%` }"></div></div>
        <span class="device-id">{{ item.id }}</span>
      </li>
    </ol>
    <footer v-if="filtered.length > pageSize" class="pagination"><button class="button secondary" :disabled="page === 0" @click="page--">上一页</button><span>{{ page + 1 }} / {{ pages }}</span><button class="button secondary" :disabled="page + 1 >= pages" @click="page++">下一页</button></footer>
    <p class="scale-note muted">横轴：累计字节数 · 0 — {{ formatBytes(maximum) }} · 每页 {{ pageSize }} 台</p>
  </article>
</template>

<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'
import type { TrafficTotal } from '../../types/traffic'
import { formatBytes } from '../../utils/traffic'

const props = defineProps<{ title: string; items: readonly TrafficTotal[]; color: string }>()
const query = shallowRef('')
const page = shallowRef(0)
const pageSize = 10
const total = computed(() => props.items.reduce((sum, item) => sum + item.bytes, 0))
const filtered = computed(() => {
  const q = query.value.trim().toLocaleLowerCase()
  return [...props.items].filter(item => `${item.name} ${item.id}`.toLocaleLowerCase().includes(q)).sort((a, b) => b.bytes - a.bytes || a.id.localeCompare(b.id))
})
const maximum = computed(() => Math.max(1, ...filtered.value.map(item => item.bytes)))
const pages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize)))
const visible = computed(() => filtered.value.slice(page.value * pageSize, (page.value + 1) * pageSize))
watch(query, () => { page.value = 0 })
watch(pages, value => { page.value = Math.min(page.value, value - 1) })
</script>

<style scoped>
.totals { min-width: 0; }.heading { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }.heading h3 { font-size: 1.7rem; margin: .3rem 0 1rem; }.heading span { font-size: .8rem; }.search-label { display: grid; gap: .5rem; font-size: .8rem; color: var(--muted); }.bars { list-style: none; padding: 0; margin: 1.2rem 0; display: grid; gap: 1rem; }.bar-label { display: flex; justify-content: space-between; gap: 1rem; font-size: .85rem; }.name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.bar-label strong { white-space: nowrap; font-variant-numeric: tabular-nums; }.bar-track { height: .6rem; background: var(--panel-soft); border-radius: 3px; margin: .45rem 0 .25rem; }.bar { height: 100%; background: var(--bar-color); border-radius: inherit; transition: width .3s; }.device-id { display: block; color: var(--muted); font-size: .68rem; overflow-wrap: anywhere; }.scale-note { font-size: .72rem; }.pagination { display: flex; align-items: center; justify-content: space-between; gap: .6rem; font-size: .8rem; }
@media (prefers-reduced-motion: reduce) { .bar { transition: none; } }
</style>
