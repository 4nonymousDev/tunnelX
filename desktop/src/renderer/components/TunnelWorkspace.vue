<template>
  <div class="app-shell">
    <ConnectionHeader
      :connection="connection"
      :core-status="status"
      :busy="busy"
      :update-available="updateAvailable"
      @connect="connect"
      @disconnect="disconnect"
      @settings="settingsOpen = true"
      @lock="emit('lock')"
      @update="updateDialogOpen = true"
    />

    <main class="workspace">
      <div v-if="error" class="error-banner" role="alert">
        <span>{{ error }}</span>
        <button type="button" aria-label="关闭错误提示" @click="clearError">×</button>
      </div>

      <section class="summary-row">
        <div>
          <p class="eyebrow">TUNNEL WORKSPACE</p>
          <h2 class="workspace-title">隧道工作区</h2>
          <p class="workspace-copy">核心独立运行；关闭此窗口不会中断现有隧道。</p>
        </div>
        <button v-if="activeTab !== 'logs'" class="button button-primary" type="button" :disabled="busy" @click="openNewTunnel">
          {{ activeTab === 'import' ? '+ 从在线列表添加' : '+ 添加隧道' }}
        </button>
      </section>

      <nav class="tabs" aria-label="隧道类型">
        <button
          v-for="tab in tabs"
          :key="tab.id"
          class="tab"
          :class="{ active: activeTab === tab.id }"
          type="button"
          @click="activeTab = tab.id"
        >
          {{ tab.label }} <span class="tab-count">{{ tab.count }}</span>
        </button>
      </nav>

      <LogPanel
        v-if="activeTab === 'logs'"
        class="workspace-content"
        :logs="logs"
        :diagnostics-copied="diagnosticsCopied"
        @copy-diagnostics="copyDiagnostics"
      />
      <TunnelList
        v-else
        class="workspace-content"
        :kind="activeTab"
        :tunnels="visibleTunnels"
        @add="openNewTunnel"
        @edit="openEditTunnel"
        @delete="deleteTarget = $event"
        @toggle="toggleTunnel"
      />
    </main>

    <TunnelEditorDialog
      :open="editorOpen"
      :kind="editorKind"
      :tunnel="editTarget"
      :busy="busy"
      @close="closeEditor"
      @save="saveTunnel"
    />
    <ImportPickerDialog
      :open="importPickerOpen"
      :connected="connection.state === 'connected'"
      :entries="availableImports"
      :unavailable-entries="unavailableImports"
      :used-ports="importListenPorts"
      :busy="busy"
      @close="importPickerOpen = false"
      @save="saveImports"
    />
    <SettingsDialog
      :open="settingsOpen"
      :snapshot="snapshot"
      :busy="busy"
      :key-path-override="keyGenerationResult?.key_path"
      :update-state="updateState"
      @close="settingsOpen = false"
      @save="saveSettings"
      @generate="openKeyGenerator"
      @check-update="checkUpdateFromSettings"
    />
    <UpdateDialog
      :open="updateDialogOpen"
      :state="updateState"
      @close="updateDialogOpen = false"
      @confirm="beginUpdate"
    />
    <KeyGenerationDialog
      :open="keyDialogOpen"
      :key-path="keyGenerationPath"
      :busy="busy"
      :result="keyGenerationResult"
      @close="closeKeyGenerator"
      @browse="browseKeyDirectory"
      @submit="submitKeyGeneration"
      @copy="copyText"
    />
    <ConfirmationDialog
      :confirmation="displayedConfirmation"
      :title="confirmationTitle"
      :busy="busy"
      @answer="answerConfirmation"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, shallowRef } from 'vue'

import type {
  ConfirmationDTO,
  KeyGenerationRequestDTO,
  KeyGenerationResultDTO,
  SettingsDTO,
  TunnelConfigDTO,
  TunnelDTO,
  TunnelKind,
} from '@shared/dto'
import { useTunnelX } from '../composables/useTunnelX'
import { buildDiagnosticReport } from '../utils/diagnostics'
import { availableImportEntries, unavailableImportEntries, usedImportPorts } from '../utils/imports'
import ConfirmationDialog from './ConfirmationDialog.vue'
import ConnectionHeader from './ConnectionHeader.vue'
import ImportPickerDialog from './ImportPickerDialog.vue'
import KeyGenerationDialog from './KeyGenerationDialog.vue'
import LogPanel from './LogPanel.vue'
import SettingsDialog from './SettingsDialog.vue'
import TunnelEditorDialog from './TunnelEditorDialog.vue'
import TunnelList from './TunnelList.vue'
import UpdateDialog from './UpdateDialog.vue'

