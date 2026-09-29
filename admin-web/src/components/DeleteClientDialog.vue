<template>
  <ActionDialog title="删除设备记录" :description="description" confirm-label="确认删除" :busy="busy" :error="error" @cancel="emit('cancel')" @confirm="submit" />
</template>

<script setup lang="ts">
import { computed, shallowRef } from 'vue'
import type { AdminApi } from '../composables/useAdminApi'
import type { ClientDto } from '../types/admin'
import ActionDialog from './ActionDialog.vue'

const props = defineProps<{ api: AdminApi; client: ClientDto }>()
const emit = defineEmits<{ cancel: []; deleted: [] }>()
const busy = shallowRef(false)
const error = shallowRef('')
const description = computed(() => [
  props.client.last_reported_name || props.client.last_client_id || '未知客户端',
  `公钥指纹：${props.client.fingerprint}`,
  '将删除此记录和公钥授权，并撤销此公钥仍持有的身份。其他公钥对应的同名设备不受影响。历史审计和已有黑名单限制保留。',
].join('\n\n'))

async function submit(reason: string): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await props.api.deleteClient(props.client.fingerprint, reason)
    emit('deleted')
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '删除失败，请重试'
  } finally { busy.value = false }
}
</script>
