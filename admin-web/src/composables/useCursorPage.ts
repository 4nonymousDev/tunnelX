import { computed, onScopeDispose, readonly, shallowRef } from 'vue'
import type { PageDto } from '../types/admin'

export function useCursorPage<T>(fetchPage: (cursor?: string) => Promise<PageDto<T>>) {
  const page = shallowRef<PageDto<T>>({ items: [], next_cursor: null })
  const cursor = shallowRef<string | undefined>()
  const history = shallowRef<(string | undefined)[]>([])
  const loading = shallowRef(false)
  const error = shallowRef('')
  let generation = 0
  let failedRequest: { cursor?: string; history: (string | undefined)[] } | null = null

  async function load(nextCursor: string | undefined, nextHistory: (string | undefined)[]) {
    const requestId = ++generation
    loading.value = true
    error.value = ''
    try {
      const result = await fetchPage(nextCursor)
      if (requestId !== generation) return
      page.value = result
      cursor.value = nextCursor
      history.value = nextHistory
      failedRequest = null
    } catch (cause) {
      if (requestId !== generation) return
      error.value = cause instanceof Error ? cause.message : '加载失败，请重试'
      failedRequest = { cursor: nextCursor, history: nextHistory }
    } finally {
      if (requestId === generation) loading.value = false
    }
  }

  function reset() {
    page.value = { items: [], next_cursor: null }
    cursor.value = undefined
    history.value = []
    failedRequest = null
    return load(undefined, [])
  }
  function next() {
    if (loading.value || !page.value.next_cursor) return
    return load(page.value.next_cursor, [...history.value, cursor.value])
  }
  function previous() {
    if (loading.value || history.value.length === 0) return
    const earlier = [...history.value]
    return load(earlier.pop(), earlier)
  }
  function refresh() {
    if (loading.value) return
    return load(cursor.value, [...history.value])
  }
  function retry() {
    if (loading.value) return
    return failedRequest ? load(failedRequest.cursor, failedRequest.history) : refresh()
  }

  onScopeDispose(() => { generation++ })
  return {
    page: readonly(page), loading: readonly(loading), error: readonly(error),
    pageNumber: computed(() => history.value.length + 1),
    hasPrevious: computed(() => history.value.length > 0),
    hasNext: computed(() => Boolean(page.value.next_cursor)),
    reset, next, previous, refresh, retry,
  }
}
