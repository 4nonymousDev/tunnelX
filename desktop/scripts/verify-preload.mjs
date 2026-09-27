import { readFile } from 'node:fs/promises'

const preloadUrl = new URL('../dist-electron/preload/index.js', import.meta.url)
const source = await readFile(preloadUrl, 'utf8')
const runtimeRequires = [...source.matchAll(/require\(["']([^"']+)["']\)/g)]
  .map(match => match[1])
const unexpectedRequires = runtimeRequires.filter(id => id !== 'electron')

if (unexpectedRequires.length > 0) {
  throw new Error(`sandbox preload 包含不可用的运行时依赖: ${unexpectedRequires.join(', ')}`)
}

for (const marker of [
  'exposeInMainWorld',
  'updateSettings',
  'tunnelx:settings:update',
  'generateKey',
  'tunnelx:key:generate',
  'checkForUpdates',
  'tunnelx:update:check',
  'downloadUpdate',
  'tunnelx:update:download',
  'installUpdate',
  'tunnelx:update:install',
]) {
  if (!source.includes(marker)) {
    throw new Error(`sandbox preload 缺少必需标记: ${marker}`)
  }
}

console.log('sandbox preload verification passed')
