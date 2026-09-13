<template>
  <section class="page">
    <header class="page-header"><div><p class="eyebrow">持久化资产</p><h1 class="page-title">客户端管理</h1></div><div class="header-actions"><span class="count">{{ props.api.clients.value.length }} 个客户端</span><button class="button primary" type="button" @click="importOpen = true">导入 .pub</button></div></header>
    <div class="table-wrap">
      <table class="data-table">
        <thead><tr><th>客户端</th><th>管理员备注</th><th>授权</th><th>黑名单</th><th>在线会话</th><th>最后接入</th><th></th></tr></thead>
        <tbody>
          <tr v-for="client in props.api.clients.value" :key="client.fingerprint">
            <td><strong>{{ client.last_reported_name || client.last_client_id || '未知客户端' }}</strong><code :title="client.fingerprint">{{ shortFingerprint(client.fingerprint) }}</code></td>
            <td>{{ client.note || '—' }}</td>
            <td><span class="status" :class="client.effective_access ? 'online' : 'closing'">{{ client.effective_access ? '有效' : client.authorized ? '受阻' : '未授权' }}</span></td>
            <td>{{ client.blocked ? '已拉黑' : '正常' }}</td><td>{{ client.active_session_count }}</td><td>{{ formatDate(client.last_seen_at) }}</td>
            <td><button class="link-button" type="button" @click="emit('select', client.fingerprint)">查看详情</button></td>
          </tr>
        </tbody>
      </table>
      <div v-if="props.api.clients.value.length === 0" class="empty">没有历史客户端记录</div>
    </div>
    <ImportPublicKeyDialog v-if="importOpen" :busy="props.api.loading.value" @cancel="importOpen = false" @submit="submitImport" />
  </section>
</template>

<script setup lang="ts">
import type { AdminApi } from '../composables/useAdminApi'
import type { ImportPublicKeyRequestDto } from '../types/admin'
import { formatDate, shortFingerprint } from '../utils/format'
import ImportPublicKeyDialog from '../components/ImportPublicKeyDialog.vue'
import { shallowRef } from 'vue'

const props = defineProps<{ api: AdminApi }>()
const emit = defineEmits<{ select: [fingerprint: string] }>()
const importOpen = shallowRef(false)

async function submitImport(payload: ImportPublicKeyRequestDto): Promise<void> {
  try {
    await props.api.importPublicKey(payload)
    importOpen.value = false
  } catch { /* composable exposes the error banner */ }
}
</script>

<style scoped>
.count { color: var(--muted); }
.header-actions { display: flex; align-items: center; gap: 1rem; }
</style>
