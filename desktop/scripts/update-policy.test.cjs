const assert = require('node:assert/strict')
const { readFileSync } = require('node:fs')
const path = require('node:path')
const { test } = require('node:test')
const vm = require('node:vm')
const ts = require('typescript')
const vue = require('vue')

test('required updates prompt once per change, remain visible after dismissal and never install silently', async () => {
  let checks = 0
  const snapshot = vue.shallowRef({ server_addr: 'example.invalid:2222' })
  const updateState = vue.shallowRef({ phase: 'unsupported' })
  const api = { snapshot, updateState, async checkForUpdates() { checks++; return updateState.value } }
  const context = vm.createContext({
    exports: {},
    require(name) {
      if (name === 'vue') return { ...vue, onUnmounted() {} }
      if (name.includes('useTunnelX')) return { useTunnelX: () => api }
      if (name.endsWith('.vue') || name.startsWith('../utils/')) return {}
      throw new Error(name)
    },
    defineEmits: () => () => {},
  })
  const script = readFileSync(path.join(__dirname, '../src/renderer/components/TunnelWorkspace.vue'), 'utf8').match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const scope = vue.effectScope()
  scope.run(() => vm.runInContext(ts.transpileModule(script, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText, context))
  const run = code => vm.runInContext(code, context)
  try {
    assert.equal(checks, 0)
    const policy = { minimum_version: '0.2.1', enforce_after: '2030-01-01T00:00:00Z', generation: 2, required: true, blocked: false }
    snapshot.value = { ...snapshot.value, update_policy: policy }
    await vue.nextTick()
    assert.equal(checks, 1)
    assert.equal(run('updateDialogOpen.value'), true)
    run('updateDialogOpen.value = false')
    snapshot.value = { ...snapshot.value, update_policy: { ...policy } }
    await vue.nextTick()
    assert.equal(run('updateDialogOpen.value'), false)
    assert.equal(run('updateRequirement.value.required'), true)
    assert.equal(checks, 1)
    snapshot.value = { ...snapshot.value, update_policy: { ...policy, blocked: true } }
    await vue.nextTick()
    assert.equal(run('updateDialogOpen.value'), true)
    assert.equal(run('updateRequirement.value.blocked'), true)
    assert.equal(checks, 2)
    snapshot.value = { ...snapshot.value, update_policy: undefined }
    await vue.nextTick()
    assert.equal(run('updateRequirement.value'), undefined)
    // A policy change during a download must not start another update operation.
    updateState.value = { phase: 'downloading' }
    snapshot.value = { ...snapshot.value, update_policy: { ...policy, generation: 3 } }
    await vue.nextTick()
    assert.equal(checks, 2)
  } finally { scope.stop() }
})
