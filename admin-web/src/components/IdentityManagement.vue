<template>
  <section class="identity-management panel">
    <h2>身份管理</h2>
    <p>新设备和旧设备补登记均使用<button class="link-button" type="button" @click="emit('register')">登记客户端</button>，一次提交公钥和设备 ID。这里用于查询、撤销或明确变更已有身份。</p>
    <details ref="changePanel" class="change-panel">
    <summary>身份查询与变更</summary>
    <form class="identity-form" @submit.prevent="bind">
      <label>设备 ID（Client ID）<input v-model.trim="form.client_id" maxlength="128" required @input="verified = false" /></label>
      <button class="button" type="button" :disabled="!form.client_id || lookupBusy" @click="lookup">查询此 ID 当前登记</button>
      <p v-if="lookupError" role="alert">{{ lookupError }}</p>
      <p v-if="lookedUpId === form.client_id && !existing">此 ID 尚未登记。请使用<button class="link-button" type="button" @click="emit('register')">登记客户端</button>提交公钥和 ID。</p>
      <template v-if="existing && lookedUpId === form.client_id">
      <p>当前登记：<code>{{ existing.fingerprint }}</code> · {{ existing.revoked ? '已撤销' : '有效' }}</p>
      <label>变更方式<select v-model="keyMode" @change="verified = false"><option value="public_key">更换为新公钥</option><option value="fingerprint">使用已授权指纹 / 恢复原身份</option></select></label>
      <label v-if="keyMode === 'public_key'">新公钥内容<textarea v-model.trim="publicKey" rows="4" maxlength="16384" required placeholder="粘贴新 .pub 文件的完整内容，例如 ssh-ed25519 AAAA…" @input="verified = false" /></label>
      <label v-else>已授权公钥的完整 SHA256 指纹<input v-model.trim="form.fingerprint" maxlength="50" required placeholder="SHA256:…" @input="verified = false" /></label>
      <p v-if="keyMode === 'public_key'">提交后将授权新公钥并替换此设备 ID 的绑定，无需先登记新设备。旧公钥将无法再使用此 ID 连接。</p>
      <label>变更原因<input v-model.trim="form.reason" maxlength="500" required /></label>
      <label class="verify"><input v-model="verified" type="checkbox" required :disabled="lookupBusy" />我已核对设备配置和公钥文件，确认变更此 ID 对应的公钥或恢复已撤销的身份</label>
      <p class="warning">提交将更新第 {{ existing.generation }} 版登记并断开相关现有连接；已撤销的身份将恢复有效。</p>
      <button class="button primary" :disabled="api.loading.value || !verified || lookupBusy">确认{{ existing.revoked ? '恢复 / 变更身份' : '变更身份' }}</button>
      </template>
    </form>
    </details>
    <details class="claims-panel">
    <summary>查看设备自行上报的声明</summary>
    <p class="muted">只展示最近 200 项；设备声明不构成归属证明，也不会覆盖已登记身份。</p>
    <div class="table-wrap"><table class="data-table">
      <thead><tr><th>Client ID / 名称</th><th>声明来源的已认证公钥</th><th>最近声明</th><th></th></tr></thead>
      <tbody><tr v-for="claim in api.identityClaims.value" :key="`${claim.fingerprint}:${claim.client_id}`">
        <td><code>{{ claim.client_id }}</code><small>{{ claim.reported_name || '—' }}</small></td>
        <td><code>{{ claim.fingerprint }}</code></td><td>{{ formatDate(claim.last_seen_at) }}</td>
        <td><button class="link-button" type="button" @click="select(claim.client_id, claim.fingerprint)">查询 / 核对</button></td>
      </tr></tbody>
    </table></div>
    </details>
    <h3>已登记身份</h3>
    <div class="table-wrap"><table class="data-table">
      <thead><tr><th>Client ID</th><th>可信公钥</th><th>状态 / 版本</th><th></th></tr></thead>
      <tbody><tr v-for="binding in api.identities.value" :key="binding.client_id">
        <td><code>{{ binding.client_id }}</code></td><td><code>{{ binding.fingerprint }}</code></td>
        <td>{{ binding.revoked ? '已撤销' : '有效' }} / {{ binding.generation }}</td>
        <td class="identity-actions"><button class="link-button" type="button" @click="select(binding.client_id, binding.fingerprint)">{{ binding.revoked ? '查询 / 恢复' : '查询 / 变更' }}</button><button v-if="!binding.revoked" class="link-button danger-text" type="button" @click="revoke = binding">撤销身份</button></td>
      </tr></tbody>
    </table></div>
    <section v-if="unresolved.length">
      <h3>需要核对的管理操作</h3>
      <p>操作可能已生效，请勿直接重复执行。核对只读取当前授权或会话状态，不会重放旧的授权文件。</p>
      <ul><li v-for="operation in unresolved" :key="operation.id"><code>{{ operation.id }}</code> · {{ operation.action }} · {{ operation.state }}
        <button v-if="operation.state === 'needs_reconcile'" class="link-button" type="button" @click="reconcile = operation.id">核对当前结果</button>
        <span v-else class="muted">操作尚在执行；异常中断后重启服务再核对</span>
      </li></ul>
    </section>
    <ActionDialog v-if="revoke" title="撤销设备身份并断开连接" :busy="api.loading.value" @confirm="confirmRevoke" @cancel="revoke = null" />
    <ActionDialog v-if="reconcile" title="记录核对原因" :busy="api.loading.value" @confirm="confirmReconcile" @cancel="reconcile = ''" />
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, shallowRef, watch } from 'vue'
import type { AdminApi } from '../composables/useAdminApi'
import type { IdentityBindingDto } from '../types/admin'
import { formatDate } from '../utils/format'
import ActionDialog from './ActionDialog.vue'

