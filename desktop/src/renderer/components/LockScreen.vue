<template>
  <main class="lock-screen">
    <form class="unlock-card" aria-labelledby="unlock-title" @submit.prevent="emit('unlock', password)">
      <div class="lock-mark" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none"><path d="M7.5 10V7.5a4.5 4.5 0 0 1 9 0V10M6 10h12v10H6V10Z" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </div>
      <p class="eyebrow">TUNNELX LOCKED</p>
      <h1 id="unlock-title" class="title">界面已锁定</h1>
      <p class="copy">输入密码以返回隧道工作区。</p>
      <label class="field">
        <span class="field-label">密码</span>
        <input ref="passwordInput" v-model="password" class="input" type="password" maxlength="128" autocomplete="current-password" required />
      </label>
      <p v-if="error" class="error-message" role="alert">{{ error }}</p>
      <button class="button button-primary unlock-button" type="submit" :disabled="busy">{{ busy ? '正在验证…' : '解锁' }}</button>
    </form>
  </main>
</template>

<script setup lang="ts">
import { onMounted, shallowRef, useTemplateRef, watch } from 'vue'

const props = defineProps<{ busy: boolean; error?: string }>()
const emit = defineEmits<{ unlock: [password: string] }>()

const password = shallowRef('')
const passwordInput = useTemplateRef<HTMLInputElement>('passwordInput')

onMounted(() => passwordInput.value?.focus())
watch(() => props.error, error => {
  if (error) password.value = ''
})
</script>

<style scoped>
.lock-screen { display: grid; width: 100%; height: 100%; place-items: center; padding: 64px 24px 24px; user-select: none; -webkit-app-region: drag; }
.unlock-card { width: min(390px, 100%); padding: 34px; border: 1px solid var(--line-strong); border-radius: 24px; background: rgba(17, 24, 42, .94); box-shadow: 0 30px 100px rgba(0,0,0,.46); -webkit-app-region: no-drag; }
.lock-mark { display: grid; width: 52px; height: 52px; place-items: center; margin-bottom: 21px; border-radius: 16px; color: white; background: linear-gradient(145deg, #6f8cff, #4a62dc); box-shadow: 0 10px 34px rgba(91,124,250,.3); }
.lock-mark svg { width: 25px; height: 25px; }
.eyebrow { margin: 0 0 7px; color: var(--accent-light); font-size: 10px; font-weight: 800; letter-spacing: .15em; }
.title { margin: 0; font-size: 24px; letter-spacing: -.03em; }
.copy { margin: 9px 0 24px; color: var(--muted); font-size: 12px; }
.field { display: grid; gap: 8px; }
.field-label { color: #c3cad8; font-size: 12px; }
.error-message { margin: 12px 0 0; color: #ffb0b8; font-size: 12px; }
.unlock-button { width: 100%; margin-top: 18px; }
</style>
