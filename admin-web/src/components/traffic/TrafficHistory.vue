<template>
  <div class="history">
    <TrafficTimeFilter :sampled-at="snapshot.sampled_at" @change="range = $event" />
    <div class="history-note"><span>{{ range ? '所选时段累计' : `自 ${formatDate(snapshot.since)} 起累计` }} · 双向有效载荷，两侧请勿相加</span><span v-if="snapshot.persistent && snapshot.persistence_healthy" :title="`每 5 秒保存，正常关闭时补齐；最近保存：${formatDate(snapshot.persisted_at)}`">已启用持久化 · 重启继续累计</span><span v-else class="warning" role="status">{{ snapshot.persistent ? '保存失败，正在重试；最新流量尚未持久化' : '未配置数据目录，仅在当前运行期间累计' }}</span></div>
    <div class="role-tabs" role="group" aria-label="历史流量设备角色"><button type="button" :aria-pressed="mobileRole === 'exporter'" @click="mobileRole = 'exporter'">Exporter</button><button type="button" :aria-pressed="mobileRole === 'importer'" @click="mobileRole = 'importer'">Importer</button></div>
    <p class="query-status" :class="{ warning: error }" role="status" :title="statusText">{{ statusText }}<button v-if="error" type="button" class="link-button" @click="refresh">重试</button></p>
    <div class="total-charts" :aria-busy="loading"><p v-if="range && !data" class="history-placeholder">{{ error ? '无法加载所选时段的数据' : '正在查询历史流量…' }}</p><TrafficTotals v-show="!range || data" class="role-chart" :class="{ 'mobile-hidden': mobileRole !== 'exporter' }" title="Exporter" :items="range ? data?.exporters ?? [] : snapshot.exporters" color="var(--accent)" /><TrafficTotals v-show="!range || data" class="role-chart" :class="{ 'mobile-hidden': mobileRole !== 'importer' }" title="Importer" :items="range ? data?.importers ?? [] : snapshot.importers" color="#91b8ff" /></div>
  </div>
</template>

<script setup lang="ts">
import { computed, shallowRef } from 'vue'
import type { TrafficRange, TrafficSnapshot } from '../../types/traffic'
import type { AdminApi } from '../../composables/useAdminApi'
import { useTrafficHistory } from '../../composables/useTrafficHistory'
import { formatDate } from '../../utils/format'
import TrafficTotals from './TrafficTotals.vue'
import TrafficTimeFilter from './TrafficTimeFilter.vue'
const props = defineProps<{ snapshot: TrafficSnapshot; api: AdminApi; active: boolean }>()
const mobileRole = shallowRef<'exporter' | 'importer'>('exporter')
const range = shallowRef<TrafficRange | null>(null)
const { data, loading, error, refresh } = useTrafficHistory(props.api, range, () => props.active, () => props.snapshot.sampled_at)
const statusText = computed(() => error.value ? `${range.value?.label} · 查询失败，${data.value ? '显示上次结果，' : ''}将自动重试：${error.value}` : range.value ? `${range.value.label} · 本地时区 · ${data.value ? `数据保存至 ${formatDate(data.value.persisted_at)}` : '查询中'} · 旧审计按连接结束时间归档` : '全部历史 · 日期筛选按本地时区，包含结束日期全天')
</script>

<style scoped>
.history { height: 100%; min-height: 0; display: flex; flex-direction: column; gap: .6rem; }
.history-note { display: flex; justify-content: space-between; flex-wrap: wrap; gap: .4rem 1rem; font-size: .73rem; line-height: 1.5; color: var(--muted); flex: none; }
.warning { color: #f4bf78; }
.query-status { margin: 0; height: 1.1rem; flex: none; color: var(--muted); font-size: .7rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.query-status.warning { color: #f4bf78; }
.query-status button { margin-left: .5rem; }
.history-placeholder { grid-column: 1 / -1; align-self: center; text-align: center; color: var(--muted); font-size: .85rem; }
.total-charts { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; flex: 1; min-height: 0; }
.role-tabs { display: none; }
@media (max-width: 900px) { .total-charts { grid-template-columns: minmax(0, 1fr); }.mobile-hidden { display: none !important; }.role-tabs { display: flex; gap: .5rem; flex: none; }.role-tabs button { border: 1px solid var(--border); border-radius: 6px; padding: .4rem .8rem; color: var(--muted); background: var(--panel-soft); cursor: pointer; }.role-tabs button[aria-pressed="true"] { color: var(--text); border-color: var(--accent); } }
@media (max-height: 700px) { .history { gap: .4rem; }.history-note > span:first-child { display: none; } }
</style>
