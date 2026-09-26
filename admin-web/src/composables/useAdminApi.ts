import { computed, onUnmounted, readonly, shallowRef } from 'vue'
import type {
  AdminActionDto,
  ApiErrorBody,
  AuditEventDto,
  AuditQuery,
  ClientDetailDto,
  ClientDto,
  OverviewDto,
  ImportPublicKeyRequestDto,
  ImportPublicKeyResultDto,
  PageDto,
  SessionDto,
  IdentityBindingDto,
  IdentityClaimDto,
  AdminOperationDto,
  BindIdentityRequestDto,
  AccountDto,
  AccountDeviceDto,
  CreateAccountRequestDto,
  UpdateAccountRequestDto,
  AdminSessionDto,
} from '../types/admin'

const API_ROOT = '/api/v1'

function listPage<T>(payload: unknown): PageDto<T> {
  if (Array.isArray(payload)) return { items: payload as T[], next_cursor: null }
  const value = payload as { items?: T[]; data?: T[]; next_cursor?: string | null }
  return { items: value.items ?? value.data ?? [], next_cursor: value.next_cursor ?? null }
}

export function useAdminApi() {
  const session = shallowRef<AdminSessionDto | null>(null)
  const restoringSession = shallowRef(true)
  const authBusy = shallowRef(false)
  const overview = shallowRef<OverviewDto | null>(null)
  const sessions = shallowRef<SessionDto[]>([])
  const clients = shallowRef<ClientDto[]>([])
  const accounts = shallowRef<AccountDto[]>([])
  const clientDetail = shallowRef<ClientDetailDto | null>(null)
  const auditPage = shallowRef<PageDto<AuditEventDto>>({ items: [], next_cursor: null })
  const adminActions = shallowRef<AdminActionDto[]>([])
  const identities = shallowRef<IdentityBindingDto[]>([])
  const identityClaims = shallowRef<IdentityClaimDto[]>([])
  const operations = shallowRef<AdminOperationDto[]>([])
  const loading = shallowRef(false)
  const error = shallowRef('')
  const eventsConnected = shallowRef(false)
  let eventController: AbortController | null = null
  let reconnectTimer: number | null = null
  let reconnectAttempt = 0
  let refreshTimer: number | null = null
  let refreshRunning = false
  let refreshDirty = false
  let authEpoch = 0
  const requests = new Set<AbortController>()
  let currentAuditQuery: AuditQuery = {}

  const authenticated = computed(() => session.value !== null)
  const username = computed(() => session.value?.username ?? '')

  async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
    if (!session.value) throw new Error('请先登录管理员账号')
    const headers = new Headers(init.headers)
    const method = (init.method ?? 'GET').toUpperCase()
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', session.value.csrf_token)
    if (init.body !== undefined) headers.set('Content-Type', 'application/json')
    const epoch = authEpoch
    const controller = new AbortController()
    requests.add(controller)
    const timeout = window.setTimeout(() => controller.abort(), 15000)
    try {
      const response = await fetch(`${API_ROOT}${path}`, { ...init, headers, signal: controller.signal, cache: 'no-store', credentials: 'same-origin' })
      if (epoch !== authEpoch || !session.value) throw new Error('管理会话已结束')
      if (response.status === 401) { clearSession('登录已过期或权限已变更，请重新登录'); throw new Error('管理会话已结束') }
      const payload: unknown = response.status === 204 ? undefined : await response.json().catch(() => {
        if (response.ok) throw new Error('服务器返回了无效响应')
        return {}
      })
      if (epoch !== authEpoch || !session.value) throw new Error('管理会话已结束')
      if (response.status === 202) {
        const state = payload as { operation_id: string }
        void loadGovernance().catch(() => {})
        throw new Error(`操作可能已经生效，请在客户端管理中核对操作 ${state.operation_id}，勿重复执行`)
      }
      if (!response.ok) {
        if (response.status === 503) throw new Error('服务器繁忙，请稍后重试')
        if (response.status === 409 && path.startsWith('/accounts/')) throw new Error('操作冲突：请刷新账号后重试，并确保至少保留一名启用的管理员')
        if (response.status === 409 && path === '/accounts') throw new Error('用户名已存在，请使用其他用户名或管理现有账号')
        if (response.status === 409 && path === '/clients/import-key') {
          throw new Error('设备 ID 已绑定其他公钥或已撤销，请在身份查询与变更中处理；重复登记不会覆盖现有身份。')
        }
        const body = payload as ApiErrorBody
        const detail = typeof body.error === 'string' ? body.error : body.error?.message
        throw new Error(detail ?? body.message ?? `请求失败（HTTP ${response.status}）`)
      }
      if (controller.signal.aborted) throw new Error('请求超时')
      return payload as T
    } finally { window.clearTimeout(timeout); requests.delete(controller) }
  }

  async function execute<T>(action: () => Promise<T>): Promise<T> {
    const epoch = authEpoch
    loading.value = true
    error.value = ''
    try { return await action() }
    catch (cause) {
      if (epoch === authEpoch) error.value = cause instanceof Error ? cause.message : '请求失败'
      throw cause
    } finally { if (epoch === authEpoch) loading.value = false }
  }

  async function loadValue<T>(path: string, apply: (value: T) => void) {
    const epoch = authEpoch
    const value = await request<T>(path)
    if (epoch === authEpoch && session.value) apply(value)
  }
  async function loadOverview() { await loadValue<OverviewDto>('/overview', value => { overview.value = value }) }
  async function loadSessions() { await loadValue('/sessions', value => { sessions.value = listPage<SessionDto>(value).items }) }
  async function loadClients() { await loadValue('/clients', value => { clients.value = listPage<ClientDto>(value).items }) }
  async function loadAccounts() { await loadValue('/accounts', value => { accounts.value = listPage<AccountDto>(value).items }) }
  async function loadAccountDevices(username: string): Promise<AccountDeviceDto[]> {
    return listPage<AccountDeviceDto>(await request(`/accounts/${encodeURIComponent(username)}/devices`)).items
  }
  async function createAccount(payload: CreateAccountRequestDto): Promise<AccountDto> {
    const result = await execute(() => request<AccountDto>('/accounts', { method: 'POST', body: JSON.stringify(payload) }))
    await loadAccounts()
    return result
  }
  async function updateAccount(username: string, payload: UpdateAccountRequestDto): Promise<AccountDto> {
    const result = await execute(() => request<AccountDto>(`/accounts/${encodeURIComponent(username)}`, { method: 'PATCH', body: JSON.stringify(payload) }))
    await Promise.all([loadAccounts(), loadSessions(), loadClients()])
    return result
  }
  async function loadAudit(query: AuditQuery = {}) {
    currentAuditQuery = { ...query }
    const params = new URLSearchParams()
    Object.entries(query).forEach(([key, value]) => {
      if (value !== undefined && value !== '') params.set(key, String(value))
    })
    await loadValue(`/audit/events?${params.toString()}`, value => { auditPage.value = listPage<AuditEventDto>(value) })
  }
  async function loadAdminActions() {
    await loadValue('/admin-actions?limit=200', value => { adminActions.value = listPage<AdminActionDto>(value).items })
  }
  async function loadGovernance() {
    const epoch = authEpoch
    const [bindings, claims, pending] = await Promise.all([
      request('/identities?limit=200'), request('/identity-claims?limit=200'), request('/operations?limit=200'),
    ])
    if (epoch !== authEpoch || !session.value) return
    identities.value = listPage<IdentityBindingDto>(bindings).items
    identityClaims.value = listPage<IdentityClaimDto>(claims).items
    operations.value = listPage<AdminOperationDto>(pending).items
  }
  async function findIdentity(clientId: string) {
    const page = listPage<IdentityBindingDto>(await request(`/identities?${new URLSearchParams({client_id: clientId})}`))
    return page.items[0] ?? null
  }
  async function bindIdentity(payload: BindIdentityRequestDto) {
    await execute(() => request('/identities', { method: 'POST', body: JSON.stringify(payload) }))
    await Promise.all([loadGovernance(), loadClients(), loadSessions()])
  }
  async function revokeIdentity(binding: IdentityBindingDto, reason: string) {
    await execute(() => request(`/identities/${encodeURIComponent(binding.client_id)}/revoke`, {
      method: 'POST', body: JSON.stringify({ expected_generation: binding.generation, reason }),
    }))
    await Promise.all([loadGovernance(), loadClients(), loadSessions()])
  }
  async function reconcileOperation(id: string, reason: string) {
    await execute(() => request(`/operations/${encodeURIComponent(id)}/reconcile`, { method: 'POST', body: JSON.stringify({ reason }) }))
    await Promise.all([loadGovernance(), loadAdminActions()])
  }
  async function loadClientDetail(fingerprint: string) {
    const epoch = authEpoch
    const encoded = encodeURIComponent(fingerprint)
    const query = new URLSearchParams({ fingerprint, limit: '200' })
    const [client, sessionPayload, auditPayload, actionPayload] = await Promise.all([
      request<ClientDto>(`/clients/${encoded}`), request(`/sessions?${query}`),
      request(`/audit/events?${query}`), request(`/admin-actions?${query}`),
    ])
    if (epoch !== authEpoch || !session.value) return
    clientDetail.value = {
      client,
      sessions: listPage<SessionDto>(sessionPayload).items,
      access_events: listPage<AuditEventDto>(auditPayload).items,
      admin_actions: listPage<AdminActionDto>(actionPayload).items,
    }
  }

  async function refreshAll() {
    await execute(async () => {
      await Promise.all([loadOverview(), loadSessions(), loadClients(), loadAccounts(), loadAudit(), loadAdminActions(), loadGovernance()])
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
  async function importPublicKey(payload: ImportPublicKeyRequestDto) {
    const result = await execute(() => request<ImportPublicKeyResultDto>('/clients/import-key', { method: 'POST', body: JSON.stringify(payload) }))
    await Promise.all([loadClients(), loadGovernance()])
    return result
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
    if (!session.value) throw new Error('请先登录管理员账号')
    const epoch = authEpoch
    const params = new URLSearchParams({ format: 'csv' })
    Object.entries(query).forEach(([key, value]) => {
      if (value !== undefined && value !== '' && key !== 'cursor') params.set(key, String(value))
    })
    const controller = new AbortController()
    requests.add(controller)
    const timeout = window.setTimeout(() => controller.abort(), 30000)
    let blob: Blob
    try {
      const response = await fetch(`${API_ROOT}/audit/events?${params.toString()}`, {
        credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
      })
      if (epoch !== authEpoch || !session.value) throw new Error('管理会话已结束')
      if (response.status === 401) { clearSession('登录已过期或权限已变更，请重新登录'); throw new Error('管理会话已结束') }
      if (!response.ok) throw new Error(`导出失败（HTTP ${response.status}）`)
      blob = await response.blob()
      if (epoch !== authEpoch || !session.value) throw new Error('管理会话已结束')
    } finally { window.clearTimeout(timeout); requests.delete(controller) }
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `tunnelx-audit-${new Date().toISOString().slice(0, 10)}.csv`
    anchor.click()
    URL.revokeObjectURL(url)
  }

  function handleEvent(name: string) {
    if (!['sessions.changed', 'clients.changed', 'accounts.changed', 'audit.appended'].includes(name)) return
    refreshDirty = true
    scheduleRefresh()
  }
  function scheduleRefresh() {
    if (refreshTimer !== null || refreshRunning || !session.value || !refreshDirty) return
    refreshTimer = window.setTimeout(() => {
      refreshTimer = null
      refreshRunning = true
      refreshDirty = false
      const epoch = authEpoch
      void Promise.all([loadSessions(), loadOverview(), loadClients(), loadAccounts(), loadAudit(currentAuditQuery), loadAdminActions(), loadGovernance()])
        .catch(() => {}).finally(() => { if (epoch === authEpoch) { refreshRunning = false; scheduleRefresh() } })
    }, document.hidden ? 5000 : 1000)
  }

  async function readEventStream(signal: AbortSignal) {
    const epoch = authEpoch
    const response = await fetch(`${API_ROOT}/events`, {
      headers: { Accept: 'text/event-stream' }, signal, cache: 'no-store', credentials: 'same-origin',
    })
    if (epoch !== authEpoch || !session.value || signal.aborted) return
    if (response.status === 401) { clearSession('登录已过期或权限已变更，请重新登录'); return }
    if (!response.ok || !response.body) throw new Error(`实时连接失败（HTTP ${response.status}）`)
    eventsConnected.value = true
    reconnectAttempt = 0
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    let eventName = 'message'
    try {
    while (true) {
      const { value, done } = await reader.read()
      if (done || signal.aborted || epoch !== authEpoch || !session.value) break
      buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n')
      if (buffer.length > 65536) throw new Error('实时事件超过大小限制')
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
    } finally {
      await reader.cancel().catch(() => {})
      reader.releaseLock()
    }
  }

  function connectEvents() {
    eventController?.abort()
    if (reconnectTimer !== null) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    if (!session.value) return
    const controller = new AbortController()
    eventController = controller
    void readEventStream(controller.signal).catch(() => {
      // Connection failures and normal EOF share the reconnect path below.
    }).finally(() => {
      if (eventController !== controller) return
      eventsConnected.value = false
      eventController = null
      if (session.value && !controller.signal.aborted) {
        const delay = Math.min(30000, 1000 * 2 ** Math.min(reconnectAttempt++, 5)) + Math.random() * 750
        reconnectTimer = window.setTimeout(connectEvents, delay)
      }
    })
  }

  async function openSession(path: '/auth/login' | '/auth/session', init: RequestInit = {}) {
    const epoch = authEpoch
    const controller = new AbortController()
    requests.add(controller)
    const timeout = window.setTimeout(() => controller.abort(), 15000)
    try {
      const response = await fetch(`${API_ROOT}${path}`, {
        ...init, credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
        ...(init.body !== undefined ? { headers: { 'Content-Type': 'application/json' } } : {}),
      })
      if (epoch !== authEpoch) return false
      if (response.status === 401 && path === '/auth/session') return false
      if (!response.ok) {
        if (response.status === 429 || response.status === 503) throw new Error('登录尝试过多或服务器繁忙，请稍后重试')
        throw new Error(path === '/auth/login' ? '登录失败，请检查账号、密码及管理员权限' : '无法恢复登录，请重试')
      }
      const payload: unknown = await response.json().catch(() => { throw new Error('服务器返回了无效登录响应') })
      if (epoch !== authEpoch) return false
      const value = payload as Partial<AdminSessionDto> | null
      if (!value || typeof value.username !== 'string' || typeof value.csrf_token !== 'string' || !value.csrf_token || typeof value.expires_at !== 'string') throw new Error('服务器返回了无效登录响应')
      session.value = value as AdminSessionDto
      await refreshAll()
      if (epoch === authEpoch && session.value) connectEvents()
      return epoch === authEpoch && session.value !== null
    } finally { window.clearTimeout(timeout); requests.delete(controller) }
  }

  async function authenticate(accountName: string, password: string) {
    if (authBusy.value) return
    clearSession()
    const epoch = authEpoch
    authBusy.value = true
    try { await openSession('/auth/login', { method: 'POST', body: JSON.stringify({ username: accountName.trim(), password }) }) }
    catch (cause) {
      if (epoch === authEpoch) clearSession(cause instanceof Error ? cause.message : '无法连接服务器，请重试')
      throw cause
    } finally { if (epoch === authEpoch || !session.value) authBusy.value = false }
  }

  async function restoreSession() {
    clearSession()
    const epoch = authEpoch
    restoringSession.value = true
    try { await openSession('/auth/session') }
    catch { if (epoch === authEpoch) clearSession('无法恢复登录，请重新登录或刷新重试') }
    finally { restoringSession.value = false }
  }

  async function logout() {
    if (!session.value || authBusy.value) return
    authBusy.value = true
    try { await execute(() => request<void>('/auth/logout', { method: 'POST' })); clearSession() }
    finally { authBusy.value = false }
  }

  function clearSession(message = '') {
    authEpoch++
    session.value = null
    error.value = message
    loading.value = false
    for (const controller of requests) controller.abort()
    requests.clear()
    eventController?.abort()
    eventController = null
    if (reconnectTimer !== null) window.clearTimeout(reconnectTimer)
    if (refreshTimer !== null) window.clearTimeout(refreshTimer)
    reconnectTimer = null
    refreshTimer = null
    refreshRunning = false
    refreshDirty = false
    reconnectAttempt = 0
    eventsConnected.value = false
    overview.value = null
    sessions.value = []
    clients.value = []
    accounts.value = []
    clientDetail.value = null
    identities.value = []
    identityClaims.value = []
    operations.value = []
    auditPage.value = { items: [], next_cursor: null }
    currentAuditQuery = {}
    adminActions.value = []
  }

  onUnmounted(() => clearSession())

  return {
    authenticated, username, restoringSession: readonly(restoringSession), authBusy: readonly(authBusy),
    overview: readonly(overview), sessions: readonly(sessions), clients: readonly(clients),
    clientDetail: readonly(clientDetail), auditPage: readonly(auditPage), adminActions: readonly(adminActions),
    loading: readonly(loading), error: readonly(error), eventsConnected: readonly(eventsConnected),
    authenticate, restoreSession, logout, refreshAll, loadAudit, loadClientDetail, disconnectSession,
    updateClientNote, importPublicKey, blockClient, unblockClient, exportAudit,
    identities: readonly(identities), identityClaims: readonly(identityClaims), operations: readonly(operations),
    loadGovernance, findIdentity, bindIdentity, revokeIdentity, reconcileOperation,
    accounts: readonly(accounts), loadAccounts, loadAccountDevices, createAccount, updateAccount,
  }
}

export type AdminApi = ReturnType<typeof useAdminApi>
