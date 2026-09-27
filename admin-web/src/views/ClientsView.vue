<template>
  <section class="page">
    <header class="page-header"><div><p class="eyebrow">设备资产</p><h1 class="page-title">设备管理</h1></div><div class="header-actions"><span class="count">{{ props.api.clients.value.length }} 个设备</span><button class="button primary" type="button" @click="openImport">登记设备</button></div></header>
    <p class="muted">获取客户端公钥与设备 ID，点击“登记设备”一次完成公钥授权和身份绑定。</p>
    <div class="table-wrap">
      <table class="data-table">
        <thead><tr><th>客户端</th><th>管理员备注</th><th>授权</th><th>黑名单</th><th>在线会话</th><th>最后接入</th><th></th></tr></thead>
        <tbody>
          <tr v-for="client in props.api.clients.value" :key="client.fingerprint">
            <td><strong>{{ client.last_reported_name || client.last_client_id || '未知客户端' }}</strong><code :title="client.fingerprint">{{ shortFingerprint(client.fingerprint) }}</code></td>
            <td>{{ client.note || '—' }}</td>
            <td><span class="status" :class="client.effective_access ? 'online' : 'closing'">{{ client.effective_access ? '有效' : !client.authorized ? '未授权' : client.blocked ? '受阻' : '待登记设备 ID' }}</span></td>
            <td>{{ client.blocked ? '已拉黑' : '正常' }}</td><td>{{ client.active_session_count }}</td><td>{{ formatDate(client.last_seen_at) }}</td>
            <td><button class="link-button" type="button" @click="emit('select', client.fingerprint)">查看详情</button></td>
          </tr>
        </tbody>
      </table>
      <div v-if="props.api.clients.value.length === 0" class="empty">没有历史客户端记录</div>
    </div>
    <IdentityManagement :api="props.api" @register="openImport" />
    <ImportPublicKeyDialog v-if="importOpen" :busy="props.api.loading.value" :error="importError" @cancel="importOpen = false" @submit="submitImport" />
  </section>
</template>

<script setup lang="ts">
import type { AdminApi } from '../composables/useAdminApi'
import type { ImportPublicKeyRequestDto } from '../types/admin'
import { formatDate, shortFingerprint } from '../utils/format'
import ImportPublicKeyDialog from '../components/ImportPublicKeyDialog.vue'
import IdentityManagement from '../components/IdentityManagement.vue'
import { shallowRef } from 'vue'

const props = defineProps<{ api: AdminApi }>()
const emit = defineEmits<{ select: [fingerprint: string] }>()
const importOpen = shallowRef(false)
const importError = shallowRef('')

function openImport(): void {
  importError.value = ''
  importOpen.value = true
}

async function submitImport(payload: ImportPublicKeyRequestDto): Promise<void> {
  importError.value = ''
  try {
    await props.api.importPublicKey(payload)
    importOpen.value = false
  } catch (cause) { importError.value = cause instanceof Error ? cause.message : '登记失败' }
}
</script>

<style scoped>
.count { color: var(--muted); }
.header-actions { display: flex; align-items: center; gap: 1rem; }
.advanced-management { margin-top: 1.5rem; padding: 1rem; border: 1px solid var(--border); border-radius: 12px; }.advanced-management summary { cursor: pointer; font-weight: 700; }.muted { line-height: 1.6; font-size: .85rem; }
</style>
