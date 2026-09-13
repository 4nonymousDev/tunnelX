import { computed, onUnmounted, readonly, shallowRef } from 'vue'
import type {
  AdminActionDto,
  ApiErrorBody,
  AuditEventDto,
  AuditQuery,
  ClientDetailDto,
  ClientDto,
  OverviewDto,
  PageDto,
  SessionDto,
} from '../types/admin'

const API_ROOT = '/api/v1'

function listPage<T>(payload: unknown): PageDto<T> {
  if (Array.isArray(payload)) return { items: payload as T[], next_cursor: null }
  const value = payload as { items?: T[]; data?: T[]; next_cursor?: string | null }
  return { items: value.items ?? value.data ?? [], next_cursor: value.next_cursor ?? null }
}

export function useAdminApi() {
  const token = shallowRef('')
  const overview = shallowRef<OverviewDto | null>(null)
  const sessions = shallowRef<SessionDto[]>([])
  const clients = shallowRef<ClientDto[]>([])
  const clientDetail = shallowRef<ClientDetailDto | null>(null)
  const auditPage = shallowRef<PageDto<AuditEventDto>>({ items: [], next_cursor: null })
  const adminActions = shallowRef<AdminActionDto[]>([])
  const loading = shallowRef(false)
  const error = shallowRef('')
  const eventsConnected = shallowRef(false)
  let eventController: AbortController | null = null
  let reconnectTimer: number | null = null

  const authenticated = computed(() => token.value.length > 0)

  async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
    if (!token.value) throw new Error('请先输入管理 Token')
    const headers = new Headers(init.headers)
    headers.set('Authorization', `Bearer ${token.value}`)
    if (init.body !== undefined) headers.set('Content-Type', 'application/json')
    const response = await fetch(`${API_ROOT}${path}`, { ...init, headers, cache: 'no-store' })
    if (!response.ok) {
      let body: ApiErrorBody = {}
      try { body = await response.json() as ApiErrorBody } catch { /* empty/non-JSON response */ }
      const detail = typeof body.error === 'string' ? body.error : body.error?.message
      throw new Error(detail ?? body.message ?? `请求失败（HTTP ${response.status}）`)
    }
    if (response.status === 204) return undefined as T
    return await response.json() as T
  }

  async function execute<T>(action: () => Promise<T>): Promise<T> {
    loading.value = true
    error.value = ''
    try { return await action() }
    catch (cause) {
      error.value = cause instanceof Error ? cause.message : '请求失败'
      throw cause
    } finally { loading.value = false }
  }

  async function loadOverview() { overview.value = await request<OverviewDto>('/overview') }
  async function loadSessions() { sessions.value = listPage<SessionDto>(await request('/sessions')).items }
  async function loadClients() { clients.value = listPage<ClientDto>(await request('/clients')).items }
  async function loadAudit(query: AuditQuery = {}) {
    const params = new URLSearchParams()
    Object.entries(query).forEach(([key, value]) => {
      if (value !== undefined && value !== '') params.set(key, String(value))
    })
    auditPage.value = listPage<AuditEventDto>(await request(`/audit/events?${params.toString()}`))
  }
  async function loadAdminActions() {
    adminActions.value = listPage<AdminActionDto>(await request('/admin-actions?limit=200')).items
  }
  async function loadClientDetail(fingerprint: string) {
    const encoded = encodeURIComponent(fingerprint)
    const query = new URLSearchParams({ fingerprint, limit: '200' })
    const [client, sessionPayload, auditPayload, actionPayload] = await Promise.all([
      request<ClientDto>(`/clients/${encoded}`), request(`/sessions?${query}`),
      request(`/audit/events?${query}`), request(`/admin-actions?${query}`),
    ])
    clientDetail.value = {
      client,
      sessions: listPage<SessionDto>(sessionPayload).items,
      access_events: listPage<AuditEventDto>(auditPayload).items,
      admin_actions: listPage<AdminActionDto>(actionPayload).items,
    }
  }

  async function refreshAll() {
    await execute(async () => {
      await Promise.all([loadOverview(), loadSessions(), loadClients(), loadAudit(), loadAdminActions()])
    })
  }

  async function disconnectSession(id: string, reason: string) {
    await execute(() => request(`/sessions/${encodeURIComponent(id)}/disconnect`, { method: 'POST', body: JSON.stringify({ reason }) }))
    await Promise.all([loadSessions(), loadOverview()])
  }
  async function updateClientNote(fingerprint: string, note: string, reason: string) {
    await execute(() => request(`/clients/${encodeURIComponent(fingerprint)}`, { method: 'PATCH', body: JSON.stringify({ note, reason }) }))
    await loadClients()
    await loadClientDetail(fingerprint)
  }
  async function blockClient(fingerprint: string, reason: string, expiresAt: string | null = null) {
    await execute(() => request(`/clients/${encodeURIComponent(fingerprint)}/block`, { method: 'POST', body: JSON.stringify({ reason, expires_at: expiresAt }) }))
    await Promise.all([loadClients(), loadSessions(), loadOverview()])
  }
  async function unblockClient(fingerprint: string, reason: string) {
    await execute(() => request(`/clients/${encodeURIComponent(fingerprint)}/block`, { method: 'DELETE', body: JSON.stringify({ reason }) }))
    await loadClients()
    await loadClientDetail(fingerprint)
  }

  async function exportAudit(query: AuditQuery = {}) {
    const params = new URLSearchParams({ format: 'csv' })
    Object.entries(query).forEach(([key, value]) => {
      if (value !== undefined && value !== '' && key !== 'cursor') params.set(key, String(value))
    })
    const response = await fetch(`${API_ROOT}/audit/events?${params.toString()}`, {
      headers: { Authorization: `Bearer ${token.value}` }, cache: 'no-store',
    })
    if (!response.ok) throw new Error(`导出失败（HTTP ${response.status}）`)
    const url = URL.createObjectURL(await response.blob())
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `tunnelx-audit-${new Date().toISOString().slice(0, 10)}.csv`
    anchor.click()
    URL.revokeObjectURL(url)
  }

  function handleEvent(name: string) {
    if (name === 'sessions.changed') void Promise.all([loadSessions(), loadOverview()]).catch(() => {})
    else if (name === 'clients.changed') void loadClients().catch(() => {})
    else if (name === 'audit.appended') void Promise.all([loadAudit(), loadAdminActions()]).catch(() => {})
  }

  async function readEventStream(signal: AbortSignal) {
    const response = await fetch(`${API_ROOT}/events`, {
      headers: { Authorization: `Bearer ${token.value}`, Accept: 'text/event-stream' }, signal, cache: 'no-store',
    })
    if (!response.ok || !response.body) throw new Error(`实时连接失败（HTTP ${response.status}）`)
    eventsConnected.value = true
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    let eventName = 'message'
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n')
      let boundary = buffer.indexOf('\n\n')
      while (boundary >= 0) {
        const block = buffer.slice(0, boundary)
        buffer = buffer.slice(boundary + 2)
        for (const line of block.split('\n')) if (line.startsWith('event:')) eventName = line.slice(6).trim()
        handleEvent(eventName)
        eventName = 'message'
        boundary = buffer.indexOf('\n\n')
      }
    }
  }

  function connectEvents() {
    eventController?.abort()
    if (reconnectTimer !== null) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    if (!token.value) return
    const controller = new AbortController()
    eventController = controller
    void readEventStream(controller.signal).catch(() => {
      // Connection failures and normal EOF share the reconnect path below.
    }).finally(() => {
      if (eventController !== controller) return
      eventsConnected.value = false
      eventController = null
      if (token.value && !controller.signal.aborted) reconnectTimer = window.setTimeout(connectEvents, 2500)
    })
  }

  async function authenticate(value: string) {
    token.value = value.trim()
    try {
      await refreshAll()
      connectEvents()
    } catch (cause) {
      token.value = ''
      throw cause
    }
  }

  function clearToken() {
    token.value = ''
    eventController?.abort()
    if (reconnectTimer !== null) window.clearTimeout(reconnectTimer)
    eventsConnected.value = false
    overview.value = null
    sessions.value = []
    clients.value = []
    clientDetail.value = null
  }

  onUnmounted(clearToken)

  return {
    authenticated, overview: readonly(overview), sessions: readonly(sessions), clients: readonly(clients),
    clientDetail: readonly(clientDetail), auditPage: readonly(auditPage), adminActions: readonly(adminActions),
    loading: readonly(loading), error: readonly(error), eventsConnected: readonly(eventsConnected),
    authenticate, clearToken, refreshAll, loadAudit, loadClientDetail, disconnectSession,
    updateClientNote, blockClient, unblockClient, exportAudit,
  }
}

export type AdminApi = ReturnType<typeof useAdminApi>
