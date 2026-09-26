import { randomBytes, scryptSync, timingSafeEqual } from 'node:crypto'
import { mkdir, readFile, rename, unlink, writeFile } from 'node:fs/promises'
import path from 'node:path'

import type { InterfaceLockState } from '../shared/ipc'

const FILE_VERSION = 1
const KEY_LENGTH = 64
const MIN_PASSWORD_LENGTH = 4
const MAX_PASSWORD_LENGTH = 128

interface StoredLockState {
  version: number
  locked: boolean
  salt: string
  verifier: string
}

export class InterfaceLockManager {
  private state: StoredLockState | undefined
  private initialized = false
  private changing = false
  private failedAttempts = 0
  private nextAttemptAt = 0

  constructor(private readonly filePath: string) {}

  async initialize(): Promise<void> {
    try {
      const parsed = JSON.parse(await readFile(this.filePath, 'utf8')) as unknown
      this.state = storedState(parsed)
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw new Error('无法读取界面锁，请恢复锁文件后重试。')
      this.state = undefined
    }
    this.initialized = true
  }

  assertUnlocked(): void {
    if (!this.initialized || this.state?.locked || this.changing) throw new Error('界面已锁定，请先解锁。')
  }

  getState(): InterfaceLockState {
    return {
      locked: this.state?.locked ?? false,
      hasPassword: Boolean(this.state),
    }
  }

  async lock(password: string): Promise<InterfaceLockState> {
    this.assertUnlocked()
    validatePassword(password)
    this.changing = true
    try {
    const salt = randomBytes(16)
    const nextState: StoredLockState = {
      version: FILE_VERSION,
      locked: true,
      salt: salt.toString('base64'),
      verifier: derive(password, salt).toString('base64'),
    }
    await this.persist(nextState)
    this.state = nextState
    return this.getState()
    } finally { this.changing = false }
  }

  async unlock(password: string): Promise<InterfaceLockState> {
    if (!this.initialized || this.changing) throw new Error('锁定状态尚未就绪')
    if (Date.now() < this.nextAttemptAt) throw new Error('密码错误次数过多，请稍后重试')
    validatePassword(password)
    if (!this.state) throw new Error('尚未设置锁定密码')
    this.changing = true
    try {
    const salt = Buffer.from(this.state.salt, 'base64')
    const expected = Buffer.from(this.state.verifier, 'base64')
    const actual = derive(password, salt)
    if (expected.length !== actual.length || !timingSafeEqual(expected, actual)) {
      this.failedAttempts += 1
      this.nextAttemptAt = Date.now() + Math.min(30_000, 500 * 2 ** Math.min(this.failedAttempts, 6))
      throw new Error('密码错误，请重试')
    }

    const nextState = { ...this.state, locked: false }
    await this.persist(nextState)
    this.state = nextState
    this.failedAttempts = 0
    this.nextAttemptAt = 0
    return this.getState()
    } finally { this.changing = false }
  }

  private async persist(state: StoredLockState): Promise<void> {
    const directory = path.dirname(this.filePath)
    await mkdir(directory, { recursive: true })
    const temporaryPath = `${this.filePath}.${process.pid}.${randomBytes(6).toString('hex')}.tmp`
    try {
      await writeFile(temporaryPath, `${JSON.stringify(state, null, 2)}\n`, { encoding: 'utf8', mode: 0o600 })
      await rename(temporaryPath, this.filePath)
    } catch (error) {
      await unlink(temporaryPath).catch(() => undefined)
      throw error
    }
  }
}

function derive(password: string, salt: Buffer): Buffer {
  return scryptSync(password, salt, KEY_LENGTH)
}

function validatePassword(password: string): void {
  if (typeof password !== 'string' || password.length < MIN_PASSWORD_LENGTH) {
    throw new Error(`密码至少需要 ${MIN_PASSWORD_LENGTH} 个字符`)
  }
  if (password.length > MAX_PASSWORD_LENGTH) throw new Error(`密码不能超过 ${MAX_PASSWORD_LENGTH} 个字符`)
}

function storedState(value: unknown): StoredLockState {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('锁定状态文件无效')
  const input = value as Partial<StoredLockState>
  if (input.version !== FILE_VERSION || typeof input.locked !== 'boolean') throw new Error('锁定状态文件版本无效')
  if (typeof input.salt !== 'string' || Buffer.from(input.salt, 'base64').length !== 16) throw new Error('锁定状态盐值无效')
  if (typeof input.verifier !== 'string' || Buffer.from(input.verifier, 'base64').length !== KEY_LENGTH) throw new Error('锁定状态摘要无效')
  return input as StoredLockState
}
