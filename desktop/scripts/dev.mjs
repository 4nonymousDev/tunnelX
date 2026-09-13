import { spawn } from 'node:child_process'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

const npmCommand = process.platform === 'win32' ? 'npm.cmd' : 'npm'
const electronCommand = process.platform === 'win32'
  ? 'node_modules\\.bin\\electron.cmd'
  : 'node_modules/.bin/electron'
const projectRoot = fileURLToPath(new URL('../..', import.meta.url))
const corePath = path.join(projectRoot, 'tunnelx-cli.exe')

function run(command, args, options = {}) {
  return spawn(command, args, { stdio: 'inherit', ...options })
}

async function requireSuccess(child, label) {
  const code = await new Promise((resolve, reject) => {
    child.once('error', reject)
    child.once('exit', resolve)
  })
  if (code !== 0) throw new Error(`${label}失败，退出码 ${code ?? 'unknown'}`)
}

const coreCompiler = run('go', [
  'build',
  '-ldflags', '-X main.Version=dev',
  '-o', corePath,
  './cmd/tunnelx-cli',
], { cwd: projectRoot })
await requireSuccess(coreCompiler, 'TunnelX 核心编译')

const compiler = run(npmCommand, ['run', 'build:electron'])
await requireSuccess(compiler, 'Electron 主进程编译')

const vite = run(npmCommand, ['run', 'dev:renderer'])

let ready = false
for (let attempt = 0; attempt < 60; attempt += 1) {
  try {
    const response = await fetch('http://127.0.0.1:5173')
    if (response.ok) {
      ready = true
      break
    }
  } catch {
    await delay(250)
  }
}

if (!ready) {
  vite.kill()
  throw new Error('Vite development server did not become ready')
}

const electron = run(electronCommand, ['.'], {
  env: {
    ...process.env,
    VITE_DEV_SERVER_URL: 'http://127.0.0.1:5173',
    TUNNELX_CORE_PATH: corePath,
    TUNNELX_CORE_STDIO: 'inherit',
  },
})

const shutdown = () => {
  electron.kill()
  vite.kill()
}

process.once('SIGINT', shutdown)
process.once('SIGTERM', shutdown)
electron.once('exit', code => {
  vite.kill()
  process.exit(code ?? 0)
})
