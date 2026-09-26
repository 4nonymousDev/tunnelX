const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')
const vm = require('node:vm')
const ts = require('typescript')
const vue = require('vue')

const sourceRoot = path.join(__dirname, '../src')
const session = { username: 'admin', csrf_token: 'csrf-from-server', expires_at: '2030-01-01T00:00:00Z' }
const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
const settle = () => new Promise(resolve => setImmediate(resolve))
function deferred() { let resolve; const promise = new Promise(done => { resolve = done }); return { promise, resolve } }

function harness() {
  const calls = [], timers = new Map(), cleanup = [], downloads = []
  let nextTimer = 0
  let route = () => undefined
  const browser = {
    setTimeout(fn, delay) { const id = ++nextTimer; timers.set(id, { fn, delay }); return id },
    clearTimeout(id) { timers.delete(id) },
  }
  const forbiddenStorage = new Proxy({}, { get() { throw new Error('Authentication must not use browser storage') } })
  const context = vm.createContext({
    exports: {},
    require(name) { if (name === 'vue') return { ...vue, onUnmounted(fn) { cleanup.push(fn) } }; throw new Error(name) },
    window: browser, localStorage: forbiddenStorage, sessionStorage: forbiddenStorage,
    document: { hidden: false, createElement() { return { click() { downloads.push(this.download) } } } },
    Headers, AbortController, TextDecoder, TextEncoder, URLSearchParams, Blob,
    URL: { createObjectURL() { return 'blob:test' }, revokeObjectURL() {} },
    fetch: async (url, init = {}) => {
      const call = { url, init }; calls.push(call)
      const custom = route(call)
      if (custom !== undefined) return custom
      if (url.endsWith('/auth/login') || url.endsWith('/auth/session')) return json(session)
      if (url.endsWith('/auth/logout')) return new Response(null, { status: 204 })
      if (url.endsWith('/events')) {
        return new Response(new ReadableStream({ start(controller) {
          init.signal.addEventListener('abort', () => controller.error(new Error('aborted')), { once: true })
        } }), { headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (url.includes('format=csv')) return new Response('time,result\nnow,ok\n')
      if (url.endsWith('/overview')) return json({ online_users: 2 })
      return json({ items: [{ username: 'ordinary-user' }] })
    },
  })
  const compiled = ts.transpileModule(fs.readFileSync(path.join(sourceRoot, 'composables/useAdminApi.ts'), 'utf8'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  })
  vm.runInContext(compiled.outputText, context)
  const api = context.exports.useAdminApi()
  return { api, calls, timers, downloads, route(value) { route = value }, close() { cleanup.forEach(fn => fn()) } }
}

test('page refresh restores a cookie session and all reads/SSE/export use same-origin credentials', async () => {
  const h = harness()
  try {
    await h.api.restoreSession()
    assert.equal(h.api.authenticated.value, true)
    assert.equal(h.api.username.value, 'admin')
    assert.equal(h.api.restoringSession.value, false)
    assert.equal(h.calls[0].url, '/api/v1/auth/session')
    await h.api.exportAudit()
    assert.equal(h.downloads.length, 1)
    assert.ok(h.calls.some(call => call.url === '/api/v1/events'))
    for (const { init } of h.calls) {
      assert.equal(init.credentials, 'same-origin')
      assert.equal(new Headers(init.headers).has('Authorization'), false)
    }
  } finally { h.close() }
})

test('login preserves password whitespace and every mutation including logout sends memory CSRF', async () => {
  const h = harness()
  try {
    const secret = '  a long password with spaces  '
    await h.api.authenticate(' admin ', secret)
    const login = h.calls.find(call => call.url.endsWith('/auth/login'))
    assert.deepEqual(JSON.parse(login.init.body), { username: 'admin', password: secret })
    assert.equal(new Headers(login.init.headers).has('X-CSRF-Token'), false)
    await h.api.createAccount({ username: 'test-user', password: secret, max_devices: 10, is_admin: true, reason: 'test' })
    await h.api.updateAccount('test-user', { is_admin: false, expected_generation: 1, reason: 'test' })
    await h.api.logout()
    for (const call of h.calls.filter(call => ['POST', 'PATCH'].includes(call.init.method) && !call.url.endsWith('/auth/login'))) {
      assert.equal(new Headers(call.init.headers).get('X-CSRF-Token'), session.csrf_token)
    }
    assert.equal(h.api.authenticated.value, false)
    assert.equal(h.api.accounts.value.length, 0)
    assert.equal(h.timers.size, 0)
  } finally { h.close() }
})

test('update policy saves explicit deadline with CSRF and exposes stale-generation conflicts', async () => {
  const h = harness()
  try {
    await h.api.restoreSession()
    const policy = { minimum_version: '0.2.1', enforce_after: '2026-10-04T12:00:00Z', message: 'Security update', generation: 2 }
    h.route(({ url }) => url.endsWith('/client-update-policy') ? json(policy) : undefined)
    assert.equal((await h.api.getClientUpdatePolicy()).generation, 2)
    const body = { ...policy, expected_generation: 2, reason: 'test' }
    delete body.generation
    await h.api.setClientUpdatePolicy(body)
    const saved = h.calls.find(call => call.init.method === 'PUT')
    assert.deepEqual(JSON.parse(saved.init.body), body)
    assert.equal(new Headers(saved.init.headers).get('X-CSRF-Token'), session.csrf_token)
    assert.equal(saved.init.credentials, 'same-origin')
    h.route(({ url }) => url.endsWith('/client-update-policy') ? json({}, 409) : undefined)
    await assert.rejects(h.api.setClientUpdatePolicy(body), /刷新/)
  } finally { h.close() }
})

test('401 clears data, cancels SSE and rejects stale work without repopulating a new login', async () => {
  const h = harness()
  try {
    await h.api.restoreSession()
    const stale = deferred()
    h.route(({ url }) => url === '/api/v1/accounts' ? stale.promise : url.includes('/audit/events') ? json({}, 401) : undefined)
    const oldRequest = h.api.loadAccounts()
    const oldResult = assert.rejects(oldRequest, /会话已结束/)
    await assert.rejects(h.api.loadAudit(), /会话已结束/)
    assert.equal(h.api.authenticated.value, false)
    assert.equal(h.api.accounts.value.length, 0)
    assert.equal(h.api.overview.value, null)
    assert.equal(h.api.eventsConnected.value, false)
    assert.ok(h.calls.find(call => call.url.endsWith('/events')).init.signal.aborted)
    h.route(() => undefined)
    await h.api.authenticate('admin', 'valid synthetic password')
    stale.resolve(json({ items: [{ username: 'must-never-appear' }] }))
    await oldResult
    assert.equal(h.api.accounts.value[0].username, 'ordinary-user')
    assert.equal(h.api.error.value, '')
  } finally { h.close() }
})

test('logout waits for server revocation and a failed logout keeps the session visible', async () => {
  const h = harness()
  try {
    await h.api.restoreSession()
    h.route(({ url }) => url.endsWith('/auth/logout') ? json({}, 503) : undefined)
    await assert.rejects(h.api.logout(), /繁忙/)
    assert.equal(h.api.authenticated.value, true)
    const response = deferred()
    h.route(({ url }) => url.endsWith('/auth/logout') ? response.promise : undefined)
    const logout = h.api.logout()
    await settle()
    assert.equal(h.api.authenticated.value, true)
    response.resolve(new Response(null, { status: 204 }))
    await logout
    assert.equal(h.api.authenticated.value, false)
    assert.equal(h.api.authBusy.value, false)
  } finally { h.close() }
})

test('expired SSE stops reconnecting and export 401 also clears the session', async () => {
  for (const endpoint of ['/events', 'format=csv']) {
    const h = harness()
    try {
      h.route(({ url }) => url.includes(endpoint) ? json({}, 401) : undefined)
      await h.api.restoreSession()
      if (endpoint === 'format=csv') await assert.rejects(h.api.exportAudit(), /会话已结束/)
      await settle()
      assert.equal(h.api.authenticated.value, false)
      assert.equal(h.api.eventsConnected.value, false)
      assert.equal(h.timers.size, 0)
    } finally { h.close() }
  }
})

test('failed login never exposes a server response that echoes a password', async () => {
  for (const status of [401, 403, 500, 200]) {
    const h = harness()
    try {
      const secret = 'sensitive synthetic password'
      h.route(({ url }) => url.endsWith('/auth/login') ? new Response(`invalid JSON ${secret}`, { status }) : undefined)
      await assert.rejects(h.api.authenticate('admin', secret))
      assert.equal(h.api.authenticated.value, false)
      assert.equal(h.api.error.value.includes(secret), false)
      assert.equal(h.api.authBusy.value, false)
    } finally { h.close() }
  }
})

test('absent session goes to login without showing an error or exposing application data', async () => {
  const h = harness()
  try {
    h.route(({ url }) => url.endsWith('/auth/session') ? json({}, 401) : undefined)
    await h.api.restoreSession()
    assert.equal(h.api.restoringSession.value, false)
    assert.equal(h.api.authenticated.value, false)
    assert.equal(h.api.error.value, '')
    assert.equal(h.calls.length, 1)
  } finally { h.close() }
})

function componentScript(filename, props, extras = {}) {
  const source = fs.readFileSync(path.join(sourceRoot, filename), 'utf8').match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const emitted = []
  const context = vm.createContext({
    exports: {}, TextEncoder,
    require(name) {
      if (name === 'vue') return { ...vue, onUnmounted() {}, onMounted() {}, watch() {} }
      if (name.includes('useAdminApi')) return { useAdminApi: () => extras.api }
      if (name.endsWith('.vue')) return {}
      if (name.endsWith('/utils/format')) return { formatDate: value => value }
      throw new Error(name)
    },
    defineProps: () => props,
    defineEmits: () => (...args) => emitted.push(args),
  })
  vm.runInContext(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText, context)
  return { emitted, run(code) { return vm.runInContext(code, context) } }
}

test('login form erases its password before the request settles, preserving spaces', async () => {
  const response = deferred(), logins = []
  const c = componentScript('App.vue', {}, { api: {
    authBusy: { value: false }, authenticated: { value: false },
    authenticate(username, password) { logins.push({ username, password }); return response.promise },
  } })
  c.run("usernameInput.value = 'admin'; passwordInput.value = '  secret password  '")
  const pending = c.run('login()')
  assert.equal(c.run('passwordInput.value'), '')
  assert.deepEqual(logins, [{ username: 'admin', password: '  secret password  ' }])
  response.resolve()
  await pending
})

test('account role controls submit explicit privilege changes with the expected generation', () => {
  const account = { username: 'alice', generation: 4, is_admin: false }
  const c = componentScript('components/AccountDialog.vue', { mode: 'role', account, busy: false })
  c.run("reason.value = 'Grant trusted operator'; submit()")
  assert.deepEqual(JSON.parse(JSON.stringify(c.emitted)), [['update', 'alice', {
    expected_generation: 4, reason: 'Grant trusted operator', is_admin: true,
  }]])
  assert.match(c.run('description.value'), /管理所有账号/)
  const create = componentScript('components/AccountDialog.vue', { mode: 'create', busy: false })
  create.run("username.value = 'new-admin'; password.value = '  synthetic password  '; isAdmin.value = true; reason.value = 'Create trusted operator'; submit()")
  assert.equal(create.run('password.value'), '')
  assert.equal(create.emitted[0][1].password, '  synthetic password  ')
  assert.equal(create.emitted[0][1].is_admin, true)
})

test('policy form starts with seven days, preserves deadlines and sends a clean cancellation', async () => {
  const saved = []
  const c = componentScript('views/UpdatesView.vue', { api: {
    async setClientUpdatePolicy(body) { saved.push(JSON.parse(JSON.stringify(body))); return { ...body, generation: body.expected_generation + 1 } },
  } })
  c.run("apply({minimum_version:'', enforce_after:null, message:'', generation:1}); enabled.value=true; setDefaultDeadline()")
  const remaining = c.run('new Date(deadline.value).getTime() - Date.now()')
  assert.ok(remaining > 7 * 86400000 - 61000 && remaining <= 7 * 86400000)
  c.run("minimum.value='0.2.1'; message.value='Please update'; reason.value='Schedule security update'")
  const expectedDeadline = c.run('new Date(deadline.value).toISOString()')
  await c.run('save()')
  assert.equal(saved[0].enforce_after, expectedDeadline)
  assert.equal(saved[0].expected_generation, 1)
  c.run("enabled.value=false; reason.value='Cancel update'")
  await c.run('save()')
  assert.deepEqual(saved[1], { minimum_version: '', enforce_after: null, message: '', expected_generation: 2, reason: 'Cancel update' })
})
