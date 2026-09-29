<template>
  <article class="panel history-panel" :aria-labelledby="titleId">
    <h3 :id="titleId">{{ title }}</h3>
    <div class="history-body" :aria-busy="pager.loading.value" aria-live="polite">
      <div v-if="pager.error.value" class="history-state" role="alert">
        <p>{{ pager.error.value }}</p>
        <button class="button secondary" type="button" @click="pager.retry">重试</button>
      </div>
      <div v-else-if="pager.loading.value" class="history-state">正在加载…</div>
      <ol v-else-if="pager.page.value.items.length" class="history-list" :start="(pager.pageNumber.value - 1) * CLIENT_HISTORY_PAGE_SIZE + 1" :style="{ gridTemplateRows: 'repeat(' + CLIENT_HISTORY_PAGE_SIZE + ', minmax(0, 1fr))' }">
        <li v-for="item in pager.page.value.items" :key="item.id">
          <strong :title="item.heading">{{ item.heading }}</strong>
          <span>{{ formatDate(item.time) }}</span>
          <span v-if="item.reason" :title="item.reason">{{ item.reason }}</span>
        </li>
      </ol>
      <div v-else class="history-state">{{ props.kind === 'access' ? '暂无访问记录' : '暂无管理操作' }}</div>
    </div>
    <nav class="history-pagination" :aria-label="title + '分页'">
      <button class="button secondary" type="button" :disabled="pager.loading.value || !pager.hasPrevious.value" @click="pager.previous">上一页</button>
      <span>第 {{ pager.pageNumber.value }} 页</span>
      <button class="button secondary" type="button" :disabled="pager.loading.value || !pager.hasNext.value" @click="pager.next">下一页</button>
    </nav>
  </article>
</template>

<script setup lang="ts">
import { computed, useId, watch } from 'vue'
import { CLIENT_HISTORY_PAGE_SIZE, type AdminApi } from '../composables/useAdminApi'
import { useCursorPage } from '../composables/useCursorPage'
import { formatDate } from '../utils/format'

const props = defineProps<{ api: AdminApi; fingerprint: string; kind: 'access' | 'actions' }>()
const titleId = useId()
const title = computed(() => props.kind === 'access' ? '访问记录' : '管理操作时间线')
interface HistoryEntry { id: string | number; heading: string; time: string | number; reason?: string }
const pager = useCursorPage<HistoryEntry>(async (cursor) => {
  if (props.kind === 'access') {
    const page = await props.api.loadClientAccessPage(props.fingerprint, cursor)
    return { ...page, items: page.items.map(item => ({ id: item.id, heading: item.kind + ' · ' + item.result, time: item.time, reason: item.reason })) }
  }
  const page = await props.api.loadClientActionsPage(props.fingerprint, cursor)
  return { ...page, items: page.items.map(item => ({ id: item.id, heading: item.action + ' · ' + item.result, time: item.created_at, reason: item.reason })) }
})

watch(() => [props.fingerprint, props.kind], () => { void pager.reset() }, { immediate: true })
// Refresh the current page after a management action without changing its cursor.
watch(() => props.api.clientDetail.value, (detail) => {
  if (detail?.client.fingerprint === props.fingerprint) void pager.refresh()
})
</script>

<style scoped>
.history-panel { height: 30rem; min-width: 0; display: grid; grid-template-rows: auto minmax(0, 1fr) auto; gap: 1rem; }
.history-panel h3 { margin: 0; }
.history-body { min-height: 0; min-width: 0; }
.history-list { display: grid; height: 100%; margin: 0; padding: 0 0 0 1.4rem; }
.history-list li { min-height: 0; min-width: 0; padding: .2rem 0 .2rem .25rem; border-bottom: 1px solid var(--border); }
.history-list li:last-child { border-bottom: 0; }
.history-list strong, .history-list span { display: block; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.history-list strong { font-size: .88rem; line-height: 1.35; }
.history-list span { margin-top: .15rem; color: var(--muted); font-size: .78rem; line-height: 1.25; }
.history-state { height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: .75rem; color: var(--muted); text-align: center; overflow-wrap: anywhere; }
.history-state p { margin: 0; }
.history-pagination { display: flex; justify-content: space-between; align-items: center; gap: .5rem; padding-top: .85rem; border-top: 1px solid var(--border); }
.history-pagination span { color: var(--muted); font-size: .8rem; white-space: nowrap; }
.history-pagination .button { padding: .5rem .65rem; font-size: .8rem; }
.history-pagination .button:disabled { cursor: not-allowed; }
</style>
