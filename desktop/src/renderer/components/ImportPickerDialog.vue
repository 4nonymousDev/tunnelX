<template>
  <div v-if="open" class="dialog-backdrop" @mousedown.self="emit('close')">
    <form class="dialog" @submit.prevent="submit">
      <div class="dialog-heading">
        <div>
          <p class="eyebrow">IMPORT FROM SERVER</p>
          <h2 class="dialog-title">接入服务端已映射端口</h2>
          <p class="dialog-copy">选择在线设备发布的服务，并指定本机访问端口。</p>
        </div>
        <button class="dialog-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </div>

      <div v-if="!connected" class="empty-message">
        请先连接服务器，连接成功后才能读取在线端口。
      </div>
      <div v-else-if="entries.length === 0 && unavailableEntries.length === 0" class="empty-message">
        核心当前收到的服务端在线注册表为空。请确认导出端仍在线且隧道处于运行状态。
      </div>
      <template v-else>
        <div v-if="entries.length" class="picker-toolbar">
          <span>共 {{ entries.length }} 个可用端口，已选 {{ selectedEntries.length }} 个</span>
          <div class="picker-actions">
            <button class="link-button" type="button" @click="selectAll">全选</button>
            <button class="link-button" type="button" @click="clearSelection">清空</button>
          </div>
        </div>

        <div v-if="entries.length" class="peer-list">
          <section v-for="group in groups" :key="group.id" class="peer-group">
            <div class="peer-heading">
              <strong>{{ group.name }}</strong>
              <span>{{ group.entries.length }} 个端口</span>
            </div>
            <div
              v-for="entry in group.entries"
              :key="registryEntryKey(entry)"
              class="entry-row"
              :class="{ selected: isSelected(entry) }"
            >
              <input
                :checked="isSelected(entry)"
                class="entry-check"
                type="checkbox"
                :aria-label="`选择 ${importName(entry)}`"
                @change="toggleEntry(entry, checkboxValue($event))"
              />
              <div class="entry-copy">
                <strong>{{ importName(entry) }}</strong>
                <span>{{ entry.source_host || '127.0.0.1' }}:{{ entry.source_port }}</span>
              </div>
              <div class="server-port">
                <span>服务端映射</span>
                <strong>{{ entry.remote_port ? `:${entry.remote_port}` : '待分配' }}</strong>
              </div>
              <label class="local-port">
                <span>本机端口</span>
                <input
                  class="input port-input"
                  type="number"
                  min="1"
                  max="65535"
                  :disabled="!isSelected(entry)"
                  :value="selectedPorts[registryEntryKey(entry)] || ''"
                  @input="updatePort(entry, inputNumber($event))"
                />
              </label>
            </div>
          </section>
        </div>
        <div v-else class="empty-message compact">
          服务端记录均不可重复添加，具体原因见下方。
        </div>

        <section v-if="unavailableEntries.length" class="unavailable-list">
          <h3 class="unavailable-title">服务端存在但不可选择</h3>
          <div
            v-for="item in unavailableEntries"
            :key="registryEntryKey(item.entry)"
            class="unavailable-row"
          >
            <div class="entry-copy">
              <strong>{{ importName(item.entry) }}</strong>
              <span>{{ item.entry.name || item.entry.id }} · 服务端 :{{ item.entry.remote_port || '待分配' }}</span>
            </div>
            <span class="unavailable-reason">{{ item.reason }}</span>
          </div>
        </section>
      </template>

      <p v-if="validationError" class="validation-error" role="alert">{{ validationError }}</p>
      <div class="dialog-actions">
        <button class="button button-secondary" type="button" @click="emit('close')">取消</button>
        <button
          class="button button-primary"
          type="submit"
          :disabled="busy || !connected || entries.length === 0"
        >
          添加所选端口
        </button>
      </div>
    </form>
  </div>
</template>

<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'

import type { RegistryDTO, TunnelConfigDTO } from '@shared/dto'
import type { ImportEntryAvailability } from '../utils/imports'
import {
  allocateImportPort,
  importName,
  registryEntryKey,
} from '../utils/imports'

interface RegistryGroup {
  id: string
  name: string
  entries: RegistryDTO[]
}

const props = defineProps<{
  open: boolean
  connected: boolean
  entries: RegistryDTO[]
  unavailableEntries: ImportEntryAvailability[]
  usedPorts: number[]
  busy: boolean
}>()

const emit = defineEmits<{
  close: []
  save: [tunnels: TunnelConfigDTO[]]
}>()

const selectedPorts = shallowRef<Record<string, number>>({})
const validationError = shallowRef('')

const groups = computed<RegistryGroup[]>(() => {
  const grouped = new Map<string, RegistryGroup>()
  for (const entry of props.entries) {
    let group = grouped.get(entry.id)
    if (!group) {
      group = { id: entry.id, name: entry.name || entry.id, entries: [] }
      grouped.set(entry.id, group)
    }
    group.entries.push(entry)
  }
  return [...grouped.values()]
})

const selectedEntries = computed(() => props.entries.filter(isSelected))

watch(
  () => props.open,
  (open) => {
    if (!open) return
    selectedPorts.value = {}
    validationError.value = ''
  },
)

function isSelected(entry: RegistryDTO): boolean {
  return Object.hasOwn(selectedPorts.value, registryEntryKey(entry))
}

function toggleEntry(entry: RegistryDTO, checked: boolean): void {
  const key = registryEntryKey(entry)
  const next = { ...selectedPorts.value }
  if (!checked) {
    delete next[key]
  } else if (!Object.hasOwn(next, key)) {
    const taken = new Set([...props.usedPorts, ...Object.values(next)])
    next[key] = allocateImportPort(entry.source_port, taken)
  }
  selectedPorts.value = next
  validationError.value = ''
}

