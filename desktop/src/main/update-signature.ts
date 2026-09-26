import { execFile } from 'node:child_process'
import path from 'node:path'
import { promisify } from 'node:util'

const run = promisify(execFile)

// The upstream verifier permits skipping verification when PowerShell is not
// available. Updates must fail closed on every execution or parsing failure.
export async function verifyUpdateSignature(publishers: string[], file: string): Promise<string | null> {
  if (!publishers.length) return '未配置更新签名发布者'
  const encodedPath = Buffer.from(file, 'utf16le').toString('base64')
  const script = `$ErrorActionPreference='Stop'; [Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $p=[Text.Encoding]::Unicode.GetString([Convert]::FromBase64String('${encodedPath}')); $s=Get-AuthenticodeSignature -LiteralPath $p; [pscustomobject]@{Status=[int]$s.Status; Subject=$s.SignerCertificate.Subject} | ConvertTo-Json -Compress`
  try {
    const { stdout } = await run(path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe'), [
      '-NoLogo', '-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(script, 'utf16le').toString('base64'),
    ], { timeout: 30_000, windowsHide: true, maxBuffer: 64 * 1024, encoding: 'utf8' })
    return validateSignatureResult(publishers, JSON.parse(stdout.replace(/^\uFEFF/, '')) as unknown)
  } catch {
    return '无法验证更新的 Windows 签名，已拒绝安装'
  }
}

export function validateSignatureResult(publishers: string[], value: unknown): string | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return '更新签名结果无效'
  const signature = value as { Status?: unknown; Subject?: unknown }
  if (signature.Status !== 0 || typeof signature.Subject !== 'string') return '更新缺少有效的 Windows 签名'
  if (!publishers.some(publisher => publisher.length > 0 && publisher === signature.Subject)) return '更新签名发布者与受信任发布者不符'
  return null
}
