<template>
  <div class="backdrop" role="presentation" @click.self="emit('cancel')">
    <form class="dialog import-dialog" role="dialog" aria-modal="true" aria-labelledby="import-key-title" @submit.prevent="submit">
      <h2 id="import-key-title" class="dialog-title">导入客户端公钥</h2>
      <p class="description">选择或粘贴 tunnel_key.pub。TunnelX 元数据会自动填入下方字段，确认后公钥立即加入授权列表。</p>
      <label class="field"><span>选择 .pub 文件</span><input type="file" accept=".pub,text/plain" @change="readFile" /></label>
      <label class="field"><span>公钥内容</span><textarea v-model.trim="form.public_key" rows="4" required placeholder="ssh-ed25519 AAAA... tunnelx:{...}" @input="parseMetadata" /></label>
      <div class="grid">
        <label class="field"><span>用户名</span><input v-model.trim="form.username" maxlength="128" required /></label>
        <label class="field"><span>邮箱</span><input v-model.trim="form.email" maxlength="254" type="email" required /></label>
      </div>
      <label class="field"><span>计算机名</span><input v-model.trim="form.computer_name" maxlength="255" required /></label>
      <label class="field"><span>导入原因（审计记录）</span><textarea v-model.trim="form.reason" rows="2" maxlength="500" required placeholder="例如：新增研发设备" /></label>
      <p v-if="parseError" class="validation-error">{{ parseError }}</p>
      <div class="dialog-actions"><button class="button secondary" type="button" @click="emit('cancel')">取消</button><button class="button primary" type="submit" :disabled="busy">{{ busy ? '导入中…' : '确认导入' }}</button></div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { reactive, shallowRef } from 'vue'
import type { ImportPublicKeyRequestDto } from '../types/admin'

defineProps<{ busy: boolean }>()
const emit = defineEmits<{ cancel: []; submit: [payload: ImportPublicKeyRequestDto] }>()
const form = reactive<ImportPublicKeyRequestDto>({ public_key: '', username: '', email: '', computer_name: '', reason: '' })
const parseError = shallowRef('')

async function readFile(event: Event): Promise<void> {
  const file = (event.target as HTMLInputElement).files?.[0]
  if (!file) return
  if (file.size > 16 * 1024) { parseError.value = '公钥文件不能超过 16 KiB'; return }
  form.public_key = await file.text()
  parseMetadata()
}

function parseMetadata(): void {
  parseError.value = ''
  const line = form.public_key.trim()
  const match = line.match(/^\S+\s+\S+(?:\s+(.*))?$/s)
  const comment = match?.[1]?.trim() ?? ''
  if (!comment.startsWith('tunnelx:')) return
  try {
    const value = JSON.parse(comment.slice('tunnelx:'.length)) as Partial<{ username: string; email: string; computer_name: string }>
    form.username = typeof value.username === 'string' ? value.username : ''
    form.email = typeof value.email === 'string' ? value.email : ''
    form.computer_name = typeof value.computer_name === 'string' ? value.computer_name : ''
  } catch { parseError.value = '无法解析 .pub 中的 TunnelX 元数据' }
}

function submit(): void {
  if (parseError.value) return
  emit('submit', { ...form })
}
</script>

<style scoped>
.backdrop { position: fixed; z-index: 30; inset: 0; display: grid; place-items: center; padding: 1rem; background: #02090dcc; }
.dialog { padding: 1.5rem; border: 1px solid var(--border); border-radius: 1rem; background: var(--panel); box-shadow: 0 1.5rem 5rem #0008; }
.import-dialog { width: min(42rem, 100%); max-height: 92vh; overflow-y: auto; }
.description { color: var(--muted); line-height: 1.5; }
.field { display: grid; gap: .4rem; margin-top: 1rem; color: var(--muted); font-size: .82rem; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; }
.dialog-actions { display: flex; justify-content: flex-end; gap: .7rem; margin-top: 1.3rem; }
.validation-error { color: var(--danger); }
.dialog-title { margin: 0; }
@media (max-width: 600px) { .grid { grid-template-columns: 1fr; gap: 0; } }
</style>
