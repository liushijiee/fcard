import { readonly, shallowRef } from 'vue'
import type { LoginResult } from '../types'

const key = 'fcard.admin.session'

function readSession(): LoginResult | null {
  try {
    const raw = sessionStorage.getItem(key)
    if (!raw) return null
    const value: unknown = JSON.parse(raw)
    if (!validSession(value)) {
      sessionStorage.removeItem(key)
      return null
    }
    return value
  } catch {
    return null
  }
}

function validSession(value: unknown): value is LoginResult {
  if (!value || typeof value !== 'object') return false
  const candidate = value as Partial<LoginResult>
  return typeof candidate.access_token === 'string'
    && candidate.access_token.length > 0
    && candidate.token_type === 'Bearer'
    && typeof candidate.expires_at === 'string'
    && Date.parse(candidate.expires_at) > Date.now()
    && typeof candidate.admin?.id === 'string'
    && typeof candidate.admin?.email === 'string'
    && candidate.admin?.status === 'ACTIVE'
}

const current = shallowRef<LoginResult | null>(readSession())
export const session = readonly(current)

export function setSession(value: LoginResult): void {
  if (!validSession(value)) throw new Error('服务返回了无效的登录会话')
  current.value = value
  try { sessionStorage.setItem(key, JSON.stringify(value)) } catch { }
}

export function clearSession(): void {
  current.value = null
  try { sessionStorage.removeItem(key) } catch { }
}

export function token(): string | null {
  if (current.value && Date.parse(current.value.expires_at) <= Date.now()) clearSession()
  return current.value?.access_token ?? null
}

export function updateAdmin(admin: LoginResult['admin']): void {
  if (current.value?.admin.id === admin.id) setSession({ ...current.value, admin })
}