type WorkspaceTab = TunnelKind | 'logs'

const emit = defineEmits<{ lock: [] }>()

const {
  snapshot,
  status,
  logs,
  busy,
  error,
  updateState,
  connection,
  tunnels,
  pendingConfirmation,
  refresh,
  connect,
  disconnect,
  addTunnel,
  addTunnels,
  updateTunnel,
  deleteTunnel,
  updateSettings,
  selectKeyDirectory,
  generateKey,
  copyText,
  confirm,
  checkForUpdates,
  downloadUpdate,
  installUpdate,
  clearError,
} = useTunnelX()

const activeTab = shallowRef<WorkspaceTab>('export')
const editorOpen = shallowRef(false)
const editorKind = shallowRef<TunnelKind>('export')
const editTarget = shallowRef<TunnelDTO>()
const deleteTarget = shallowRef<TunnelDTO>()
const settingsOpen = shallowRef(false)
const keyDialogOpen = shallowRef(false)
const keyGenerationPath = shallowRef('')
const keyGenerationResult = shallowRef<KeyGenerationResultDTO>()
const importPickerOpen = shallowRef(false)
const diagnosticsCopied = shallowRef(false)
const updateDialogOpen = shallowRef(false)
let diagnosticsResetTimer: ReturnType<typeof setTimeout> | undefined

const visibleTunnels = computed(() => tunnels.value.filter(item => item.config.kind === activeTab.value))
const availableImports = computed(() => availableImportEntries(snapshot.value))
const unavailableImports = computed(() => unavailableImportEntries(snapshot.value))
const importListenPorts = computed(() => usedImportPorts(tunnels.value))
const tabs = computed<{ id: WorkspaceTab; label: string; count: number }[]>(() => [
  { id: 'export', label: '导出', count: tunnels.value.filter(item => item.config.kind === 'export').length },
  { id: 'import', label: '导入', count: tunnels.value.filter(item => item.config.kind === 'import').length },
  { id: 'logs', label: '运行日志', count: logs.value.length },
])
const displayedConfirmation = computed<ConfirmationDTO | undefined>(() => {
  if (pendingConfirmation.value) return pendingConfirmation.value
  if (!deleteTarget.value) return undefined
  return {
    id: 0,
    kind: 'delete_tunnel',
    message: `“${deleteTarget.value.config.name || '未命名隧道'}”将从核心配置中永久删除。`,
  }
})
const confirmationTitle = computed(() => !pendingConfirmation.value && deleteTarget.value
  ? '删除这条隧道？'
  : undefined)
const updateAvailable = computed(() => Boolean(
  updateState.value.latestGuiVersion
  && ['available', 'downloading', 'downloaded', 'installing', 'error'].includes(updateState.value.phase),
))

async function openNewTunnel(): Promise<void> {
  if (activeTab.value === 'logs') return
  if (activeTab.value === 'import') {
    await refresh()
    if (error.value) return
    importPickerOpen.value = true
    return
  }
  editorKind.value = activeTab.value
  editTarget.value = undefined
  editorOpen.value = true
}

function openEditTunnel(tunnel: TunnelDTO): void {
  editorKind.value = tunnel.config.kind
  editTarget.value = tunnel
  editorOpen.value = true
}

function closeEditor(): void {
  editorOpen.value = false
  editTarget.value = undefined
}

async function saveTunnel(value: TunnelConfigDTO): Promise<void> {
  if (editTarget.value) await updateTunnel(editTarget.value.config.id, value)
  else await addTunnel(value)
  if (!error.value) closeEditor()
}

async function saveImports(values: TunnelConfigDTO[]): Promise<void> {
  await addTunnels(values)
  if (!error.value) importPickerOpen.value = false
}

async function toggleTunnel(tunnel: TunnelDTO): Promise<void> {
  await updateTunnel(tunnel.config.id, { ...tunnel.config, enabled: !tunnel.config.enabled })
}

