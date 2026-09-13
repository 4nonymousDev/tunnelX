<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="emit('close')">
    <form class="dialog" @submit.prevent="submit">
      <div class="dialog-heading">
        <div>
          <p class="eyebrow">{{ form.kind === 'export' ? 'EXPORT' : 'IMPORT' }}</p>
          <h2 class="dialog-title">{{ tunnel ? '编辑隧道' : '添加隧道' }}</h2>
        </div>
        <button class="dialog-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </div>

      <div class="form-grid">
        <label class="field field-wide">
          <span class="field-label">名称</span>
          <input v-model.trim="form.name" class="input" placeholder="例如：家庭 NAS" />
        </label>

        <template v-if="form.kind === 'export'">
          <label class="field">
            <span class="field-label">本地地址</span>
            <input v-model.trim="form.local_host" class="input" placeholder="127.0.0.1" required />
          </label>
          <label class="field">
            <span class="field-label">本地端口</span>
            <input v-model.number="form.local_port" class="input" type="number" min="1" max="65535" required />
          </label>
        </template>

        <template v-else>
          <label class="field field-wide">
            <span class="field-label">对端设备 ID</span>
            <input v-model.trim="form.peer_id" class="input" placeholder="对端的稳定设备 ID" required />
          </label>
          <label class="field">
            <span class="field-label">对端隧道 ID</span>
            <input v-model.trim="form.peer_tunnel_id" class="input" placeholder="可选" />
          </label>
          <label class="field">
            <span class="field-label">对端显示名</span>
            <input v-model.trim="form.peer_name" class="input" placeholder="可选" />
          </label>
          <label class="field">
            <span class="field-label">对端源端口</span>
            <input v-model.number="form.peer_src_port" class="input" type="number" min="1" max="65535" required />
          </label>
          <label class="field">
            <span class="field-label">本机监听端口</span>
            <input v-model.number="form.listen_port" class="input" type="number" min="1" max="65535" required />
          </label>
        </template>

        <label class="check-row field-wide">
          <input v-model="form.enabled" type="checkbox" />
          <span>保存后立即启用这条隧道</span>
        </label>
      </div>

      <p v-if="validationError" class="validation-error">{{ validationError }}</p>
      <div class="dialog-actions">
        <button class="button button-secondary" type="button" @click="emit('close')">取消</button>
        <button class="button button-primary" type="submit" :disabled="busy">保存</button>
      </div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { reactive, shallowRef, watch } from 'vue'

import type { TunnelConfigDTO, TunnelDTO, TunnelKind } from '@shared/dto'

const props = defineProps<{
  open: boolean
  kind: TunnelKind
  tunnel?: TunnelDTO
  busy: boolean
}>()
const emit = defineEmits<{
  close: []
  save: [tunnel: TunnelConfigDTO]
}>()

const form = reactive<TunnelConfigDTO>(emptyTunnel('export'))
const validationError = shallowRef('')

watch(
  () => [props.open, props.tunnel, props.kind] as const,
  () => {
    if (!props.open) return
    Object.assign(form, emptyTunnel(props.kind), props.tunnel?.config)
    validationError.value = ''
  },
  { immediate: true },
)

function emptyTunnel(kind: TunnelKind): TunnelConfigDTO {
  return {
    id: '',
    kind,
    name: '',
    enabled: true,
    local_host: '127.0.0.1',
    local_port: 0,
    peer_id: '',
    peer_tunnel_id: '',
    peer_name: '',
    peer_src_port: 0,
    listen_port: 0,
  }
}

function submit(): void {
  validationError.value = validate()
  if (validationError.value) return
  emit('save', { ...form })
}

function validate(): string {
  if (form.kind === 'export') {
    if (!form.local_host) return '请填写本地地址。'
    if (!validPort(form.local_port)) return '请填写有效的本地端口。'
  } else {
    if (!form.peer_id) return '请填写对端设备 ID。'
    if (!validPort(form.peer_src_port) || !validPort(form.listen_port)) return '请填写有效的对端和监听端口。'
  }
  return ''
}

function validPort(value?: number): boolean {
  return Number.isInteger(value) && Number(value) >= 1 && Number(value) <= 65535
}
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 50; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2, 5, 13, .72); backdrop-filter: blur(8px); }
.dialog { width: min(570px, 100%); max-height: calc(100vh - 48px); overflow: auto; padding: 24px; border: 1px solid var(--line-strong); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px rgba(0,0,0,.5); }
.dialog-heading { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 22px; }
.eyebrow { margin: 0 0 5px; color: var(--accent-light); font-size: 10px; font-weight: 800; letter-spacing: .14em; }
.dialog-title { margin: 0; font-size: 20px; }
.dialog-close { border: 0; color: var(--muted); font-size: 26px; line-height: 1; background: transparent; cursor: pointer; }
.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 15px; }
.field { display: grid; gap: 7px; }
.field-wide { grid-column: 1 / -1; }
.field-label { color: #c3cad8; font-size: 12px; }
.check-row { display: flex; align-items: center; gap: 9px; color: var(--muted); font-size: 12px; }
.validation-error { margin: 15px 0 0; color: var(--danger); font-size: 12px; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 24px; }
</style>
