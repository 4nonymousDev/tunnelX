<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="emit('close')">
    <form class="dialog" @submit.prevent="submit">
      <div class="dialog-heading">
        <div><p class="eyebrow">SSH KEY</p><h2 class="dialog-title">生成专用 SSH Key</h2></div>
        <button class="dialog-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </div>
      <p class="hint">用户名、邮箱和本机计算机名会以明文写入 .pub 注释，供服务端导入时识别；它们不参与认证。</p>
      <label class="field"><span>私钥路径</span><input :value="keyPath" disabled /></label>
      <label class="field"><span>用户名</span><input v-model.trim="username" maxlength="128" required autocomplete="name" /></label>
      <label class="field"><span>邮箱</span><input v-model.trim="email" maxlength="254" required type="email" autocomplete="email" /></label>
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
import { shallowRef, watch } from 'vue'
import type { KeyGenerationRequestDTO, KeyGenerationResultDTO } from '@shared/dto'

const props = defineProps<{ open: boolean; keyPath: string; busy: boolean; result?: KeyGenerationResultDTO }>()
const emit = defineEmits<{ close: []; submit: [request: KeyGenerationRequestDTO]; copy: [text: string] }>()
const username = shallowRef('')
const email = shallowRef('')

watch(() => props.open, (open) => {
  if (open && !props.result) { username.value = ''; email.value = '' }
})

function submit(): void {
  emit('submit', { key_path: props.keyPath, username: username.value, email: email.value })
}
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 70; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2,5,13,.78); backdrop-filter: blur(8px); }
.dialog { width: min(560px, 100%); padding: 24px; border: 1px solid var(--line-strong); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px rgba(0,0,0,.5); }
.dialog-heading { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; }.dialog-title { margin: 0; }.eyebrow { margin: 0 0 5px; color: var(--accent-light); font-size: 10px; font-weight: 800; letter-spacing: .14em; }.dialog-close { border: 0; color: var(--muted); font-size: 26px; background: transparent; cursor: pointer; }
.hint { color: var(--muted); font-size: 12px; line-height: 1.6; }.field { display: grid; gap: 7px; margin-top: 14px; color: #c3cad8; font-size: 12px; }.result { display: grid; gap: 8px; margin-top: 18px; padding: 14px; border: 1px solid var(--line-strong); border-radius: 12px; background: #0b1220; font-size: 12px; }.result code { overflow-wrap: anywhere; color: var(--accent-light); }.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 22px; }
</style>
