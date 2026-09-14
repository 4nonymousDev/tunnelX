<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="emit('close')">
    <form class="dialog" @submit.prevent="submit">
      <div class="dialog-heading">
        <div>
          <p class="eyebrow">CONNECTION</p>
          <h2 class="dialog-title">连接设置</h2>
        </div>
        <button class="dialog-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </div>
      <div class="form-grid">
        <label class="field">
          <span class="field-label">设备名称</span>
          <input v-model.trim="form.name" class="input" placeholder="此设备的显示名称" />
        </label>
        <label class="field">
          <span class="field-label">服务器地址</span>
          <input v-model.trim="form.server_addr" class="input" placeholder="example.com:22" required />
        </label>
        <label class="field">
          <span class="field-label">私钥路径</span>
          <span class="key-path-row">
            <input v-model.trim="form.key_path" class="input" placeholder="D:\keys\tunnel_key" required />
            <button class="button button-secondary" type="button" :disabled="busy" @click="emit('generate', form.key_path)">生成</button>
          </span>
        </label>
      </div>
      <p class="settings-hint">设置保存在核心配置中；若当前已连接，保存后核心会自动重建连接。</p>
      <section class="update-section" aria-labelledby="version-title">
        <div>
          <h3 id="version-title" class="section-title">应用版本</h3>
          <p class="version-copy">GUI {{ updateState.currentGuiVersion }} · CLI {{ updateState.currentCliVersion }}</p>
          <p v-if="updateStatus" class="update-status" :class="{ error: updateState.phase === 'error' }">{{ updateStatus }}</p>
        </div>
        <button class="button button-secondary" type="button" :disabled="updateBusy" @click="emit('checkUpdate')">
          {{ updateState.phase === 'checking' ? '检查中…' : '检查更新' }}
        </button>
      </section>
      <div class="dialog-actions">
        <button class="button button-secondary" type="button" @click="emit('close')">取消</button>
        <button class="button button-primary" type="submit" :disabled="busy">保存设置</button>
      </div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'

import type { SettingsDTO, SnapshotDTO } from '@shared/dto'
import type { UpdateState } from '@shared/ipc'

const props = defineProps<{
  open: boolean
  snapshot?: SnapshotDTO
  busy: boolean
  keyPathOverride?: string
  updateState: UpdateState
}>()
const emit = defineEmits<{
  close: []
  save: [settings: SettingsDTO]
  generate: [keyPath: string]
  checkUpdate: []
}>()
const form = reactive<SettingsDTO>({ name: '', server_addr: '', key_path: '' })
const updateBusy = computed(() => ['checking', 'downloading', 'downloaded', 'installing'].includes(props.updateState.phase))
const updateStatus = computed(() => {
  if (props.updateState.phase === 'available') return `发现 GUI ${props.updateState.latestGuiVersion}`
  if (props.updateState.phase === 'downloading') return `正在下载 ${Math.round(props.updateState.percent ?? 0)}%`
  if (props.updateState.phase === 'downloaded' || props.updateState.phase === 'installing') return props.updateState.message
  if (props.updateState.phase === 'not-available' || props.updateState.phase === 'unsupported' || props.updateState.phase === 'error') return props.updateState.message
  return ''
})

watch(
  () => [props.open, props.snapshot] as const,
  () => {
    if (!props.open || !props.snapshot) return
    Object.assign(form, {
      name: props.snapshot.name,
      server_addr: props.snapshot.server_addr,
      key_path: props.snapshot.key_path,
    })
  },
  { immediate: true },
)

watch(
  () => props.keyPathOverride,
  (keyPath) => {
    if (props.open && keyPath) form.key_path = keyPath
  },
)

function submit(): void {
  emit('save', { ...form })
}
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 50; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2, 5, 13, .72); backdrop-filter: blur(8px); }
.dialog { width: min(520px, 100%); padding: 24px; border: 1px solid var(--line-strong); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px rgba(0,0,0,.5); }
.dialog-heading { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 22px; }
.eyebrow { margin: 0 0 5px; color: var(--accent-light); font-size: 10px; font-weight: 800; letter-spacing: .14em; }
.dialog-title { margin: 0; font-size: 20px; }
.dialog-close { border: 0; color: var(--muted); font-size: 26px; line-height: 1; background: transparent; cursor: pointer; }
.form-grid { display: grid; gap: 15px; }
.field { display: grid; gap: 7px; }
.field-label { color: #c3cad8; font-size: 12px; }
.key-path-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }
.settings-hint { margin: 16px 0 0; color: var(--muted); font-size: 11px; line-height: 1.6; }
.update-section { display: flex; align-items: center; justify-content: space-between; gap: 18px; margin-top: 18px; padding-top: 18px; border-top: 1px solid var(--line); }
.section-title { margin: 0; color: #c3cad8; font-size: 12px; }
.version-copy { margin: 6px 0 0; color: var(--muted); font: 11px Consolas, monospace; }
.update-status { margin: 6px 0 0; color: #67d8f3; font-size: 11px; }
.update-status.error { color: #ffb0b8; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 24px; }
</style>
