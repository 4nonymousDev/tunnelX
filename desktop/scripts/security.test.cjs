const assert = require('node:assert/strict')
const { mkdtemp, readFile, writeFile, rm } = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')
const { test } = require('node:test')
const { InterfaceLockManager } = require('../dist-electron/main/lock-manager')
const Module = require('node:module')
const { validateSignatureResult, verifyUpdateSignature } = require('../dist-electron/main/update-signature')

test('updates require a valid signature from the complete pinned publisher', async () => {
  const trusted = ['CN=TunnelX Test, O=Example']
  assert.equal(validateSignatureResult(trusted, { Status: 0, Subject: trusted[0] }), null)
  assert.ok(validateSignatureResult(trusted, { Status: 1, Subject: trusted[0] }))
  assert.ok(validateSignatureResult(trusted, { Status: 0, Subject: 'CN=Other' }))
  assert.ok(validateSignatureResult(trusted, { Status: 0, Subject: 'TunnelX Test' }))
  assert.ok(validateSignatureResult(trusted, undefined))
  assert.ok(await verifyUpdateSignature(trusted, path.join(os.tmpdir(), 'tunnelx-no-such-update.exe')))
})

test('existing lock cannot be replaced, and survives restart', async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'tunnelx-lock-'))
  try {
    const file = path.join(dir, 'lock.json')
    const lock = new InterfaceLockManager(file)
    assert.throws(() => lock.assertUnlocked())
    await lock.initialize()
    await lock.lock('original-password')
    const before = await readFile(file, 'utf8')
    assert.throws(() => lock.assertUnlocked())
    await assert.rejects(lock.lock('attacker-password'))
    assert.equal(await readFile(file, 'utf8'), before)
    const restarted = new InterfaceLockManager(file)
    await restarted.initialize()
    assert.equal(restarted.getState().locked, true)
    await restarted.unlock('original-password')
    restarted.assertUnlocked()
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('invalid stored lock fails closed; concurrent lock cannot overwrite', async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'tunnelx-lock-'))
  try {
    const file = path.join(dir, 'lock.json')
    await writeFile(file, '{broken')
    const broken = new InterfaceLockManager(file)
    await assert.rejects(broken.initialize())
    assert.throws(() => broken.assertUnlocked())
    await rm(file)
    const lock = new InterfaceLockManager(file)
    await lock.initialize()
    const first = lock.lock('original-password')
    await assert.rejects(lock.lock('replacement-password'))
    await first
    await lock.unlock('original-password')
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('IPC refuses locked operations and non-main-frame callers', async () => {
  const handlers = new Map()
  const originalLoad = Module._load
  let registerIpc
  Module._load = function (id, ...args) {
    if (id === 'electron') return { ipcMain: { handle: (name, fn) => handlers.set(name, fn) }, clipboard: {}, dialog: {} }
    return originalLoad.call(this, id, ...args)
  }
  try { ({ registerIpc } = require('../dist-electron/main/ipc')) } finally { Module._load = originalLoad }
  let locked = true
  let connects = 0
  const sender = { mainFrame: {} }
  const event = { sender, senderFrame: sender.mainFrame }
  registerIpc({ connect() { connects++ } }, {}, {
    assertUnlocked() { if (locked) throw new Error('locked') },
    getState() { return { locked } },
  }, () => ({ webContents: sender }))
  assert.throws(() => handlers.get('tunnelx:connect')(event), /locked/)
  assert.equal(handlers.get('tunnelx:lock:get-state')(event).locked, true)
  locked = false
  assert.throws(() => handlers.get('tunnelx:connect')({ sender, senderFrame: {} }), /请求来源/)
  handlers.get('tunnelx:connect')(event)
  assert.equal(connects, 1)
  assert.equal(handlers.has('tunnelx:login'), false)
})
