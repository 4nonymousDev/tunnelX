<template>
  <section class="tunnel-list">
    <div v-if="tunnels.length" class="cards">
      <TunnelCard
        v-for="tunnel in tunnels"
        :key="tunnel.config.id"
        :tunnel="tunnel"
        @edit="emit('edit', $event)"
        @delete="emit('delete', $event)"
        @toggle="emit('toggle', $event)"
      />
    </div>
    <div v-else class="empty-state">
      <div class="empty-symbol">{{ kind === 'export' ? '↑' : '↓' }}</div>
      <h2 class="empty-title">还没有{{ kindLabel }}隧道</h2>
      <p class="empty-copy">{{ emptyCopy }}</p>
      <button class="button button-primary" type="button" @click="emit('add')">{{ addLabel }}</button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { TunnelDTO, TunnelKind } from '@shared/dto'
import TunnelCard from './TunnelCard.vue'

const props = defineProps<{ tunnels: TunnelDTO[]; kind: TunnelKind }>()
const emit = defineEmits<{
  add: []
  edit: [tunnel: TunnelDTO]
  delete: [tunnel: TunnelDTO]
  toggle: [tunnel: TunnelDTO]
}>()

const kindLabel = computed(() => props.kind === 'export' ? '导出' : '导入')
const emptyCopy = computed(() => props.kind === 'export'
  ? '把本机或局域网服务安全地发布到 TunnelX 服务端。'
  : '选择另一台设备发布的服务，并映射到本机端口。')
const addLabel = computed(() => props.kind === 'export' ? '添加第一条' : '从在线列表添加')
</script>

<style scoped>
.tunnel-list { min-height: 0; overflow-x: hidden; overflow-y: auto; padding-right: 6px; scrollbar-gutter: stable; }
.cards { display: grid; gap: 10px; }
.empty-state { display: grid; justify-items: center; height: 100%; min-height: 260px; align-content: center; padding: 36px; border: 1px dashed var(--line-strong); border-radius: 18px; text-align: center; background: rgba(255,255,255,.015); }
.empty-symbol { display: grid; place-items: center; width: 52px; height: 52px; border-radius: 16px; color: var(--accent-light); font-size: 26px; background: rgba(91,124,250,.12); }
.empty-title { margin: 16px 0 0; font-size: 16px; }
.empty-copy { max-width: 380px; margin: 8px 0 20px; color: var(--muted); font-size: 13px; line-height: 1.6; }
</style>
