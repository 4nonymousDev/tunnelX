<template>
  <div class="backdrop" role="presentation" @click.self="emit('cancel')">
    <section class="dialog" role="dialog" aria-modal="true" :aria-labelledby="titleId">
      <h2 :id="titleId" class="dialog-title">{{ props.title }}</h2>
      <p v-if="props.description" class="dialog-description">{{ props.description }}</p>
      <label class="field-label" for="action-reason">操作原因</label>
      <textarea id="action-reason" ref="reasonInput" v-model.trim="reason" class="reason-input" maxlength="500" rows="4" placeholder="请输入可审计的操作原因" />
      <p v-if="validationError" class="validation-error">{{ validationError }}</p>
      <div class="dialog-actions">
        <button class="button secondary" type="button" @click="emit('cancel')">取消</button>
        <button class="button danger" type="button" :disabled="props.busy" @click="submit">{{ props.confirmLabel ?? '确认' }}</button>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onMounted, shallowRef, useId, useTemplateRef } from 'vue'

const props = defineProps<{ title: string; description?: string; confirmLabel?: string; busy?: boolean }>()
const emit = defineEmits<{ confirm: [reason: string]; cancel: [] }>()
const reason = shallowRef('')
const validationError = shallowRef('')
const reasonInput = useTemplateRef<HTMLTextAreaElement>('reasonInput')
const titleId = useId()

onMounted(() => nextTick(() => reasonInput.value?.focus()))

function submit() {
  const value = reason.value.trim()
  if (!value) {
    validationError.value = '操作原因不能为空'
    return
  }
  emit('confirm', value)
}
</script>

<style scoped>
.backdrop { position: fixed; inset: 0; z-index: 20; display: grid; place-items: center; padding: 1rem; background: rgb(0 0 0 / 65%); }
.dialog { width: min(30rem, 100%); padding: 1.5rem; border: 1px solid var(--border); border-radius: 14px; background: var(--panel); box-shadow: 0 24px 80px rgb(0 0 0 / 45%); }
.dialog-title { margin: 0; font-size: 1.15rem; }
.dialog-description { color: var(--muted); }
.field-label { display: block; margin: 1rem 0 .4rem; font-size: .85rem; font-weight: 700; }
.reason-input { width: 100%; resize: vertical; }
.validation-error { margin: .4rem 0 0; color: var(--danger); font-size: .85rem; }
.dialog-actions { display: flex; justify-content: flex-end; gap: .65rem; margin-top: 1.25rem; }
</style>
