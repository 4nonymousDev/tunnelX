<template>
  <section class="page">
    <header class="page-header">
      <div><p class="eyebrow">仅内存状态</p><h1 class="page-title">在线会话</h1></div>
      <span class="count">{{ props.api.sessions.value.length }} 个连接</span>
    </header>
    <div class="table-wrap">
      <table class="data-table">
        <thead><tr><th>名称 / 身份</th><th>角色</th><th>IP</th><th>版本</th><th>状态</th><th>上线时长</th><th>隧道</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="session in props.api.sessions.value" :key="session.id">
            <td><strong>{{ session.name || '未上报' }}</strong><code :title="session.fingerprint">{{ shortFingerprint(session.fingerprint) }}</code></td>
            <td>{{ session.role || '—' }}</td><td>{{ session.remote_ip || '—' }}</td><td>{{ session.version || '—' }}</td>
            <td><span class="status" :class="session.state">{{ session.state }}</span></td>
            <td>{{ formatDuration(session.connected_at) }}</td><td>{{ session.tunnels?.length ?? 0 }}</td>
            <td class="actions"><button class="link-button" type="button" @click="open('disconnect', session)">下线</button><button class="link-button danger-text" type="button" @click="open('block', session)">拉黑</button></td>
          </tr>
        </tbody>
      </table>
      <div v-if="props.api.sessions.value.length === 0" class="empty">当前没有在线会话</div>
    </div>
    <ActionDialog v-if="pending" :title="dialogTitle" :description="pending.name || pending.fingerprint" :busy="props.api.loading.value" confirm-label="确认执行" @confirm="confirm" @cancel="pending = null" />
  </section>
</template>

<script setup lang="ts">
import { computed, shallowRef } from 'vue'
import ActionDialog from '../components/ActionDialog.vue'
import type { AdminApi } from '../composables/useAdminApi'
import type { SessionDto } from '../types/admin'
import { formatDuration, shortFingerprint } from '../utils/format'

const props = defineProps<{ api: AdminApi }>()
const pending = shallowRef<SessionDto | null>(null)
const action = shallowRef<'disconnect' | 'block'>('disconnect')
const dialogTitle = computed(() => action.value === 'disconnect' ? '强制下线会话' : '拉黑客户端指纹')
function open(kind: 'disconnect' | 'block', session: SessionDto) { action.value = kind; pending.value = session }
async function confirm(reason: string) {
  if (!pending.value) return
  if (action.value === 'disconnect') await props.api.disconnectSession(pending.value.id, reason)
  else await props.api.blockClient(pending.value.fingerprint, reason)
  pending.value = null
}
</script>

<style scoped>
.count { color: var(--muted); }
.actions { white-space: nowrap; }
.actions .link-button + .link-button { margin-left: .75rem; }
</style>
