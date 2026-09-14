<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="emit('close')">
    <form class="dialog" novalidate @submit.prevent="submit">
      <div class="dialog-heading">
        <div><p class="eyebrow">SSH KEY</p><h2 class="dialog-title">生成专用 SSH Key</h2></div>
        <button class="dialog-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </div>
      <p class="hint">用户名、邮箱和本机计算机名会以明文写入 .pub 注释，供服务端导入时识别；它们不参与认证。</p>
      <div class="field">
        <label for="key-generation-path">私钥路径</label>
        <div class="key-path-row">
          <input
            id="key-generation-path"
            v-model="keyPath"
            class="input"
            placeholder="D:\keys\tunnel_key"
            :aria-invalid="Boolean(errors.keyPath)"
            :aria-describedby="errors.keyPath ? 'key-path-error' : undefined"
            @input="clearError('keyPath')"
          />
          <button class="button button-secondary" type="button" :disabled="busy" @click="emit('browse', keyPath)">选择…</button>
        </div>
        <span v-if="errors.keyPath" id="key-path-error" class="field-error" role="alert">{{ errors.keyPath }}</span>
      </div>
      <div class="field">
        <label for="key-generation-username">用户名</label>
        <input
          id="key-generation-username"
          v-model="username"
          class="input"
          maxlength="128"
          autocomplete="name"
          :aria-invalid="Boolean(errors.username)"
          :aria-describedby="errors.username ? 'username-error' : undefined"
          @input="clearError('username')"
        />
        <span v-if="errors.username" id="username-error" class="field-error" role="alert">{{ errors.username }}</span>
      </div>
      <div class="field">
        <label for="key-generation-email">邮箱</label>
        <input
          id="key-generation-email"
          v-model="email"
          class="input"
          maxlength="254"
          type="email"
          autocomplete="email"
          :aria-invalid="Boolean(errors.email)"
          :aria-describedby="errors.email ? 'email-error' : undefined"
          @input="clearError('email')"
        />
        <span v-if="errors.email" id="email-error" class="field-error" role="alert">{{ errors.email }}</span>
      </div>
      <div v-if="result" class="result">
        <strong>密钥生成成功</strong>
        <span>计算机名：{{ result.metadata.computer_name }}</span>
        <span>公钥：{{ result.pub_path }}</span>
        <code>{{ result.fingerprint }}</code>
        <button class="button button-secondary" type="button" @click="emit('copy', result.public_key)">复制公钥</button>
      </div>
      <div class="dialog-actions">
        <button class="button button-secondary" type="button" @click="emit('close')">关闭</button>
        <button v-if="!result" class="button button-primary" type="submit" :disabled="busy">{{ busy ? '生成中…' : '生成' }}</button>
      </div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { reactive, shallowRef, watch } from 'vue'
import type { KeyGenerationRequestDTO, KeyGenerationResultDTO } from '@shared/dto'

const props = defineProps<{ open: boolean; keyPath: string; busy: boolean; result?: KeyGenerationResultDTO }>()
const emit = defineEmits<{
  close: []
  browse: [keyPath: string]
  submit: [request: KeyGenerationRequestDTO]
  copy: [text: string]
}>()
const keyPath = shallowRef('')
const username = shallowRef('')
const email = shallowRef('')
const errors = reactive({ keyPath: '', username: '', email: '' })

watch(
  () => [props.open, props.keyPath] as const,
  ([open, nextKeyPath], previous) => {
    if (!open) return
    keyPath.value = nextKeyPath
    if (!previous?.[0]) {
      username.value = ''
      email.value = ''
      clearErrors()
    }
  },
  { immediate: true },
)

function submit(): void {
  const normalized = {
    key_path: keyPath.value.trim(),
    username: username.value.trim(),
    email: email.value.trim(),
  }
  errors.keyPath = normalized.key_path ? '' : '请选择或输入私钥路径'
  errors.username = normalized.username ? '' : '请输入用户名'
  errors.email = normalized.email ? '' : '请输入邮箱'
  if (normalized.email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(normalized.email)) {
    errors.email = '请输入有效的邮箱地址'
  }
  if (errors.keyPath || errors.username || errors.email) return
  emit('submit', normalized)
}

function clearError(field: keyof typeof errors): void {
  errors[field] = ''
}

function clearErrors(): void {
  errors.keyPath = ''
  errors.username = ''
  errors.email = ''
}
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 70; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2,5,13,.78); backdrop-filter: blur(8px); }
.dialog { width: min(560px, 100%); padding: 24px; border: 1px solid var(--line-strong); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px rgba(0,0,0,.5); }
.dialog-heading { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }.dialog-title { margin: 0; }.eyebrow { margin: 0 0 5px; color: var(--accent-light); font-size: 10px; font-weight: 800; letter-spacing: .14em; }.dialog-close { border: 0; color: var(--muted); font-size: 26px; background: transparent; cursor: pointer; }
.hint { color: var(--muted); font-size: 12px; line-height: 1.6; }.field { display: grid; gap: 7px; margin-top: 14px; color: #c3cad8; font-size: 12px; }.key-path-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }.field-error { color: #ff9aa4; font-size: 11px; }.result { display: grid; gap: 8px; margin-top: 18px; padding: 14px; border: 1px solid var(--line-strong); border-radius: 12px; background: #0b1220; font-size: 12px; }.result code { overflow-wrap: anywhere; color: var(--accent-light); }.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 22px; }
</style>
