import { readonly, shallowRef } from 'vue'
import type { UserLoginResult, UserProfile } from '../types'

const key = 'fcard.user.session'

function valid(value: unknown): value is UserLoginResult {
  if (!value || typeof value !== 'object') return false
  const candidate = value as Partial<UserLoginResult>
  return typeof candidate.access_token === 'string' && candidate.access_token.length > 0
    && candidate.token_type === 'Bearer' && typeof candidate.expires_at === 'string'
    && Date.parse(candidate.expires_at) > Date.now()
    && typeof candidate.user?.id === 'string' && typeof candidate.user?.email === 'string'
    && typeof candidate.user?.display_name === 'string' && candidate.user?.status === 'ACTIVE'
    && typeof candidate.user?.must_change_password === 'boolean'
}

function read(): UserLoginResult | null {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(key) ?? 'null')
    if (valid(value)) return value
    sessionStorage.removeItem(key)
  } catch { }
  return null
}

const current = shallowRef<UserLoginResult | null>(read())
export const userSession = readonly(current)

export function setUserSession(value: UserLoginResult): void {
  if (!valid(value)) throw new Error('服务返回了无效的用户会话')
  current.value = value
  try { sessionStorage.setItem(key, JSON.stringify(value)) } catch { }
}

export function clearUserSession(): void {
  current.value = null
  try { sessionStorage.removeItem(key) } catch { }
}

export function userToken(): string | null {
  if (current.value && Date.parse(current.value.expires_at) <= Date.now()) clearUserSession()
  return current.value?.access_token ?? null
}

export function updateUser(profile: UserProfile, expectedToken: string): void {
  if (current.value?.access_token === expectedToken && current.value.user.id === profile.id) {
    setUserSession({ ...current.value, user: profile })
  }
}

export function requirePasswordChange(expectedToken: string): void {
  if (current.value?.access_token === expectedToken) {
    setUserSession({ ...current.value, user: { ...current.value.user, must_change_password: true } })
  }
}
