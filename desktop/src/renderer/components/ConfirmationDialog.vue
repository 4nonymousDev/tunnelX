<template>
  <div v-if="confirmation" class="dialog-backdrop">
    <section class="dialog" role="alertdialog" aria-modal="true" aria-labelledby="confirmation-title">
      <div class="warning-mark">!</div>
      <p class="eyebrow">需要确认</p>
      <h2 id="confirmation-title" class="dialog-title">{{ displayTitle }}</h2>
      <p class="message">{{ confirmation.message }}</p>
      <dl class="details">
        <template v-if="confirmation.host">
          <dt>主机</dt><dd>{{ confirmation.host }}</dd>
        </template>
        <template v-if="confirmation.fingerprint">
          <dt>指纹</dt><dd>{{ confirmation.fingerprint }}</dd>
        </template>
        <template v-if="confirmation.path">
          <dt>路径</dt><dd>{{ confirmation.path }}</dd>
        </template>
        <template v-if="confirmation.readers?.length">
          <dt>可读取者</dt><dd>{{ confirmation.readers.join('、') }}</dd>
        </template>
      </dl>
      <div class="dialog-actions">
        <button class="button button-secondary" type="button" :disabled="busy" @click="emit('answer', false)">拒绝</button>
        <button class="button button-primary" type="button" :disabled="busy" @click="emit('answer', true)">确认</button>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { ConfirmationDTO } from '@shared/dto'

const props = defineProps<{ confirmation?: ConfirmationDTO; busy: boolean; title?: string }>()
const emit = defineEmits<{ answer: [accept: boolean] }>()
const displayTitle = computed(() => props.title || ({
  host_key: '信任新的 SSH 主机密钥？',
  key_permissions: '修复私钥文件权限？',
  config_overwrite: '覆盖现有配置文件？',
}[props.confirmation?.kind ?? ''] ?? '允许核心继续操作？'))
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 60; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2, 5, 13, .8); backdrop-filter: blur(9px); }
.dialog { width: min(500px, 100%); padding: 28px; border: 1px solid rgba(248,184,79,.28); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px rgba(0,0,0,.55); }
.warning-mark { display: grid; place-items: center; width: 42px; height: 42px; border-radius: 13px; color: #11182a; font-size: 24px; font-weight: 900; background: var(--warning); }
.eyebrow { margin: 18px 0 6px; color: var(--warning); font-size: 10px; font-weight: 800; letter-spacing: .14em; }
.dialog-title { margin: 0; font-size: 20px; }
.message { margin: 10px 0 0; color: var(--muted); font-size: 13px; line-height: 1.65; }
.details { display: grid; grid-template-columns: auto 1fr; gap: 8px 15px; margin: 18px 0 0; padding: 14px; border-radius: 12px; background: rgba(255,255,255,.035); font-size: 11px; }
.details dt { color: var(--muted); }
.details dd { min-width: 0; margin: 0; overflow-wrap: anywhere; font-family: ui-monospace, Consolas, monospace; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 24px; }
</style>