function updatePort(entry: RegistryDTO, port: number): void {
  const key = registryEntryKey(entry)
  if (!Object.hasOwn(selectedPorts.value, key)) return
  selectedPorts.value = { ...selectedPorts.value, [key]: port }
}

function selectAll(): void {
  const next: Record<string, number> = {}
  const taken = new Set(props.usedPorts)
  for (const entry of props.entries) {
    const port = allocateImportPort(entry.source_port, taken)
    next[registryEntryKey(entry)] = port
    if (port > 0) taken.add(port)
  }
  selectedPorts.value = next
  validationError.value = ''
}

function clearSelection(): void {
  selectedPorts.value = {}
  validationError.value = ''
}

function submit(): void {
  const entries = selectedEntries.value
  if (entries.length === 0) {
    validationError.value = '请至少选择一个服务端映射端口。'
    return
  }

  const ports = entries.map(entry => selectedPorts.value[registryEntryKey(entry)])
  if (ports.some(port => !validPort(port))) {
    validationError.value = '本机端口必须是 1 到 65535 的整数。'
    return
  }
  if (new Set(ports).size !== ports.length) {
    validationError.value = '所选隧道不能使用重复的本机端口。'
    return
  }
  if (ports.some(port => props.usedPorts.includes(port))) {
    validationError.value = '一个或多个本机端口已被现有导入隧道占用。'
    return
  }

  validationError.value = ''
  emit('save', entries.map((entry, index) => ({
    id: '',
    kind: 'import',
    name: importName(entry),
    enabled: true,
    peer_id: entry.id,
    peer_tunnel_id: entry.tunnel_id || '',
    peer_name: entry.name,
    peer_src_port: entry.source_port,
    listen_port: ports[index],
  })))
}

function checkboxValue(event: Event): boolean {
  return (event.target as HTMLInputElement).checked
}

function inputNumber(event: Event): number {
  return Number((event.target as HTMLInputElement).value)
}

function validPort(value?: number): boolean {
  return Number.isInteger(value) && Number(value) >= 1 && Number(value) <= 65535
}
</script>

<style scoped>
.dialog-backdrop { position: fixed; z-index: 50; inset: 0; display: grid; place-items: center; padding: 24px; background: rgba(2, 5, 13, .72); backdrop-filter: blur(8px); }
.dialog { width: min(760px, 100%); max-height: calc(100vh - 48px); overflow: auto; padding: 24px; border: 1px solid var(--line-strong); border-radius: 20px; background: #11182a; box-shadow: 0 28px 90px rgba(0,0,0,.5); }
.dialog-heading { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 20px; }
.eyebrow { margin: 0 0 5px; color: var(--accent-light); font-size: 10px; font-weight: 800; letter-spacing: .14em; }
.dialog-title { margin: 0; font-size: 20px; }
.dialog-copy { margin: 7px 0 0; color: var(--muted); font-size: 12px; }
.dialog-close { border: 0; color: var(--muted); font-size: 26px; line-height: 1; background: transparent; cursor: pointer; }
.empty-message { padding: 42px 24px; border: 1px dashed var(--line-strong); border-radius: 14px; color: var(--muted); text-align: center; }
.empty-message.compact { padding: 20px; }
.picker-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 10px; color: var(--muted); font-size: 12px; }
.picker-actions { display: flex; gap: 12px; }
.link-button { padding: 0; border: 0; color: var(--accent-light); font: inherit; background: transparent; cursor: pointer; }
.peer-list { display: grid; gap: 12px; max-height: min(520px, calc(100vh - 260px)); overflow: auto; padding-right: 4px; }
.peer-group { overflow: hidden; border: 1px solid var(--line); border-radius: 14px; background: rgba(255,255,255,.015); }
.peer-heading { display: flex; justify-content: space-between; gap: 12px; padding: 10px 13px; border-bottom: 1px solid var(--line); color: var(--muted); font-size: 11px; background: rgba(255,255,255,.025); }
.peer-heading strong { color: var(--text); font-size: 12px; }
.entry-row { display: grid; grid-template-columns: auto minmax(170px, 1fr) 110px 110px; align-items: center; gap: 13px; padding: 11px 13px; border-bottom: 1px solid var(--line); }
.entry-row:last-child { border-bottom: 0; }
.entry-row.selected { background: rgba(91,124,250,.07); }
.entry-check { width: 16px; height: 16px; accent-color: var(--accent); }
.entry-copy, .server-port, .local-port { display: grid; gap: 4px; min-width: 0; }
.entry-copy strong { overflow: hidden; font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.entry-copy span, .server-port span, .local-port span { color: var(--muted); font-size: 10px; }
.server-port strong { color: var(--success); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 12px; }
.port-input { height: 32px; }
.validation-error { margin: 14px 0 0; color: var(--danger); font-size: 12px; }
.unavailable-list { margin-top: 14px; overflow: hidden; border: 1px solid rgba(248,184,79,.2); border-radius: 14px; }
.unavailable-title { margin: 0; padding: 10px 13px; color: var(--warning); font-size: 11px; background: rgba(248,184,79,.06); }
.unavailable-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 10px 13px; border-top: 1px solid var(--line); opacity: .72; }
.unavailable-reason { color: var(--warning); font-size: 10px; text-align: right; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 20px; }
@media (max-width: 680px) {
  .entry-row { grid-template-columns: auto 1fr 105px; }
  .server-port { display: none; }
}
</style>
