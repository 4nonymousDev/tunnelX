<template>
  <section class="page">
    <header class="page-header">
      <div>
        <p class="eyebrow">实时态势</p>
        <h1 class="page-title">概览</h1>
      </div>
      <button class="button secondary" type="button" :disabled="props.api.loading.value" @click="props.api.refreshAll">刷新</button>
    </header>
    <div v-if="props.api.overview.value" class="metric-grid">
      <article v-for="metric in metrics" :key="metric.label" class="metric-card">
        <span class="metric-label">{{ metric.label }}</span>
        <strong class="metric-value">{{ metric.value }}</strong>
      </article>
    </div>
    <div v-else class="empty">暂无概览数据</div>
    <article v-if="props.api.overview.value" class="panel context-panel">
      <div><span class="muted">统计时区</span><strong>{{ props.api.overview.value.timezone }}</strong></div>
      <div><span class="muted">今日区间起点</span><strong>{{ formatDate(props.api.overview.value.period_start) }}</strong></div>
    </article>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { AdminApi } from '../composables/useAdminApi'
import { formatDate } from '../utils/format'

const props = defineProps<{ api: AdminApi }>()
const metrics = computed(() => {
  const item = props.api.overview.value
  if (!item) return []
  return [
    { label: '在线用户', value: item.online_users },
    { label: 'Importer', value: item.importers },
    { label: '活跃 Exporter', value: item.active_exporters },
    { label: '活动隧道', value: item.active_tunnels },
    { label: '今日拒绝', value: item.rejected_today },
  ]
})
</script>

<style scoped>
.metric-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); gap: 1rem; }
.metric-card { min-height: 8rem; padding: 1.25rem; border: 1px solid var(--border); border-radius: 14px; background: linear-gradient(145deg, var(--panel), var(--panel-soft)); }
.metric-label { display: block; color: var(--muted); font-size: .84rem; }
.metric-value { display: block; margin-top: .9rem; font-size: 2.2rem; color: var(--accent); }
.context-panel { display: flex; flex-wrap: wrap; gap: 3rem; margin-top: 1rem; }
.context-panel div { display: grid; gap: .3rem; }
</style>
