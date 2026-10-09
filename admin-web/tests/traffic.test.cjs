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

test('fan-out nodes sum edges and importers aggregate multiple exporters', () => {
  const links = [link('e', 'i1', 200), link('e', 'i2', 300)]
  const exporters = utils.trafficNodes(links, 'exporter')
  const importers = utils.trafficNodes(links, 'importer')
  assert.equal(exporters[0].rate, 500)
  assert.equal(importers[0].rate, 300)
  assert.equal(exporters[0].connections, 2)
  const multi = utils.trafficNodes([...links, link('e2', 'i1', 400)], 'importer')
  assert.equal(multi[0].rate, 600)
})

test('traffic formatting handles idle, fractional and large transfers', () => {
  assert.equal(utils.formatRate(1048576), '1 MiB/s')
  assert.equal(utils.formatBytes(0), '0 B')
  assert.equal(utils.formatRate(.5), '0.5 B/s')
})

test('Sankey bands conserve node throughput and share one scale at both ends', () => {
  const links = [link('e1', 'i1', 200), link('e1', 'i2', 300), link('e2', 'i2', 100)]
  const graph = utils.trafficSankey(links)
  assert.ok(graph.scale > 0)
  assert.equal(graph.edges.length, 3)
  for (const [nodes, side, offset] of [[graph.exporters, 'exporter', 'sourceY'], [graph.importers, 'importer', 'targetY']]) {
    for (const node of nodes) {
      const bands = graph.edges.filter(edge => edge[side].id === node.id)
      assert.ok(Math.abs(bands.reduce((sum, edge) => sum + edge.width, 0) - node.height) < 1e-8)
      assert.ok(Math.abs(node.height - node.rate * graph.scale) < 1e-8)
      let end = node.y
      for (const edge of bands) {
        assert.ok(Math.abs(edge[offset] - end) < 1e-8)
        end += edge.width
      }
      assert.ok(end <= node.y + node.height + 1e-8)
    }
  }
  assert.ok(Math.abs(graph.edges[0].width / graph.edges[1].width - 2 / 3) < 1e-8)
})

test('Sankey includes all connections and leaves room for labels, including idle nodes', () => {
  for (const rate of [0, .02, 100000000]) {
    const links = Array.from({ length: 40 }, (_, i) => link('e', `i${i}`, rate))
    const graph = utils.trafficSankey(links)
    assert.equal(graph.edges.length, 40)
    assert.equal(graph.importers.length, 40)
    for (const nodes of [graph.exporters, graph.importers]) {
      for (let i = 0; i < nodes.length; i++) {
        assert.ok(nodes[i].y >= 64)
        assert.ok(nodes[i].y + nodes[i].height <= graph.height - 36 + 1e-8)
        if (i) assert.ok(nodes[i].labelY - nodes[i - 1].labelY >= 32)
      }
    }
    if (!rate) for (const edge of graph.edges) assert.equal(edge.width, 0)
    assert.ok(graph.edges.every(edge => !/NaN|Infinity/.test(edge.path)))
  }
  assert.equal(utils.trafficSankey([]).edges.length, 0)
})

test('Sankey filters conserve visible flow and distinguish the same device on both sides', () => {
  const links = [link('same', 'same', 150), link('other', 'same', 250)]
  const filtered = utils.trafficSankey(links.filter(edge => edge.exporter.id === 'same'))
  assert.equal(filtered.exporters[0].rate, 150)
  assert.equal(filtered.importers[0].rate, 150)
  assert.notEqual(filtered.exporters[0].x, filtered.importers[0].x)
  const reversed = utils.trafficSankey([...links].reverse())
  assert.equal(JSON.stringify(reversed), JSON.stringify(utils.trafficSankey(links)))
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
