const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const test = require('node:test')
const vm = require('node:vm')
const ts = require('typescript')
const vue = require('vue')

function pager(fetchPage) {
  let dispose
  const context = vm.createContext({
    exports: {},
    require() { return { ...vue, onScopeDispose(fn) { dispose = fn } } },
  })
  const source = fs.readFileSync(path.join(__dirname, '../src/composables/useCursorPage.ts'), 'utf8')
  vm.runInContext(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText, context)
  return { ...context.exports.useCursorPage(fetchPage), dispose: () => dispose() }
}
const page = (id, next_cursor = null) => ({ items: [{ id }], next_cursor })
function deferred() { let resolve; const promise = new Promise(done => { resolve = done }); return { promise, resolve } }

test('independent histories paginate with server cursors and return to the first page', async () => {
  const calls = []
  const a = pager(async cursor => { calls.push(cursor); return cursor ? page('older') : page('latest', 'older-cursor') })
  const b = pager(async () => page('other'))
  await Promise.all([a.reset(), b.reset()])
  assert.equal(a.hasPrevious.value, false)
  await a.next()
  assert.equal(a.pageNumber.value, 2)
  assert.equal(a.page.value.items[0].id, 'older')
  assert.equal(a.hasNext.value, false)
  assert.equal(b.pageNumber.value, 1)
  await a.previous()
  assert.equal(a.pageNumber.value, 1)
  assert.deepEqual(calls, [undefined, 'older-cursor', undefined])
})

test('failed navigation preserves page and cursor history; retry requests the failed page', async () => {
  let fails = true
  const calls = []
  const p = pager(async cursor => {
    calls.push(cursor)
    if (cursor && fails) throw new Error('network failed')
    return cursor ? page('older') : page('latest', 'next')
  })
  await p.reset()
  await p.next()
  assert.equal(p.pageNumber.value, 1)
  assert.equal(p.page.value.items[0].id, 'latest')
  assert.ok(p.error.value)
  fails = false
  await p.retry()
  assert.equal(p.pageNumber.value, 2)
  assert.equal(p.page.value.items[0].id, 'older')
  assert.deepEqual(calls, [undefined, 'next', 'next'])
  await p.refresh()
  assert.equal(p.pageNumber.value, 2)
  assert.equal(calls.at(-1), 'next')
})

test('switching client rejects late responses and resets pagination without duplicate navigation', async () => {
  const late = deferred()
  let switched = false
  let nextCalls = 0
  const p = pager(async cursor => {
    if (switched) return page('new-client')
    if (cursor) { nextCalls++; return late.promise }
    return page('old-client', 'next')
  })
  await p.reset()
  const pending = p.next()
  await p.next()
  assert.equal(nextCalls, 1)
  switched = true
  await p.reset()
  late.resolve(page('stale-client'))
  await pending
  assert.equal(p.pageNumber.value, 1)
  assert.equal(p.page.value.items[0].id, 'new-client')
  assert.equal(p.hasPrevious.value, false)
})

test('empty pages have no navigation and disposed panels ignore pending responses', async () => {
  const late = deferred()
  const p = pager(() => late.promise)
  const pending = p.reset()
  p.dispose()
  late.resolve(page('late'))
  await pending
  assert.equal(p.page.value.items.length, 0)
  const empty = pager(async () => ({ items: [], next_cursor: null }))
  await empty.reset()
  assert.equal(empty.hasPrevious.value, false)
  assert.equal(empty.hasNext.value, false)
})
