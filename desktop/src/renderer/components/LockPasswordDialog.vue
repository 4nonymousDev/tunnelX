<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="emit('close')">
    <form class="dialog" role="dialog" aria-modal="true" aria-labelledby="lock-dialog-title" @submit.prevent="submit">
      <div class="dialog-heading">
        <div>
          <p class="eyebrow">INTERFACE LOCK</p>
          <h2 id="lock-dialog-title" class="dialog-title">锁定 TunnelX</h2>
        </div>
        <button class="dialog-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </div>
      <p class="dialog-copy">设置本次锁定使用的密码。程序重启后仍会保持锁定，密码不会明文保存。</p>
      <div class="form-grid">
        <label class="field">
          <span class="field-label">解锁密码</span>
          <input ref="passwordInput" v-model="password" class="input" type="password" minlength="4" maxlength="128" autocomplete="new-password" required />
        </label>
        <label class="field">
          <span class="field-label">确认密码</span>
          <input v-model="confirmation" class="input" type="password" minlength="4" maxlength="128" autocomplete="new-password" required />
        </label>
      </div>
      <p v-if="displayError" class="error-message" role="alert">{{ displayError }}</p>
      <div class="dialog-actions">
        <button class="button button-secondary" type="button" :disabled="busy" @click="emit('close')">取消</button>
        <button class="button button-primary" type="submit" :disabled="busy">{{ busy ? '正在锁定…' : '锁定界面' }}</button>
      </div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, shallowRef, useTemplateRef, watch } from 'vue'

const props = defineProps<{ open: boolean; busy: boolean; error?: string }>()
const emit = defineEmits<{ close: []; submit: [password: string] }>()

const password = shallowRef('')
const confirmation = shallowRef('')
const localError = shallowRef('')
const passwordInput = useTemplateRef<HTMLInputElement>('passwordInput')
const displayError = computed(() => localError.value || props.error)

watch(
  () => props.open,
  async (open) => {
    if (!open) return
    password.value = ''
    confirmation.value = ''
    localError.value = ''
    await nextTick()
    passwordInput.value?.focus()
  },
)

function submit(): void {
  localError.value = ''
  if (password.value !== confirmation.value) {
    localError.value = '两次输入的密码不一致'
    return
  }
  emit('submit', password.value)
}
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 80; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2, 5, 13, .78); backdrop-filter: blur(9px); }
.dialog { width: min(460px, 100%); padding: 24px; border: 1px solid var(--line-strong); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px rgba(0,0,0,.55); }
.dialog-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; }
.eyebrow { margin: 0 0 5px; color: var(--accent-light); font-size: 10px; font-weight: 800; letter-spacing: .14em; }
.dialog-title { margin: 0; font-size: 20px; }
.dialog-close { border: 0; color: var(--muted); font-size: 26px; line-height: 1; background: transparent; cursor: pointer; }
.dialog-copy { margin: 14px 0 20px; color: var(--muted); font-size: 12px; line-height: 1.65; }
.form-grid { display: grid; gap: 15px; }
.field { display: grid; gap: 7px; }
.field-label { color: #c3cad8; font-size: 12px; }
.error-message { margin: 14px 0 0; color: #ffb0b8; font-size: 12px; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 24px; }
</style>
