<template>
  <section class="page">
    <header class="page-header"><div><p class="eyebrow">后台权限</p><h1 class="page-title">管理员账号</h1></div><div class="actions"><button class="button secondary" type="button" :disabled="refreshing" @click="refresh">刷新</button><button class="button primary" type="button" @click="openDialog('create')">创建管理员</button></div></header>
    <p class="intro">账号仅用于登录管理后台。重置密码或禁用账号会撤销该管理员的后台登录，设备连接独立管理。</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="pageError" class="error-banner" role="alert">{{ pageError }}</p>
    <div class="table-wrap"><table class="data-table"><thead><tr><th>用户名</th><th>状态</th><th>最后变更</th><th>操作</th></tr></thead><tbody>
      <tr v-for="account in api.accounts.value" :key="account.username"><td><strong>{{ account.username }}</strong><small v-if="account.username === api.username.value">（当前账号）</small></td><td><span class="status" :class="account.enabled ? 'online' : 'closing'">{{ account.enabled ? '启用' : '禁用' }}</span></td><td>{{ formatDate(account.updated_at) }}</td><td><div class="row-actions"><button class="link-button" type="button" @click="openDialog('reset', account)">重置密码</button><button class="link-button" :class="{ 'danger-text': account.enabled }" type="button" @click="openDialog('toggle', account)">{{ account.enabled ? '禁用' : '启用' }}</button></div></td></tr>
    </tbody></table><div v-if="!api.accounts.value.length" class="empty">还没有账号。创建后将账号和密码通过可信渠道交给管理员。</div></div>

    <AccountDialog v-if="dialogMode" :mode="dialogMode" :account="selected" :busy="submitting" :error="dialogError" @cancel="dialogMode = undefined" @create="create" @update="update" />
  </section>
</template>

<script setup lang="ts">
import { ref, shallowRef } from 'vue'
import type { AdminApi } from '../composables/useAdminApi'
import type { AccountDto, CreateAccountRequestDto, UpdateAccountRequestDto } from '../types/admin'
import { formatDate } from '../utils/format'
import AccountDialog from '../components/AccountDialog.vue'

const props = defineProps<{ api: AdminApi }>()
const dialogMode = ref<'create' | 'reset' | 'toggle'>()
const selected = shallowRef<AccountDto>()
const submitting = ref(false)
const dialogError = ref('')
const pageError = ref('')
const notice = ref('')
const refreshing = ref(false)
function openDialog(mode: 'create' | 'reset' | 'toggle', account?: AccountDto): void {
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
    notice.value = `账号 ${request.username} 已创建。请通过可信渠道将登录信息交给管理员。`
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
  } catch (error) { dialogError.value = message(error) }
  finally { if (request.password !== undefined) request.password = ''; submitting.value = false }
}
function message(error: unknown): string { return error instanceof Error ? error.message : '请求失败，请重试' }
</script>

<style scoped>
.actions,.row-actions,.device-heading { display: flex; align-items: center; gap: 1rem; }.row-actions { flex-wrap: wrap; }.intro { margin: 0 0 1.4rem; color: var(--muted); line-height: 1.65; max-width: 55rem; }.notice { padding: .9rem; border-radius: 8px; color: var(--accent); background: var(--panel-soft); }
</style>
