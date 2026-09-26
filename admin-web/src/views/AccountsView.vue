<template>
  <section class="page">
    <header class="page-header"><div><p class="eyebrow">用户接入</p><h1 class="page-title">账号管理</h1></div><div class="actions"><button class="button secondary" type="button" :disabled="refreshing" @click="refresh">刷新</button><button class="button primary" type="button" @click="openDialog('create')">创建账号</button></div></header>
    <p class="intro">普通用户使用账号登录客户端；管理员还可以登录此后台，管理整个服务端。角色变更、重置密码或禁用账号会撤销该账号的现有登录。</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="pageError" class="error-banner" role="alert">{{ pageError }}</p>
    <div class="table-wrap"><table class="data-table"><thead><tr><th>用户名</th><th>角色</th><th>状态</th><th>设备上限</th><th>最后变更</th><th>操作</th></tr></thead><tbody>
      <tr v-for="account in api.accounts.value" :key="account.username"><td><strong>{{ account.username }}</strong><small v-if="account.username === api.username.value">（当前账号）</small></td><td>{{ account.is_admin ? '管理员' : '普通用户' }}</td><td><span class="status" :class="account.enabled ? 'online' : 'closing'">{{ account.enabled ? '启用' : '禁用' }}</span></td><td>{{ account.max_devices }}</td><td>{{ formatDate(account.updated_at) }}</td><td><div class="row-actions"><button class="link-button" type="button" @click="viewDevices(account.username)">查看设备</button><button class="link-button" type="button" @click="openDialog('reset', account)">重置密码</button><button class="link-button" type="button" @click="openDialog('role', account)">{{ account.is_admin ? '取消管理员' : '设为管理员' }}</button><button class="link-button" :class="{ 'danger-text': account.enabled }" type="button" @click="openDialog('toggle', account)">{{ account.enabled ? '禁用' : '启用' }}</button></div></td></tr>
    </tbody></table><div v-if="!api.accounts.value.length" class="empty">还没有账号。创建后将账号和密码通过可信渠道交给用户。</div></div>

    <section v-if="deviceUsername" class="devices panel">
      <div class="device-heading"><h3>{{ deviceUsername }} 的设备</h3><div class="actions"><button class="link-button" type="button" :disabled="devicesLoading" @click="viewDevices(deviceUsername)">刷新设备</button><button class="link-button" type="button" @click="closeDevices">收起</button></div></div>
      <p v-if="devicesLoading" class="muted" role="status">读取设备中…</p>
      <p v-else-if="deviceError" class="error-banner" role="alert">{{ deviceError }}</p>
      <template v-else><p class="muted">“已登记”表示设备凭据有效，当前连接状态请查看在线会话。</p><div class="table-wrap"><table class="data-table"><thead><tr><th>设备</th><th>指纹</th><th>登记状态</th><th>最后变更</th></tr></thead><tbody><tr v-for="device in devices" :key="device.client_id"><td><strong>{{ device.name || '未命名设备' }}</strong><code>{{ device.client_id }}</code></td><td><code>{{ device.fingerprint }}</code></td><td>{{ device.active ? '已登记' : '不可用' }}</td><td>{{ formatDate(device.updated_at) }}</td></tr></tbody></table><div v-if="!devices.length" class="empty">此账号还没有登记设备</div></div></template>
    </section>
    <AccountDialog v-if="dialogMode" :mode="dialogMode" :account="selected" :busy="submitting" :error="dialogError" @cancel="dialogMode = undefined" @create="create" @update="update" />
  </section>
</template>

<script setup lang="ts">
import { onUnmounted, ref, shallowRef } from 'vue'
import type { AdminApi } from '../composables/useAdminApi'
import type { AccountDto, AccountDeviceDto, CreateAccountRequestDto, UpdateAccountRequestDto } from '../types/admin'
import { formatDate } from '../utils/format'
import AccountDialog from '../components/AccountDialog.vue'

const props = defineProps<{ api: AdminApi }>()
const dialogMode = ref<'create' | 'reset' | 'toggle' | 'role'>()
const selected = shallowRef<AccountDto>()
const submitting = ref(false)
const dialogError = ref('')
const pageError = ref('')
const notice = ref('')
const refreshing = ref(false)
const deviceUsername = ref('')
const devices = shallowRef<AccountDeviceDto[]>([])
const devicesLoading = ref(false)
const deviceError = ref('')
let deviceRequest = 0

function openDialog(mode: 'create' | 'reset' | 'toggle' | 'role', account?: AccountDto): void {
  dialogError.value = ''; notice.value = ''; selected.value = account; dialogMode.value = mode
}
async function refresh(): Promise<void> {
  refreshing.value = true; pageError.value = ''
  try { await props.api.loadAccounts() } catch (error) { pageError.value = message(error) }
  finally { refreshing.value = false }
}
async function create(request: CreateAccountRequestDto): Promise<void> {
  submitting.value = true; dialogError.value = ''
  try {
    await props.api.createAccount(request)
    notice.value = `账号 ${request.username} 已创建。请通过可信渠道将登录信息交给用户。`
    dialogMode.value = undefined
  } catch (error) { dialogError.value = message(error) }
  finally { request.password = ''; submitting.value = false }
}
async function update(username: string, request: UpdateAccountRequestDto): Promise<void> {
  submitting.value = true; dialogError.value = ''
  try {
    const account = await props.api.updateAccount(username, request)
    notice.value = account.enabled ? `账号 ${username} 已更新；该账号需要重新登录。` : `账号 ${username} 已禁用，现有登录已撤销。`
    dialogMode.value = undefined
    if (deviceUsername.value === username) await viewDevices(username)
  } catch (error) { dialogError.value = message(error) }
  finally { if (request.password !== undefined) request.password = ''; submitting.value = false }
}
async function viewDevices(username: string): Promise<void> {
  const current = ++deviceRequest
  deviceUsername.value = username; devices.value = []; deviceError.value = ''; devicesLoading.value = true
  try {
    const result = await props.api.loadAccountDevices(username)
    if (current === deviceRequest) devices.value = result
  } catch (error) { if (current === deviceRequest) deviceError.value = message(error) }
  finally { if (current === deviceRequest) devicesLoading.value = false }
}
function closeDevices(): void { deviceRequest++; deviceUsername.value = ''; devices.value = []; devicesLoading.value = false }
onUnmounted(closeDevices)
function message(error: unknown): string { return error instanceof Error ? error.message : '请求失败，请重试' }
</script>

<style scoped>
.actions,.row-actions,.device-heading { display: flex; align-items: center; gap: 1rem; }.row-actions { flex-wrap: wrap; }.intro { margin: 0 0 1.4rem; color: var(--muted); line-height: 1.65; max-width: 55rem; }.notice { padding: .9rem; border-radius: 8px; color: var(--accent); background: var(--panel-soft); }.devices { margin-top: 1.5rem; }.device-heading { justify-content: space-between; margin-bottom: 1rem; }.device-heading h3 { margin: 0; }.devices .muted { font-size: .85rem; }.devices code { white-space: normal; overflow-wrap: anywhere; }
</style>