const props = defineProps<{ api: AdminApi }>()
const emit = defineEmits<{ register: [] }>()
const changePanel = shallowRef<HTMLDetailsElement | null>(null)
const form = reactive({ client_id: '', fingerprint: '', reason: '' })
const keyMode = shallowRef<'public_key' | 'fingerprint'>('public_key')
const publicKey = shallowRef('')
const verified = shallowRef(false)
const revoke = shallowRef<IdentityBindingDto | null>(null)
const reconcile = shallowRef('')
const lookedUpId = shallowRef('')
const lookedUpIdentity = shallowRef<IdentityBindingDto | null>(null)
const lookupBusy = shallowRef(false)
const lookupError = shallowRef('')
const existing = computed(() => lookedUpId.value === form.client_id ? lookedUpIdentity.value : null)
watch(() => [form.client_id, form.fingerprint, publicKey.value, keyMode.value, existing.value?.generation], () => { verified.value = false })
const unresolved = computed(() => props.api.operations.value.filter(item => item.state === 'pending' || item.state === 'needs_reconcile'))
function select(id: string, fingerprint: string) {
  form.client_id = id
  form.fingerprint = fingerprint
  form.reason = ''
  publicKey.value = ''
  keyMode.value = 'public_key'
  verified.value = false
  if (changePanel.value) changePanel.value.open = true
  void lookup()
}
async function lookup() {
  const id = form.client_id
  verified.value = false
  lookedUpId.value = ''
  lookupError.value = ''
  lookupBusy.value = true
  try {
    const binding = await props.api.findIdentity(id)
    if (form.client_id === id) {
      lookedUpIdentity.value = binding
      lookedUpId.value = id
      form.fingerprint = binding?.fingerprint ?? ''
      publicKey.value = ''
      keyMode.value = binding?.revoked ? 'fingerprint' : 'public_key'
    }
  } catch (cause) { lookupError.value = cause instanceof Error ? cause.message : '查询失败' }
  finally { lookupBusy.value = false }
}
async function bind() {
  if (!verified.value || lookupBusy.value || props.api.loading.value || lookedUpId.value !== form.client_id || !existing.value) return
  if (keyMode.value === 'public_key' && !publicKey.value) return
  try {
    await props.api.bindIdentity({ ...form, fingerprint: keyMode.value === 'fingerprint' ? form.fingerprint : '', public_key: keyMode.value === 'public_key' ? publicKey.value : undefined, expected_generation: existing.value.generation })
    verified.value = false
    publicKey.value = ''
    form.reason = ''
    lookedUpId.value = ''
  } catch { /* shared error banner */ }
}
async function confirmRevoke(reason: string) {
  if (!revoke.value) return
  try { await props.api.revokeIdentity(revoke.value, reason); revoke.value = null } catch { /* shared error banner */ }
}
async function confirmReconcile(reason: string) {
  try { await props.api.reconcileOperation(reconcile.value, reason); reconcile.value = '' } catch { /* shared error banner */ }
}
</script>

<style scoped>
.identity-management { margin-top: 1.5rem; }
.identity-management p { color: var(--muted); line-height: 1.6; }
.identity-form { display: grid; gap: .9rem; margin: 1.25rem 0; }
.identity-form label { display: grid; gap: .4rem; }
.identity-form .verify { display: flex; align-items: center; gap: .6rem; }
.verify input { width: auto; }
.identity-form .warning { color: #eab879; }
.change-panel, .claims-panel { margin: 1.25rem 0; }
summary { cursor: pointer; color: var(--accent); }
.identity-actions .link-button + .link-button { margin-left: 1rem; }
code { overflow-wrap: anywhere; }
.muted { color: var(--muted); }
</style>
