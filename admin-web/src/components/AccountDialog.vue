<template>
  <div class="backdrop" @click.self="cancel">
    <form class="dialog" role="dialog" aria-modal="true" aria-labelledby="account-dialog-title" @submit.prevent="submit">
      <h2 id="account-dialog-title">{{ title }}</h2>
      <p class="description">{{ description }}</p>
      <label v-if="mode === 'create'" class="field">用户名<input v-model.trim="username" maxlength="64" autocomplete="off" autocapitalize="none" spellcheck="false" required :disabled="busy" /><small>3–64 位小写字母、数字或 . _ -，以字母或数字开头。</small></label>
      <p v-else class="account-name">账号：<strong>{{ account?.username }}</strong></p>
      <label v-if="mode === 'create' || mode === 'reset'" class="field">{{ mode === 'create' ? '初始密码' : '新密码' }}<input v-model="password" type="password" autocomplete="new-password" required :disabled="busy" /><small>15–128 个字符，空格也是密码的一部分。提交后输入框立即清空。</small></label>
      <label v-if="mode === 'create'" class="field">账号角色<select v-model="isAdmin" :disabled="busy"><option :value="false">普通用户</option><option :value="true">管理员</option></select><small>{{ isAdmin ? '管理员可以登录此后台，管理所有账号、设备、隧道和审计记录。' : '普通用户仅可登录客户端，不能进入管理后台。' }}</small></label>
      <label v-if="mode === 'create'" class="field">设备上限<input v-model.number="maxDevices" type="number" min="1" max="50" step="1" required :disabled="busy" /><small>默认 10 台；设为 1 即只允许一台设备。</small></label>
      <label class="field">操作原因<textarea v-model.trim="reason" maxlength="500" rows="3" required placeholder="填写供审计核对的原因" :disabled="busy" /></label>
      <p v-if="validationError || error" class="error-banner" role="alert">{{ validationError || error }}</p>
      <div class="actions"><button class="button secondary" type="button" :disabled="busy" @click="cancel">取消</button><button class="button" :class="mode === 'create' ? 'primary' : 'danger'" type="submit" :disabled="busy">{{ busy ? '处理中…' : title }}</button></div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import type { AccountDto, CreateAccountRequestDto, UpdateAccountRequestDto } from '../types/admin'

const props = defineProps<{ mode: 'create' | 'reset' | 'toggle' | 'role'; account?: AccountDto; busy: boolean; error?: string }>()
const emit = defineEmits<{ cancel: []; create: [request: CreateAccountRequestDto]; update: [username: string, request: UpdateAccountRequestDto] }>()
const username = ref('')
const password = ref('')
const maxDevices = ref(10)
const isAdmin = ref(false)
const reason = ref('')
const validationError = ref('')
const title = computed(() => props.mode === 'create' ? '创建账号' : props.mode === 'reset' ? '重置密码' : props.mode === 'role' ? props.account?.is_admin ? '取消管理员权限' : '授予管理员权限' : props.account?.enabled ? '禁用账号' : '启用账号')
const description = computed(() => props.mode === 'create'
  ? '将账号和密码通过可信渠道交给用户。管理员角色具有整个服务端的管理权限，请仅授予可信人员。'
  : props.mode === 'role'
    ? `${props.account?.is_admin ? '取消后，该账号将无法登录管理后台。' : '授予后，该账号可以管理所有账号、设备、隧道和审计记录。'}此操作会撤销该账号当前的后台登录，并使设备需要重新登录。至少保留一名启用的管理员。`
    : '此操作会关闭该账号的设备连接，并撤销后台登录。账号启用后，需要使用当前密码重新登录。至少保留一名启用的管理员。')
onUnmounted(() => { password.value = '' })

function cancel(): void { if (!props.busy) { password.value = ''; emit('cancel') } }
function submit(): void {
  if (props.busy) return
  validationError.value = ''
  if (props.mode === 'create' && !/^[a-z0-9][a-z0-9._-]{2,63}$/.test(username.value)) { validationError.value = '用户名格式无效'; return }
  if ((props.mode === 'create' || props.mode === 'reset') && (password.value.length > 256 || [...password.value].length < 15 || [...password.value].length > 128 || new TextEncoder().encode(password.value).length > 512)) { validationError.value = '密码需要 15–128 个字符，最多 512 字节'; return }
  if (props.mode === 'create' && (!Number.isInteger(maxDevices.value) || maxDevices.value < 1 || maxDevices.value > 50)) { validationError.value = '设备上限需要是 1–50 的整数'; return }
  if (!reason.value.trim()) { validationError.value = '操作原因不能为空'; return }
  const secret = password.value
  password.value = ''
  if (props.mode === 'create') {
    emit('create', { username: username.value, password: secret, max_devices: maxDevices.value, is_admin: isAdmin.value, reason: reason.value.trim() })
  } else if (props.account) {
    emit('update', props.account.username, {
      expected_generation: props.account.generation, reason: reason.value.trim(),
      ...(props.mode === 'reset' ? { password: secret } : props.mode === 'role' ? { is_admin: !props.account.is_admin } : { enabled: !props.account.enabled }),
    })
  }
}
</script>

<style scoped>
.field select { width: 100%; padding: .65rem .75rem; border: 1px solid var(--border); border-radius: 8px; background: #081518; color: var(--text); font: inherit; }.field select:focus { outline-color: var(--accent); }
.backdrop { position: fixed; inset: 0; z-index: 30; display: grid; place-items: center; padding: 1rem; background: #02090dcc; }.dialog { width: min(34rem,100%); max-height: 92vh; overflow-y: auto; padding: 1.5rem; border: 1px solid var(--border); border-radius: 1rem; background: var(--panel); }.dialog h2 { margin: 0; }.description { color: var(--muted); font-size: .88rem; line-height: 1.6; }.field { display: grid; gap: .45rem; margin: 1rem 0; font-size: .85rem; }.field small { color: var(--muted); line-height: 1.5; }.actions { display: flex; justify-content: flex-end; gap: .7rem; margin-top: 1.3rem; }.account-name { padding: .7rem; border-radius: 8px; background: var(--panel-soft); }
</style>
