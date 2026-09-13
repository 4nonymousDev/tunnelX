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
          <span class="field-label">SSH 用户</span>
          <input v-model.trim="form.server_user" class="input" placeholder="tunnel" required />
        </label>
        <label class="field">
          <span class="field-label">私钥路径</span>
          <input v-model.trim="form.key_path" class="input" placeholder="D:\keys\tunnel_key" required />
        </label>
      </div>
      <p class="settings-hint">设置保存在核心配置中；若当前已连接，保存后核心会自动重建连接。</p>
      <div class="dialog-actions">
        <button class="button button-secondary" type="button" @click="emit('close')">取消</button>
        <button class="button button-primary" type="submit" :disabled="busy">保存设置</button>
      </div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'

import type { SettingsDTO, SnapshotDTO } from '@shared/dto'

const props = defineProps<{ open: boolean; snapshot?: SnapshotDTO; busy: boolean }>()
const emit = defineEmits<{ close: []; save: [settings: SettingsDTO] }>()
const form = reactive<SettingsDTO>({ name: '', server_addr: '', server_user: '', key_path: '' })

watch(
  () => [props.open, props.snapshot] as const,
  () => {
    if (!props.open || !props.snapshot) return
    Object.assign(form, {
      name: props.snapshot.name,
      server_addr: props.snapshot.server_addr,
      server_user: props.snapshot.server_user,
      key_path: props.snapshot.key_path,
    })
  },
  { immediate: true },
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
.settings-hint { margin: 16px 0 0; color: var(--muted); font-size: 11px; line-height: 1.6; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 24px; }
</style>
