import { onScopeDispose, shallowRef, watch } from 'vue'
import type { Ref } from 'vue'
import type { AdminApi } from './useAdminApi'
import type { TrafficHistory, TrafficRange } from '../types/traffic'

export function useTrafficHistory(api: AdminApi, range: Ref<TrafficRange | null>, active: () => boolean, sampledAt: () => string) {
  const data = shallowRef<TrafficHistory | null>(null)
  const loading = shallowRef(false)
  const error = shallowRef('')
  let controller: AbortController | null = null
  let lastRequest = 0
  async function refresh() {
    if (!range.value || !active() || loading.value || document.hidden) return
    const request = new AbortController()
    controller = request
    loading.value = true
    lastRequest = Date.now()
    try {
      const value = await api.fetchTrafficHistory(range.value.from, range.value.to, request.signal)
      if (!request.signal.aborted) { data.value = value; error.value = '' }
    } catch (cause) {
      if (!request.signal.aborted) error.value = cause instanceof Error ? cause.message : '历史流量查询失败'
    } finally {
      if (controller === request) { loading.value = false; controller = null }
    }
  }
  watch([range, active], ([next], [previous]) => {
    controller?.abort()
    controller = null
    loading.value = false
    if (next !== previous) { data.value = null; error.value = '' }
    void refresh()
  })
  watch(sampledAt, () => { if (Date.now() - lastRequest >= 5000) void refresh() })
  onScopeDispose(() => controller?.abort())
  return { data, loading, error, refresh }
}