async function saveSettings(value: SettingsDTO): Promise<void> {
  await updateSettings(value)
  if (!error.value) settingsOpen.value = false
}

async function checkUpdateFromSettings(): Promise<void> {
  const state = await checkForUpdates()
  if (state.phase === 'available' || state.phase === 'downloading' || state.phase === 'downloaded') {
    updateDialogOpen.value = true
  }
}

async function beginUpdate(): Promise<void> {
  updateDialogOpen.value = true
  const state = await downloadUpdate()
  if (state.phase === 'downloaded') await installUpdate()
}

function openKeyGenerator(path: string): void {
  keyGenerationPath.value = path
  keyGenerationResult.value = undefined
  keyDialogOpen.value = true
}

function closeKeyGenerator(): void {
  keyDialogOpen.value = false
  keyGenerationResult.value = undefined
}

async function browseKeyDirectory(currentPath: string): Promise<void> {
  const selectedPath = await selectKeyDirectory(currentPath)
  if (selectedPath) keyGenerationPath.value = selectedPath
}

async function submitKeyGeneration(request: KeyGenerationRequestDTO): Promise<void> {
  keyGenerationResult.value = await generateKey(request)
  if (keyGenerationResult.value) keyGenerationPath.value = keyGenerationResult.value.key_path
}

async function copyDiagnostics(): Promise<void> {
  await refresh()
  const refreshError = error.value
  const report = buildDiagnosticReport(snapshot.value, status.value, logs.value, refreshError)
  await copyText(report)
  if (error.value) return
  diagnosticsCopied.value = true
  if (diagnosticsResetTimer) clearTimeout(diagnosticsResetTimer)
  diagnosticsResetTimer = setTimeout(() => {
    diagnosticsCopied.value = false
  }, 2500)
}

onUnmounted(() => {
  if (diagnosticsResetTimer) clearTimeout(diagnosticsResetTimer)
})

async function answerConfirmation(accept: boolean): Promise<void> {
  if (pendingConfirmation.value) {
    await confirm(pendingConfirmation.value.id, accept)
    return
  }
  if (!deleteTarget.value) return
  const target = deleteTarget.value
  if (accept) await deleteTunnel(target.config.id)
  deleteTarget.value = undefined
}
</script>

<style scoped>
.app-shell { display: flex; flex-direction: column; width: 100%; height: 100%; min-height: 0; overflow: hidden; }
.workspace { display: flex; flex: 1 1 auto; flex-direction: column; width: min(980px, calc(100% - 56px)); min-height: 0; margin: 0 auto; padding: 34px 0 24px; overflow: hidden; }
.workspace-content { flex: 1 1 auto; min-height: 0; }
.error-banner { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 20px; padding: 12px 15px; border: 1px solid rgba(255,104,117,.25); border-radius: 12px; color: #ffb0b8; font-size: 12px; background: rgba(255,104,117,.08); }
.error-banner button { border: 0; color: inherit; font-size: 20px; background: transparent; cursor: pointer; }
.summary-row { display: flex; flex: 0 0 auto; align-items: flex-end; justify-content: space-between; gap: 24px; }
.eyebrow { margin: 0 0 7px; color: var(--accent-light); font-size: 10px; font-weight: 800; letter-spacing: .15em; }
.workspace-title { margin: 0; font-size: 24px; letter-spacing: -.03em; }
.workspace-copy { margin: 7px 0 0; color: var(--muted); font-size: 12px; }
.tabs { display: flex; flex: 0 0 auto; gap: 5px; margin: 28px 0 15px; padding: 4px; border: 1px solid var(--line); border-radius: 12px; background: rgba(255,255,255,.025); }
.tab { padding: 8px 13px; border: 0; border-radius: 8px; color: var(--muted); font: inherit; font-size: 12px; background: transparent; cursor: pointer; }
.tab:hover { color: var(--text); }
.tab.active { color: white; background: #29334b; box-shadow: 0 2px 9px rgba(0,0,0,.2); }
.tab-count { margin-left: 5px; color: #7e8aa2; font-size: 10px; }
@media (max-width: 720px) {
  .workspace { width: min(100% - 28px, 980px); padding-top: 24px; }
  .summary-row { align-items: flex-start; }
}
</style>
