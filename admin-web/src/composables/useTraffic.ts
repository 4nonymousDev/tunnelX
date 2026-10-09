import { computed, onMounted, onUnmounted, shallowRef } from 'vue'
import type { TrafficSnapshot } from '../types/traffic'

export function useTraffic(fetchSnapshot: () => Promise<TrafficSnapshot>) {
  const snapshot = shallowRef<TrafficSnapshot | null>(null)
  const error = shallowRef('')
  const loading = shallowRef(false)
  const now = shallowRef(Date.now())
  let disposed = false
  let timer: number | undefined
  const stale = computed(() => !!error.value || (snapshot.value !== null && now.value - new Date(snapshot.value.sampled_at).getTime() > 5000))

  async function refresh() {
    if (disposed || loading.value) return
    if (timer !== undefined) window.clearTimeout(timer)
    now.value = Date.now()
    loading.value = true
    try {
      if (!document.hidden) {
        const value = await fetchSnapshot()
        if (disposed) return
        snapshot.value = value
        error.value = ''
      }
    } catch (cause) {
      if (!disposed) error.value = cause instanceof Error ? cause.message : '无法加载流量统计'
    } finally {
      loading.value = false
      now.value = Date.now()
      if (!disposed) timer = window.setTimeout(() => { void refresh() }, 2000)
    }
  }
  function visible() { if (!document.hidden) void refresh() }
  onMounted(() => { document.addEventListener('visibilitychange', visible); void refresh() })
  onUnmounted(() => {
    disposed = true
    if (timer !== undefined) window.clearTimeout(timer)
    document.removeEventListener('visibilitychange', visible)
  })
  return { snapshot, error, loading, stale, refresh }
}
