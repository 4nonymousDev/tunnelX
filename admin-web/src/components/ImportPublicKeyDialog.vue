<template>
  <div class="backdrop" role="presentation" @click.self="emit('cancel')">
    <form class="dialog import-dialog" role="dialog" aria-modal="true" aria-labelledby="import-key-title" @submit.prevent="submit">
      <h2 id="import-key-title" class="dialog-title">登记设备公钥</h2>
      <p class="description">提交客户端的公钥和设备 ID，完成新设备登记或旧设备补登记。</p>
      <label class="field"><span>设备 ID（Client ID）</span><input v-model.trim="form.client_id" maxlength="128" required placeholder="填写该设备原配置中的 ID" /><small>请使用设备交给运维的原 ID，无需修改客户端配置。</small></label>
      <label class="field"><span>选择 .pub 文件</span><input type="file" accept=".pub,text/plain" @change="readFile" /></label>
      <label class="field"><span>公钥内容</span><textarea v-model.trim="form.public_key" rows="4" required placeholder="ssh-ed25519 AAAA... tunnelx:{...}" @input="parseMetadata" /></label>
      <div class="grid">
        <label class="field"><span>用户名（可选）</span><input v-model.trim="form.username" maxlength="128" /></label>
        <label class="field"><span>邮箱（可选）</span><input v-model.trim="form.email" maxlength="254" type="email" /></label>
      </div>
      <label class="field"><span>计算机名（可选）</span><input v-model.trim="form.computer_name" maxlength="255" /></label>
      <p class="description">旧设备补登记也使用此表单，提交原公钥和原 ID 即可。若该 ID 已绑定其他公钥或已撤销，请在身份查询与变更中处理。</p>
      <label class="field"><span>登记原因（审计记录）</span><textarea v-model.trim="form.reason" rows="2" maxlength="500" required placeholder="例如：新增研发设备或补齐原设备登记" /></label>
      <p v-if="parseError" class="validation-error">{{ parseError }}</p>
      <p v-if="error" class="validation-error" role="alert">{{ error }}</p>
      <div class="dialog-actions"><button class="button secondary" type="button" @click="emit('cancel')">取消</button><button class="button primary" type="submit" :disabled="busy">{{ busy ? '登记中…' : '确认登记' }}</button></div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { reactive, shallowRef } from 'vue'
import type { ImportPublicKeyRequestDto } from '../types/admin'

const props = defineProps<{ busy: boolean; error?: string }>()
const emit = defineEmits<{ cancel: []; submit: [payload: ImportPublicKeyRequestDto] }>()
const form = reactive<ImportPublicKeyRequestDto>({ client_id: '', public_key: '', username: '', email: '', computer_name: '', reason: '' })
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
  if (parseError.value || props.busy) return
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
