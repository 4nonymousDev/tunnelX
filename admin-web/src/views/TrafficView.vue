<template>
  <section class="page">
    <header class="page-header">
      <div><p class="eyebrow">传输观察</p><h1 class="page-title">流量监控</h1></div>
      <button class="button secondary" type="button" :disabled="loading" @click="refresh">刷新</button>
    </header>
    <p v-if="error" class="error-banner" role="alert">{{ error }}，将自动重试。</p>
    <template v-if="snapshot">
      <div class="traffic-status"><span :class="{ stale }">{{ stale ? '数据已过期 · 等待更新' : '实时采样 · 每 2 秒刷新' }}</span><span>采样时间 {{ formatDate(snapshot.sampled_at) }}</span></div>
      <TrafficRealtime :links="snapshot.links" :stale="stale" />
      <header class="totals-heading"><h2>本次运行累计流量</h2><p class="muted">自 server 启动（{{ formatDate(snapshot.since) }}）以来的双向有效载荷，包含正在传输的连接，重启后清零。两侧统计同一批数据，请勿相加。</p></header>
      <div class="total-charts"><TrafficTotals title="Exporter" :items="snapshot.exporters" color="var(--accent)" /><TrafficTotals title="Importer" :items="snapshot.importers" color="#91b8ff" /></div>
    </template>
    <p v-else class="empty" role="status">{{ loading ? '正在加载流量统计…' : '暂无流量数据' }}</p>
  </section>
</template>

<script setup lang="ts">
import type { AdminApi } from '../composables/useAdminApi'
import { useTraffic } from '../composables/useTraffic'
import { formatDate } from '../utils/format'
import TrafficRealtime from '../components/traffic/TrafficRealtime.vue'
import TrafficTotals from '../components/traffic/TrafficTotals.vue'

const props = defineProps<{ api: AdminApi }>()
const { snapshot, error, loading, stale, refresh } = useTraffic(props.api.fetchTraffic)
</script>

<style scoped>
.traffic-status { display: flex; justify-content: space-between; gap: 1rem; flex-wrap: wrap; color: var(--muted); font-size: .8rem; margin-bottom: 1rem; }
.stale { color: #f4bf78; }.totals-heading { margin: 2rem 0 1rem; }.totals-heading h2 { font-size: 1.25rem; }.totals-heading p { font-size: .8rem; line-height: 1.7; }.total-charts { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
@media (max-width: 1000px) { .total-charts { grid-template-columns: 1fr; } }
</style>
