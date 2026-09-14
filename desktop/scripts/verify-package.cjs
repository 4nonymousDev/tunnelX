const fs = require('node:fs')
const path = require('node:path')

const { listPackage } = require('@electron/asar')

const sensitiveName = /^(?:\.env(?:\..*)?|config\.json|tunnel_key(?:\.pub)?|host_key(?:\.pub)?|known_hosts|authorized_keys|\.tunnelx-control\.json|tunnelx\.log(?:\..*)?|.*\.(?:pem|pfx|p12|jks|keystore|key))$/i
const sensitiveContent = /(?:BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY|github_pat_[A-Za-z0-9_]{20,}|ghp_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|[A-Za-z]:\\Users\\[^\\\s"']+)/

module.exports = async function verifyPackage(context) {
  const resources = path.join(context.appOutDir, 'resources')
  const archive = path.join(resources, 'app.asar')
  const entries = listPackage(archive, {})
  const failures = []

  for (const entry of entries) {
    const normalized = entry.replaceAll('\\', '/').replace(/^\//, '')
    const base = path.posix.basename(normalized)
    if (sensitiveName.test(base)) failures.push(`敏感文件名进入 app.asar: ${normalized}`)

  }

  const projectDir = context.packager.projectDir
  for (const directory of [path.join(projectDir, 'dist'), path.join(projectDir, 'dist-electron')]) {
    for (const file of walk(directory)) {
      if (!/\.(?:js|css|html|json)$/i.test(file)) continue
      const source = fs.readFileSync(file, 'utf8')
      const match = source.match(sensitiveContent)
      if (match) failures.push(`应用产物包含疑似本机路径或凭据: ${path.relative(projectDir, file)} (${match[0].slice(0, 80)})`)
    }
  }

  for (const file of walk(resources)) {
    if (file === archive) continue
    if (sensitiveName.test(path.basename(file))) {
      failures.push(`敏感文件进入 resources: ${path.relative(resources, file)}`)
    }
  }

  if (failures.length) throw new Error(`安装包隐私检查失败:\n${failures.join('\n')}`)
  console.log(`安装包隐私检查通过（${entries.length} 个 app.asar 条目）`)
}

function walk(directory) {
  const files = []
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const target = path.join(directory, entry.name)
    if (entry.isDirectory()) files.push(...walk(target))
    else files.push(target)
  }
  return files
}
