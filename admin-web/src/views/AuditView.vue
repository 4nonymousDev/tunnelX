<template>
  <section class="page">
    <header class="page-header"><div><p class="eyebrow">可追溯事件</p><h1 class="page-title">审计日志</h1></div><button class="button secondary" type="button" @click="props.api.exportAudit(apiQuery())">导出 CSV</button></header>
    <form class="filters panel" @submit.prevent="search">
      <label>起始时间<input v-model="filters.from" type="datetime-local" /></label><label>结束时间<input v-model="filters.to" type="datetime-local" /></label>
      <label>客户端 ID<input v-model.trim="filters.client_id" placeholder="client_id" /></label><label>指纹<input v-model.trim="filters.fingerprint" placeholder="SHA256:…" /></label>
      <label>IP（当前页）<input v-model.trim="filters.ip" placeholder="127.0.0.1" /></label><label>事件（当前页）<input v-model.trim="filters.kind" placeholder="connection / access" /></label>
      <label>结果<input v-model.trim="filters.result" placeholder="success / rejected" /></label>
      <button class="button primary filter-submit" type="submit">查询</button>
    </form>
    <div class="table-wrap">
      <table class="data-table"><thead><tr><th>时间</th><th>客户端</th><th>指纹 / IP</th><th>事件</th><th>结果</th><th>原因</th></tr></thead>
        <tbody><tr v-for="event in visibleEvents" :key="event.id"><td>{{ formatDate(event.time) }}</td><td>{{ event.client_id || '—' }}</td><td><code :title="event.fingerprint">{{ event.fingerprint ? shortFingerprint(event.fingerprint) : '—' }}</code><small>{{ event.remote_ip || '—' }}</small></td><td>{{ event.kind }}</td><td><span class="status" :class="event.result === 'success' ? 'online' : 'closing'">{{ event.result }}</span></td><td>{{ event.reason || '—' }}</td></tr></tbody>
      </table><div v-if="visibleEvents.length === 0" class="empty">没有符合条件的审计事件</div>
    </div>
    <footer class="pagination"><button class="button secondary" type="button" :disabled="cursorHistory.length === 0" @click="previous">上一页</button><button class="button secondary" type="button" :disabled="!props.api.auditPage.value.next_cursor" @click="next">下一页</button></footer>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, shallowRef } from 'vue'
import type { AdminApi } from '../composables/useAdminApi'
import type { AuditQuery } from '../types/admin'
import { formatDate, shortFingerprint } from '../utils/format'

const props = defineProps<{ api: AdminApi }>()
const filters = reactive({ from: '', to: '', client_id: '', fingerprint: '', ip: '', kind: '', result: '', limit: 50 })
const cursorHistory = shallowRef<string[]>([])
const currentCursor = shallowRef('')
const visibleEvents = computed(() => props.api.auditPage.value.items.filter(item => (!filters.ip || item.remote_ip?.includes(filters.ip)) && (!filters.kind || item.kind.includes(filters.kind))))
function rfc3339(value: string): string | undefined { return value ? new Date(value).toISOString() : undefined }
function apiQuery(cursor?: string): AuditQuery { return { from: rfc3339(filters.from), to: rfc3339(filters.to), client_id: filters.client_id || undefined, fingerprint: filters.fingerprint || undefined, result: filters.result || undefined, limit: filters.limit, cursor } }
async function search() { cursorHistory.value = []; currentCursor.value = ''; await props.api.loadAudit(apiQuery()) }
async function next() { const cursor = props.api.auditPage.value.next_cursor; if (!cursor) return; cursorHistory.value = [...cursorHistory.value, currentCursor.value]; currentCursor.value = cursor; await props.api.loadAudit(apiQuery(cursor)) }
async function previous() { const history = [...cursorHistory.value]; const cursor = history.pop() ?? ''; cursorHistory.value = history; currentCursor.value = cursor; await props.api.loadAudit(apiQuery(cursor || undefined)) }
</script>

<style scoped>
.filters { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); align-items: end; gap: .8rem; margin-bottom: 1rem; }
.filters label { display: grid; gap: .35rem; color: var(--muted); font-size: .78rem; }
.filter-submit { height: 2.45rem; }
.pagination { display: flex; justify-content: flex-end; gap: .65rem; margin-top: 1rem; }
</style>
