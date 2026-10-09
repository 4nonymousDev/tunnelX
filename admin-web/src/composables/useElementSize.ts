import { onScopeDispose, shallowRef, watch } from 'vue'
import type { Ref } from 'vue'

// Keep the last visible dimensions while a tab is hidden.
export function useElementSize(target: Readonly<Ref<HTMLElement | null>>) {
  const width = shallowRef(0)
  const height = shallowRef(0)
  const observer = new ResizeObserver(entries => {
    const rect = entries[0]?.contentRect
    if (rect && rect.width > 0 && rect.height > 0) {
      width.value = rect.width
      height.value = rect.height
    }
  })
  watch(target, element => {
    observer.disconnect()
    if (element) observer.observe(element)
  }, { flush: 'post', immediate: true })
  onScopeDispose(() => observer.disconnect())
  return { width, height }
}
