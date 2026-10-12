import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { clearSession, session, setSession } from './session'
import { clearUserSession, requirePasswordChange, setUserSession, updateUser, userSession, userToken } from './user-session'
import { request, userApi } from './api'
import type { UserLoginResult } from '../types'

function login(): UserLoginResult {
  return { access_token: 'user-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), user: { id: 'user-1', email: 'user@example.com', display_name: 'User', status: 'ACTIVE', must_change_password: false, created_at: new Date().toISOString() } }
}

function admin(): void {
  setSession({ access_token: 'admin-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), admin: { id: 'admin-1', email: 'admin@example.com', status: 'ACTIVE', created_at: new Date().toISOString() } })
}

describe('user realm isolation', () => {
  beforeEach(() => { clearSession(); clearUserSession(); vi.stubGlobal('fetch', vi.fn()) })
  afterEach(() => { clearSession(); clearUserSession(); vi.unstubAllGlobals(); vi.useRealTimers() })

  it('stores identities separately and clears only the selected realm', () => {
    admin(); setUserSession(login())
    expect(sessionStorage.getItem('fcard.admin.session')).toContain('admin-token')
    expect(sessionStorage.getItem('fcard.user.session')).toContain('user-token')
    clearUserSession()
    expect(session.value?.access_token).toBe('admin-token')
    setUserSession(login()); clearSession()
    expect(userToken()).toBe('user-token')
  })

  it('does not accept administrator or malformed profiles as users', () => {
    expect(() => setUserSession({ ...login(), user: undefined } as unknown as UserLoginResult)).toThrow()
    expect(() => setUserSession({ ...login(), user: { ...login().user, must_change_password: undefined } } as unknown as UserLoginResult)).toThrow()
    expect(userSession.value).toBeNull()
  })

  it('expires only the user session', () => {
    vi.useFakeTimers()
    admin(); setUserSession({ ...login(), expires_at: new Date(Date.now() + 1000).toISOString() })
    vi.advanceTimersByTime(1001)
    expect(userToken()).toBeNull()
    expect(session.value).not.toBeNull()
  })

  it('sends the user token instead of an existing administrator token', async () => {
    admin(); setUserSession(login())
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ data: login().user })))
    await userApi.me()
    const [path, options] = vi.mocked(fetch).mock.calls[0]!
    expect(path).toBe('/api/v1/user/auth/me')
    expect(new Headers(options?.headers).get('Authorization')).toBe('Bearer user-token')
  })

  it('clears a revoked user session without clearing administrator login', async () => {
    admin(); setUserSession(login())
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ error: { code: 'unauthorized' } }), { status: 401 }))
    await expect(userApi.me()).rejects.toMatchObject({ status: 401 })
    expect(userSession.value).toBeNull()
    expect(session.value?.access_token).toBe('admin-token')
  })

  it('does not clear user login when the administrator session expires', async () => {
    admin(); setUserSession(login())
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ error: { code: 'unauthorized' } }), { status: 401 }))
    await expect(request('/api/v1/admin/auth/me')).rejects.toMatchObject({ status: 401 })
    expect(session.value).toBeNull()
    expect(userToken()).toBe('user-token')
  })

  it('ignores outdated failures and profile updates after account switches', async () => {
    setUserSession(login())
    let respond!: (response: Response) => void
    vi.mocked(fetch).mockImplementation(() => new Promise<Response>((resolve) => { respond = resolve }))
    const pending = userApi.me()
    setUserSession({ ...login(), access_token: 'second-token', user: { ...login().user, id: 'user-2' } })
    updateUser({ ...login().user, display_name: 'Old data' }, 'user-token')
    requirePasswordChange('user-token')
    respond(new Response(JSON.stringify({ error: { code: 'unauthorized' } }), { status: 401 }))
    await expect(pending).rejects.toMatchObject({ status: 401 })
    expect(userSession.value?.user.id).toBe('user-2')
    expect(userToken()).toBe('second-token')
    expect(userSession.value?.user.must_change_password).toBe(false)
  })

  it('reacts to server-side forced password change without touching admin', async () => {
    admin(); setUserSession(login())
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ error: { code: 'password_change_required' } }), { status: 403 }))
    await expect(request('/api/v1/user/future-resource', { realm: 'user' })).rejects.toMatchObject({ code: 'password_change_required' })
    expect(userSession.value?.user.must_change_password).toBe(true)
    expect(session.value?.access_token).toBe('admin-token')
  })

  it('does not send a token on user login or clear sessions on wrong password', async () => {
    admin(); setUserSession(login())
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ error: { code: 'unauthorized' } }), { status: 401 }))
    await expect(userApi.login('user@example.com', 'wrong')).rejects.toMatchObject({ status: 401 })
    const [, options] = vi.mocked(fetch).mock.calls[0]!
    expect(new Headers(options?.headers).has('Authorization')).toBe(false)
    expect(userSession.value).not.toBeNull()
    expect(session.value).not.toBeNull()
  })
})
