<template>
  <section class="page">
    <header class="page-header"><div><button class="back" type="button" @click="emit('back')">← 返回客户端</button><h1 class="page-title">客户端详情</h1></div></header>
    <div v-if="client" class="detail-stack">
      <article class="panel identity">
        <div><p class="eyebrow">{{ client.last_role || '未知角色' }}</p><h2>{{ client.last_reported_name || client.last_client_id || '未知客户端' }}</h2><code>{{ client.fingerprint }}</code></div>
        <div class="identity-status"><span class="status" :class="client.online ? 'online' : 'closing'">{{ client.online ? '在线' : '离线' }}</span><span>{{ client.authorized ? '已授权' : '未授权' }} · {{ client.blocked ? '已拉黑' : '未拉黑' }}</span></div>
      </article>
      <div class="split">
        <article class="panel"><h3>基本资料</h3><dl class="facts"><dt>用户名</dt><dd>{{ client.username || '—' }}</dd><dt>邮箱</dt><dd>{{ client.email || '—' }}</dd><dt>计算机名</dt><dd>{{ client.computer_name || '—' }}</dd><dt>备注</dt><dd>{{ client.note || '—' }}</dd><dt>最后 IP</dt><dd>{{ client.last_ip || '—' }}</dd><dt>客户端 ID</dt><dd>{{ client.last_client_id || '—' }}</dd><dt>版本</dt><dd>{{ client.last_version || '—' }}</dd><dt>首次接入</dt><dd>{{ formatDate(client.first_seen_at) }}</dd><dt>最后接入</dt><dd>{{ formatDate(client.last_seen_at) }}</dd></dl></article>
        <article class="panel"><h3>管理操作</h3><label class="field-label">管理员备注<textarea v-model="note" maxlength="200" rows="3" /></label><div class="detail-actions"><button class="button primary" type="button" @click="openAction('note')">保存备注</button><button v-if="client.blocked" class="button secondary" type="button" @click="openAction('unblock')">解禁</button><button v-else class="button danger" type="button" @click="openAction('block')">拉黑</button></div></article>
      </div>
      <article class="panel"><h3>当前 Session 与发布隧道</h3><div v-if="detailSessions.length" class="session-list"><div v-for="session in detailSessions" :key="session.id" class="session-item"><div><strong>{{ session.name || session.client_id }}</strong><small>{{ session.remote_ip }} · {{ session.state }} · {{ formatDuration(session.connected_at) }}</small></div><button class="link-button danger-text" type="button" @click="openDisconnect(session)">强制下线</button><ul v-if="session.tunnels?.length" class="tunnels"><li v-for="tunnel in session.tunnels" :key="tunnel.id">{{ tunnel.name }} <code>127.0.0.1:{{ tunnel.remote_port }}</code></li></ul></div></div><div v-else class="empty">当前没有在线 Session</div></article>
      <div class="split"><article class="panel"><h3>访问记录</h3><ol v-if="details?.access_events.length" class="timeline"><li v-for="item in details.access_events" :key="item.id"><strong>{{ item.kind }}</strong><span>{{ formatDate(item.time) }} · {{ item.result }}</span></li></ol><div v-else class="empty compact">暂无访问记录</div></article><article class="panel"><h3>管理操作时间线</h3><ol v-if="detailActions.length" class="timeline"><li v-for="item in detailActions" :key="item.id"><strong>{{ item.action }} · {{ item.result }}</strong><span>{{ formatDate(item.created_at) }} · {{ item.reason }}</span></li></ol><div v-else class="empty compact">暂无管理操作</div></article></div>
    </div>
    <div v-else class="empty">正在载入客户端详情…</div>
    <ActionDialog v-if="pendingAction" :title="dialogTitle" :busy="props.api.loading.value" @confirm="confirmAction" @cancel="pendingAction = null" />
  </section>
</template>

<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'
import ActionDialog from '../components/ActionDialog.vue'
import type { AdminApi } from '../composables/useAdminApi'
import type { SessionDto } from '../types/admin'
import { formatDate, formatDuration } from '../utils/format'

const props = defineProps<{ api: AdminApi; fingerprint: string }>()
const emit = defineEmits<{ back: [] }>()
const note = shallowRef('')
const pendingAction = shallowRef<'note' | 'block' | 'unblock' | 'disconnect' | null>(null)
const selectedSession = shallowRef<SessionDto | null>(null)
const details = computed(() => props.api.clientDetail.value)
const client = computed(() => details.value?.client ?? props.api.clients.value.find(item => item.fingerprint === props.fingerprint) ?? null)
const detailSessions = computed(() => details.value?.sessions ?? props.api.sessions.value.filter(item => item.fingerprint === props.fingerprint))
const detailActions = computed(() => details.value?.admin_actions ?? props.api.adminActions.value.filter(item => item.target_id === props.fingerprint))
const dialogTitle = computed(() => ({ note: '保存管理员备注', block: '拉黑客户端', unblock: '解禁客户端', disconnect: '强制下线会话' })[pendingAction.value ?? 'note'])

watch(() => props.fingerprint, async (fingerprint) => { await props.api.loadClientDetail(fingerprint); note.value = client.value?.note ?? '' }, { immediate: true })
function openAction(action: 'note' | 'block' | 'unblock') { pendingAction.value = action }
function openDisconnect(session: SessionDto) { selectedSession.value = session; pendingAction.value = 'disconnect' }
async function confirmAction(reason: string) {
  if (pendingAction.value === 'note') await props.api.updateClientNote(props.fingerprint, note.value.trim(), reason)
  else if (pendingAction.value === 'block') await props.api.blockClient(props.fingerprint, reason)
  else if (pendingAction.value === 'unblock') await props.api.unblockClient(props.fingerprint, reason)
  else if (pendingAction.value === 'disconnect' && selectedSession.value) await props.api.disconnectSession(selectedSession.value.id, reason)
  await props.api.loadClientDetail(props.fingerprint)
  pendingAction.value = null
  selectedSession.value = null
}
</script>

<style scoped>
.back { margin: 0 0 .65rem; padding: 0; border: 0; background: transparent; color: var(--accent); cursor: pointer; }
.detail-stack { display: grid; gap: 1rem; }
.identity { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.identity h2 { margin: .25rem 0 .6rem; }.identity-status { display: grid; justify-items: end; gap: .5rem; color: var(--muted); }.split { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }.facts { display: grid; grid-template-columns: 8rem 1fr; gap: .7rem; }.facts dt { color: var(--muted); }.facts dd { margin: 0; overflow-wrap: anywhere; }.field-label { display: grid; gap: .5rem; color: var(--muted); }.detail-actions { display: flex; gap: .6rem; margin-top: .8rem; }.session-list { display: grid; gap: .8rem; }.session-item { display: grid; grid-template-columns: 1fr auto; gap: .5rem; padding: .8rem; border: 1px solid var(--border); border-radius: 10px; }.session-item small { display: block; margin-top: .25rem; color: var(--muted); }.tunnels { grid-column: 1 / -1; margin: .2rem 0 0; padding-left: 1.2rem; color: var(--muted); }.timeline { display: grid; gap: .8rem; margin: 0; padding-left: 1.2rem; }.timeline li { padding-left: .35rem; }.timeline span { display: block; margin-top: .2rem; color: var(--muted); font-size: .82rem; }.compact { padding: 1rem 0; }
@media (max-width: 760px) { .split { grid-template-columns: 1fr; }.identity { align-items: flex-start; flex-direction: column; }.identity-status { justify-items: start; } }
</style>
