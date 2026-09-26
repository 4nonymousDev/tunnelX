<template>
  <section class="page">
    <header class="page-header"><div><p class="eyebrow">版本管理</p><h1 class="page-title">客户端更新</h1></div><button class="button secondary" type="button" :disabled="busy" @click="refresh">刷新策略</button></header>
    <p class="intro">普通更新由用户自行选择安装。需要强制更新时，在这里设置最低版本和截止时间；默认预留 7 天，期限内可以继续连接。</p>
    <p v-if="pageError" class="error-banner" role="alert">{{ pageError }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="!current && busy" role="status">正在读取策略…</p>
    <template v-if="current">
      <div class="panel current"><strong>当前策略：{{ current.minimum_version ? `最低版本 ${current.minimum_version}` : '未启用强制更新' }}</strong><p v-if="current.enforce_after">截止时间：{{ formatDate(current.enforce_after) }}</p><p v-if="current.message">{{ current.message }}</p></div>
      <form class="panel policy-form" @submit.prevent="save">
        <fieldset :disabled="busy">
          <label class="toggle"><input v-model="enabled" type="checkbox" @change="setDefaultDeadline" />启用强制更新</label>
          <template v-if="enabled">
            <label>最低客户端版本<input v-model="minimum" required placeholder="例如 0.2.1" maxlength="100" pattern="(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)" /></label>
            <label>更新截止时间（本机时区）<input v-model="deadline" required type="datetime-local" min="2020-01-01T00:00" max="2100-12-31T23:59" /></label>
            <button class="link-button" type="button" @click="useSevenDays">设为从现在起 7 天</button>
            <label>给用户的更新说明<textarea v-model="message" rows="3" maxlength="500" placeholder="例如：此版本修复接入安全问题，请及时更新。" /></label>
            <p class="hint">请先发布可用的客户端安装包。到期后，低版本客户端的现有连接和隧道会断开，并停止接入；原账号、密钥和配置保留。旧客户端只有升级后才能显示此提醒。</p>
            <p v-if="immediate" class="error-banner" role="alert">截止时间已到：保存后会立即停止低版本客户端的连接。</p>
          </template>
          <p v-else class="hint">保存后将取消最低版本限制。已经被断开的客户端可以点击“连接”重新接入。</p>
          <label>操作原因（记入审计日志）<input v-model="reason" required maxlength="500" placeholder="说明本次设置或撤销的原因" /></label>
          <button class="button primary" type="submit" :disabled="!reason.trim()">{{ busy ? '保存中…' : enabled ? '保存更新策略' : '保存为不强制更新' }}</button>
        </fieldset>
      </form>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, shallowRef } from 'vue'
import type { AdminApi } from '../composables/useAdminApi'
import type { ClientUpdatePolicyDto } from '../types/admin'
import { formatDate } from '../utils/format'

const props = defineProps<{ api: AdminApi }>()
const current = shallowRef<ClientUpdatePolicyDto>()
const busy = ref(false)
const enabled = ref(false)
const minimum = ref('')
const deadline = ref('')
const message = ref('')
const reason = ref('')
const notice = ref('')
const pageError = ref('')
const immediate = computed(() => Boolean(deadline.value && new Date(deadline.value).getTime() <= Date.now()))

function localTime(date: Date): string {
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
function useSevenDays(): void { deadline.value = localTime(new Date(Date.now() + 7 * 24 * 60 * 60 * 1000)) }
function setDefaultDeadline(): void { if (enabled.value && !deadline.value) useSevenDays() }
function apply(policy: ClientUpdatePolicyDto): void {
  current.value = policy
  enabled.value = Boolean(policy.minimum_version)
  minimum.value = policy.minimum_version
  deadline.value = policy.enforce_after ? localTime(new Date(policy.enforce_after)) : ''
  message.value = policy.message
  reason.value = ''
}
async function refresh(): Promise<void> {
  busy.value = true; pageError.value = ''; notice.value = ''
  try { apply(await props.api.getClientUpdatePolicy()) }
  catch (error) { pageError.value = error instanceof Error ? error.message : '读取策略失败' }
  finally { busy.value = false }
}
async function save(): Promise<void> {
  if (!current.value || busy.value) return
  busy.value = true; pageError.value = ''; notice.value = ''
  try {
    const result = await props.api.setClientUpdatePolicy({
      minimum_version: enabled.value ? minimum.value.trim() : '',
      enforce_after: enabled.value ? new Date(deadline.value).toISOString() : null,
      message: enabled.value ? message.value.trim() : '',
      expected_generation: current.value.generation,
      reason: reason.value.trim(),
    })
    apply(result)
    notice.value = result.minimum_version ? '更新策略已保存，在线客户端将收到通知。' : '已取消强制更新。'
  } catch (error) { pageError.value = error instanceof Error ? error.message : '保存策略失败' }
  finally { busy.value = false }
}
onMounted(refresh)
</script>

<style scoped>
.intro,.hint { color: var(--muted); line-height: 1.7; max-width: 55rem; }.current,.policy-form { margin-top: 1.2rem; max-width: 48rem; }.current p { white-space: pre-wrap; overflow-wrap: anywhere; }.policy-form fieldset { display: grid; gap: 1.2rem; padding: 0; border: 0; }.policy-form label { display: grid; gap: .5rem; }.policy-form .toggle { display: flex; align-items: center; gap: .6rem; }.toggle input { width: auto; }.policy-form button { justify-self: start; }.policy-form textarea { resize: vertical; }.notice { color: var(--accent); }.hint { margin: 0; font-size: .85rem; }
</style>
