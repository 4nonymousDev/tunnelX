const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')
const vm = require('node:vm')
const ts = require('typescript')
const vue = require('vue')

function load(file, globals = {}) {
  const context = vm.createContext({ exports: {}, ...globals })
  const source = fs.readFileSync(path.join(__dirname, '../src', file), 'utf8')
  vm.runInContext(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText, context)
  return context.exports
}
const utils = load('utils/traffic.ts')
const device = id => ({ id, name: id })
const link = (e, i, rate) => ({ exporter: device(e), importer: device(i), bytes_per_second: rate, connections: 1 })

test('fan-out nodes sum edges and each side has an independent scale', () => {
  const links = [link('e', 'i1', 200), link('e', 'i2', 300)]
  const exporters = utils.trafficNodes(links, 'exporter')
  const importers = utils.trafficNodes(links, 'importer')
  assert.equal(exporters[0].rate, 500)
  assert.equal(importers[0].rate, 300)
  assert.equal(exporters[0].connections, 2)
  assert.notEqual(utils.trafficScale(500).max, utils.trafficScale(300).max)
  const multi = utils.trafficNodes([...links, link('e2', 'i1', 400)], 'importer')
  assert.equal(multi[0].rate, 600)
})

test('scales include zero and their maximum, including idle and large transfers', () => {
  for (const value of [0, .02, 1, 20, 123456789]) {
    const scale = utils.trafficScale(value)
    assert.equal(scale.ticks[0], 0)
    assert.equal(scale.ticks.at(-1), scale.max)
    assert.ok(scale.max >= value)
    assert.ok(scale.ticks.length <= 6)
  }
  assert.equal(utils.formatRate(1048576), '1 MiB/s')
  assert.equal(utils.formatBytes(0), '0 B')
})

test('equal-bandwidth nodes preserve exact Y values without overlapping labels', () => {
  for (const rate of [0, 100]) {
    const nodes = Array.from({ length: 16 }, (_, i) => ({ ...device(`n${i}`), rate, connections: 1 }))
    const positioned = utils.positionNodes(nodes, 100, 652, true)
    assert.equal(new Set(positioned.map(n => n.y)).size, 1)
    assert.equal(new Set(positioned.map(n => n.x)).size, 16)
    assert.ok(positioned[0].labelY >= 66)
    assert.ok(positioned.at(-1).labelY <= 590)
    for (let i = 1; i < positioned.length; i++) assert.ok(positioned[i].labelY - positioned[i - 1].labelY >= 32)
  }
})

function harness(fetchSnapshot) {
  let mount, unmount, visible, nextTimer = 0
  const timers = new Map()
  const document = { hidden: false, addEventListener(_, fn) { visible = fn }, removeEventListener() { visible = undefined } }
  const exports = load('composables/useTraffic.ts', {
    require() { return { ...vue, onMounted(fn) { mount = fn }, onUnmounted(fn) { unmount = fn } } },
    document, Date, Error,
    window: { setTimeout(fn) { const id = ++nextTimer; timers.set(id, fn); return id }, clearTimeout(id) { timers.delete(id) } },
  })
  const meter = exports.useTraffic(fetchSnapshot)
  return { ...meter, mount, unmount, timers, document, visible: () => visible?.() }
}
const snapshot = () => ({ sampled_at: new Date().toISOString(), since: new Date().toISOString(), links: [], exporters: [], importers: [] })
const settle = () => new Promise(resolve => setImmediate(resolve))

test('polling skips hidden pages, avoids overlapping requests and discards results after unmount', async () => {
  let resolve, calls = 0
  const h = harness(() => { calls++; return new Promise(done => { resolve = done }) })
  h.mount()
  await h.refresh()
  assert.equal(calls, 1)
  resolve(snapshot())
  await settle()
  assert.ok(h.snapshot.value)
  h.document.hidden = true
  await h.refresh()
  assert.equal(calls, 1)
  h.document.hidden = false
  h.visible()
  assert.equal(calls, 2)
  const old = h.snapshot.value
  h.unmount()
  resolve({ ...snapshot(), exporters: [{ id: 'late', name: 'late', bytes: 1 }] })
  await settle()
  assert.equal(h.snapshot.value, old)
  assert.equal(h.timers.size, 0)
})

test('failed fetch retains the last snapshot as stale and a successful retry recovers', async () => {
  let fail = false
  const h = harness(async () => { if (fail) throw new Error('offline'); return snapshot() })
  await h.refresh()
  const old = h.snapshot.value
  fail = true
  await h.refresh()
  assert.equal(h.snapshot.value, old)
  assert.equal(h.stale.value, true)
  assert.equal(h.error.value, 'offline')
  fail = false
  await h.refresh()
  assert.equal(h.stale.value, false)
  h.unmount()
})
