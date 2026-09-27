const assert = require('node:assert/strict')
const { EventEmitter } = require('node:events')
const { readFileSync } = require('node:fs')
const { createRequire } = require('node:module')
const path = require('node:path')
const vm = require('node:vm')
const { test } = require('node:test')
const { NsisUpdater } = require('electron-updater/out/NsisUpdater')

function fixture({ packaged = true, platform = 'win32', downloadError } = {}) {
  const calls = []
  const updater = new EventEmitter()
  updater.checkForUpdates = async () => {
    calls.push('check')
    updater.emit('checking-for-update')
    updater.emit('update-available', { version: '0.2.5' })
  }
  updater.downloadUpdate = async () => {
    calls.push('download')
    if (downloadError) throw new Error(downloadError)
    updater.emit('update-downloaded', { version: '0.2.5' })
  }
  updater.quitAndInstall = (...args) => calls.push(['install', ...args])
  const filename = path.resolve(__dirname, '../dist-electron/main/update-manager.js')
  const realRequire = createRequire(filename)
  const exports = {}
  const timers = []
  vm.runInNewContext(readFileSync(filename, 'utf8'), {
    exports, console, process: { platform },
    require(id) {
      if (id === 'electron') return { app: { isPackaged: packaged, getVersion: () => '0.2.4' } }
      if (id === 'electron-updater') return { autoUpdater: updater }
      return realRequire(id)
    },
    setTimeout(fn, delay) { timers.push({ fn, delay }); return { unref() {} } },
    setInterval(fn, delay) { timers.push({ fn, delay }); return { unref() {} } },
    clearTimeout() {}, clearInterval() {},
  }, { filename })
  const manager = new exports.UpdateManager(async () => { calls.push('prepare') })
  return { manager, updater, calls, timers }
}

test('unsigned Windows releases check, download and install after confirmation', async () => {
  const { manager, updater, calls, timers } = fixture()
  assert.equal(manager.getState().phase, 'idle')
  assert.equal(updater.autoDownload, false)
  assert.equal(updater.autoInstallOnAppQuit, false)
  manager.start()
  assert.deepEqual(timers.map(timer => timer.delay), [15_000, 4 * 60 * 60 * 1000])
  assert.equal((await manager.checkForUpdates()).phase, 'available')
  assert.deepEqual(calls, ['check'])
  await assert.rejects(manager.installUpdate(), /尚未下载完成/)
  assert.equal((await manager.downloadUpdate()).phase, 'downloaded')
  assert.deepEqual(calls, ['check', 'download'])
  await manager.installUpdate()
  assert.deepEqual(calls, ['check', 'download', 'prepare', ['install', false, true]])
})

test('failed download validation cannot reach the installer', async () => {
  const { manager, calls } = fixture({ downloadError: 'sha512 checksum mismatch' })
  await manager.checkForUpdates()
  assert.equal((await manager.downloadUpdate()).phase, 'error')
  assert.match(manager.getState().message, /checksum mismatch/)
  await assert.rejects(manager.installUpdate(), /尚未下载完成/)
  assert.deepEqual(calls, ['check', 'download'])
})

test('development and unsupported platforms do not contact the update service', async () => {
  for (const options of [{ packaged: false }, { platform: 'linux' }]) {
    const { manager, calls, timers } = fixture(options)
    manager.start()
    assert.equal((await manager.checkForUpdates()).phase, 'unsupported')
    assert.equal((await manager.downloadUpdate()).phase, 'unsupported')
    await assert.rejects(manager.installUpdate(), /仅 Windows/)
    assert.deepEqual(calls, [])
    assert.deepEqual(timers, [])
  }
})

test('real NSIS updater permits unsigned config and enforces a configured publisher', async () => {
  const config = {}
  const calls = []
  const updater = {
    configOnDisk: { value: Promise.resolve(config) },
    _verifyUpdateCodeSignature: async (...args) => { calls.push(args); return 'untrusted signature' },
  }
  assert.equal(await NsisUpdater.prototype.verifySignature.call(updater, 'update.exe'), null)
  assert.deepEqual(calls, [])
  config.publisherName = ['CN=TunnelX Test, O=Example']
  assert.equal(await NsisUpdater.prototype.verifySignature.call(updater, 'update.exe'), 'untrusted signature')
  assert.deepEqual(calls, [[config.publisherName, 'update.exe']])
  assert.equal(require('../package.json').build.win.verifyUpdateCodeSignature, false)
})
