import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { clearSession, session, setSession, token, updateAdmin } from './session'
import type { LoginResult } from '../types'

function value(): LoginResult {
  return { access_token: 'test-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 60000).toISOString(), admin: { id: '1', email: 'admin@example.com', status: 'ACTIVE', created_at: new Date().toISOString() } }
}

describe('session lifecycle', () => {
  beforeEach(clearSession)
  afterEach(() => { clearSession(); vi.useRealTimers() })

  it('persists only the session, not login credentials', () => {
    setSession(value())
    expect(token()).toBe('test-token')
    expect(JSON.parse(sessionStorage.getItem('fcard.admin.session')!).admin.email).toBe('admin@example.com')
    expect(localStorage.getItem('fcard.admin.session')).toBeNull()
    clearSession()
    expect(sessionStorage.getItem('fcard.admin.session')).toBeNull()
  })

  it('expires a session before making a new request', () => {
    vi.useFakeTimers()
    setSession(value())
    vi.advanceTimersByTime(61000)
    expect(token()).toBeNull()
    expect(session.value).toBeNull()
  })

  it('rejects expired session payloads', () => {
    expect(() => setSession({ ...value(), expires_at: 'invalid' })).toThrow()
    expect(session.value).toBeNull()
  })

  it('does not replace the current identity with a stale profile', () => {
    setSession(value())
    updateAdmin({ ...value().admin, id: 'other-admin', email: 'other@example.com' })
    expect(session.value?.admin.id).toBe('1')
  })
})
