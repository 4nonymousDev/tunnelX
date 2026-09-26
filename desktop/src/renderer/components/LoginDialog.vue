<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="close">
    <form class="dialog" aria-labelledby="account-login-title" @submit.prevent="submit">
      <div class="dialog-heading">
        <div><p class="eyebrow">ACCOUNT</p><h2 id="account-login-title">账号登录</h2></div>
        <button class="dialog-close" type="button" :disabled="busy" aria-label="关闭" @click="close">×</button>
      </div>
      <p class="copy">使用管理员分配的账号。登录后自动登记此设备并连接隧道，已有密钥、设备 ID 和隧道配置会继续保留。</p>
      <div class="fields">
        <label>服务器地址<input v-model.trim="serverAddr" class="input" autocomplete="off" placeholder="example.com:2222" required :disabled="busy" /></label>
        <label>设备名称<input v-model.trim="deviceName" class="input" autocomplete="off" placeholder="此设备的显示名称" :disabled="busy" /></label>
        <label>用户名<input v-model.trim="username" class="input" autocomplete="username" autocapitalize="none" spellcheck="false" maxlength="64" required :disabled="busy" /></label>
        <label>密码<input v-model="password" class="input" type="password" autocomplete="current-password" required :disabled="busy" /></label>
      </div>
      <p class="copy small">首次连接会单独询问服务器指纹，请与管理员核对。密码仅用于本次登录，不保存在应用配置中。</p>
      <p v-if="validationError || error" class="form-error" role="alert">{{ validationError || error }}</p>
      <p v-if="busy" class="copy" role="status">正在登录… 如出现服务器指纹或密钥权限提示，请完成确认。</p>
      <div class="dialog-actions">
        <button class="button button-secondary" type="button" :disabled="busy" @click="close">取消</button>
        <button class="button button-primary" type="submit" :disabled="busy || !snapshot">{{ busy ? '登录中…' : '登录并连接' }}</button>
      </div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import type { LoginRequestDTO, SettingsDTO, SnapshotDTO } from '@shared/dto'

const props = defineProps<{ open: boolean; snapshot?: SnapshotDTO; busy: boolean; error?: string }>()
const emit = defineEmits<{ close: []; submit: [request: LoginRequestDTO, settings: SettingsDTO] }>()
const serverAddr = ref('')
const deviceName = ref('')
const username = ref('')
const password = ref('')
const validationError = ref('')

watch(() => props.open, open => {
  password.value = ''
  validationError.value = ''
  if (open) {
    serverAddr.value = props.snapshot?.server_addr ?? ''
    deviceName.value = props.snapshot?.name ?? ''
  }
})
onUnmounted(() => { password.value = '' })

function close(): void {
  if (props.busy) return
  password.value = ''
  emit('close')
}
function submit(): void {
  if (props.busy || !props.snapshot) return
  validationError.value = ''
  if (!/^[a-z0-9][a-z0-9._-]{2,63}$/.test(username.value)) {
    validationError.value = '用户名需为 3–64 位小写字母、数字或 . _ -，并以字母或数字开头'
    return
  }
  if (!password.value || password.value.length > 256 || [...password.value].length > 128 || new TextEncoder().encode(password.value).length > 512) {
    validationError.value = '密码不能为空，且不能超过 128 个字符或 512 字节'
    return
  }
  const request = { username: username.value, password: password.value }
  password.value = ''
  // The existing key path is deliberately copied rather than replaced by a UI default.
  emit('submit', request, { name: deviceName.value, server_addr: serverAddr.value, key_path: props.snapshot.key_path })
}
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 50; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2,5,13,.75); backdrop-filter: blur(8px); }
.dialog { width: min(500px,100%); max-height: 90vh; overflow-y: auto; padding: 26px; border: 1px solid var(--line-strong); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px #0008; }
.dialog-heading { display: flex; align-items: flex-start; justify-content: space-between; }.dialog-heading h2 { margin: 0; font-size: 22px; }.eyebrow { color: var(--accent-light); font-size: 10px; letter-spacing: .14em; margin: 0 0 6px; }.dialog-close { border: 0; background: none; color: var(--muted); font-size: 26px; cursor: pointer; }
.copy { color: var(--muted); font-size: 12px; line-height: 1.65; }.small { font-size: 11px; }.fields { display: grid; gap: 14px; margin-top: 20px; }.fields label { display: grid; gap: 7px; font-size: 12px; color: #c3cad8; }.form-error { color: #ffb0b8; font-size: 12px; line-height: 1.6; }.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 22px; }
</style>
