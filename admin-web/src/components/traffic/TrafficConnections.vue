<template>
  <button type="button" class="details-button" @click="open">连线明细（{{ links.length }} 对）</button>
  <dialog ref="dialog" class="connections-dialog" aria-label="实时连线明细" @click="onBackdrop">
    <div class="dialog-content">
      <header><h2>连线明细</h2><button class="button secondary" type="button" autofocus @click="dialog?.close()">关闭</button></header>
      <div ref="viewport" class="table-viewport"><table><thead><tr><th>Exporter</th><th>Importer</th><th>双向带宽</th><th>连接数</th></tr></thead><tbody><tr v-for="link in visible" :key="trafficLinkKey(link)"><td :title="`${link.exporter.name} · ${link.exporter.id}`">{{ link.exporter.name }}<small>{{ link.exporter.id }}</small></td><td :title="`${link.importer.name} · ${link.importer.id}`">{{ link.importer.name }}<small>{{ link.importer.id }}</small></td><td>{{ formatRate(link.bytes_per_second) }}</td><td>{{ link.connections }}</td></tr></tbody></table><p v-if="!links.length" class="empty">暂无符合筛选条件的连接</p></div>
      <footer><button class="button secondary" type="button" :disabled="page === 0" @click="page--">上一页</button><span>{{ page + 1 }} / {{ pages }}</span><button class="button secondary" type="button" :disabled="page + 1 >= pages" @click="page++">下一页</button></footer>
    </div>
  </dialog>
</template>

<script setup lang="ts">
import { computed, shallowRef, useTemplateRef, watch } from 'vue'
import { useElementSize } from '../../composables/useElementSize'
import type { TrafficLink } from '../../types/traffic'
import { formatRate, trafficLinkKey } from '../../utils/traffic'
const props = defineProps<{ links: readonly TrafficLink[] }>()
const dialog = useTemplateRef<HTMLDialogElement>('dialog')
const viewport = useTemplateRef<HTMLDivElement>('viewport')
const { height } = useElementSize(viewport)
const page = shallowRef(0)
const pageSize = computed(() => Math.max(1, Math.floor((height.value - 40) / 64)))
const pages = computed(() => Math.max(1, Math.ceil(props.links.length / pageSize.value)))
const visible = computed(() => props.links.slice(page.value * pageSize.value, (page.value + 1) * pageSize.value))
watch(pages, value => { page.value = Math.min(page.value, value - 1) })
function open() { page.value = 0; dialog.value?.showModal() }
function onBackdrop(event: MouseEvent) {
  if (event.target !== dialog.value || !dialog.value) return
  const rect = dialog.value.getBoundingClientRect()
  if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) dialog.value.close()
}
</script>

<style scoped>
.details-button { padding: .4rem 0; border: 0; background: none; color: var(--accent); font-size: .75rem; cursor: pointer; }
.connections-dialog { width: min(850px, calc(100vw - 24px)); height: min(620px, calc(100dvh - 24px)); max-height: none; padding: 1rem; border: 1px solid var(--border); border-radius: 16px; background: var(--panel); color: var(--text); overflow: hidden; }
.connections-dialog::backdrop { background: #0009; backdrop-filter: blur(4px); }
.dialog-content { height: 100%; display: flex; flex-direction: column; gap: .75rem; }
header,footer { display: flex; align-items: center; justify-content: space-between; gap: .5rem; flex: none; }
h2 { margin: 0; font-size: 1rem; } footer { font-size: .8rem; }
.table-viewport { flex: 1; min-height: 0; overflow: hidden; }
table { width: 100%; border-collapse: collapse; table-layout: fixed; font-size: .8rem; }
th { height: 40px; color: var(--muted); text-align: left; }
td { height: 64px; border-bottom: 1px solid var(--border); }
td,th { padding: .5rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
th:nth-child(3) { width: 24%; } th:nth-child(4) { width: 15%; }
small { display: block; margin-top: .3rem; color: var(--muted); overflow: hidden; text-overflow: ellipsis; }
@media (max-width: 600px) { table { font-size: .68rem; } td,th { padding: .25rem; } }
</style>
