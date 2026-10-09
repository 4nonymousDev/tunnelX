<template>
  <section class="traffic-page">
    <header class="traffic-header"><div><p class="eyebrow">传输观察</p><h1>流量监控</h1></div><button class="button secondary" type="button" :disabled="loading" @click="refresh">刷新</button></header>
    <p v-if="error" class="error-banner" role="alert">{{ error }}，将自动重试。</p>
    <div class="tab-bar" role="tablist" aria-label="流量统计">
      <button v-for="tab in tabs" :id="`${id}-${tab.id}-tab`" :key="tab.id" type="button" role="tab" :aria-selected="active === tab.id" :aria-controls="`${id}-${tab.id}-panel`" :tabindex="active === tab.id ? 0 : -1" :class="{ active: active === tab.id }" @click="active = tab.id" @keydown="switchTab($event)">{{ tab.label }}</button>
    </div>
    <template v-if="snapshot">
      <div class="traffic-status"><span :class="{ stale }">{{ stale ? '数据已过期 · 等待更新' : '实时采样 · 每 2 秒刷新' }}</span><span>采样时间 {{ formatDate(snapshot.sampled_at) }}</span></div>
      <section v-show="active === 'live'" :id="`${id}-live-panel`" class="tab-panel" role="tabpanel" :aria-labelledby="`${id}-live-tab`" tabindex="0"><TrafficRealtime :links="snapshot.links" :stale="stale" /></section>
      <section v-show="active === 'history'" :id="`${id}-history-panel`" class="tab-panel" role="tabpanel" :aria-labelledby="`${id}-history-tab`" tabindex="0"><TrafficHistory :snapshot="snapshot" :api="api" :active="active === 'history'" /></section>
    </template>
    <p v-else class="empty" role="status">{{ loading ? '正在加载流量统计…' : '暂无流量数据' }}</p>
  </section>
</template>

<script setup lang="ts">
import { nextTick, shallowRef, useId } from 'vue'
import type { AdminApi } from '../composables/useAdminApi'
import { useTraffic } from '../composables/useTraffic'
import { formatDate } from '../utils/format'
import TrafficRealtime from '../components/traffic/TrafficRealtime.vue'
import TrafficHistory from '../components/traffic/TrafficHistory.vue'

const props = defineProps<{ api: AdminApi }>()
const { snapshot, error, loading, stale, refresh } = useTraffic(props.api.fetchTraffic)
const id = useId()
const tabs = [{ id: 'live', label: '实时流量桑基图' }, { id: 'history', label: '历史流量累计' }] as const
const active = shallowRef<'live' | 'history'>('live')
function switchTab(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  active.value = event.key === 'Home' ? 'live' : event.key === 'End' ? 'history' : active.value === 'live' ? 'history' : 'live'
  void nextTick(() => document.getElementById(`${id}-${active.value}-tab`)?.focus())
}
</script>

<style scoped>
.traffic-page { height: 100%; min-height: 0; display: flex; flex-direction: column; gap: .65rem; }
.traffic-header { display: flex; align-items: center; justify-content: space-between; flex: none; }
.traffic-header h1 { margin: .15rem 0; font-size: 1.65rem; }
.tab-bar { display: flex; align-self: flex-start; gap: .25rem; padding: .25rem; border: 1px solid var(--border); border-radius: 12px; background: var(--panel-soft); flex: none; }
.tab-bar button { padding: .65rem 1rem; border: 0; border-radius: 8px; color: var(--muted); background: transparent; cursor: pointer; font-size: .85rem; }
.tab-bar button.active { background: #20443f; color: var(--accent); box-shadow: 0 2px 8px #0002; }
.tab-bar button:focus-visible,.tab-panel:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.traffic-status { display: flex; justify-content: space-between; gap: .5rem; color: var(--muted); font-size: .72rem; flex: none; }
.stale { color: #f4bf78; }
.tab-panel { flex: 1; min-height: 0; min-width: 0; }
.error-banner { margin: 0; flex: none; }
@media (max-width: 600px), (max-height: 760px) { .traffic-header .eyebrow { display: none; }.traffic-header h1 { font-size: 1.3rem; }.traffic-page { gap: .45rem; }.tab-bar button { padding: .5rem .75rem; } }
@media (max-width: 480px) { .traffic-status > span:last-child { display: none; } }
</style>
