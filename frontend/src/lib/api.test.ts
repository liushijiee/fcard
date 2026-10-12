import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError, request } from './api'
import { clearSession, session, setSession } from './session'
import type { LoginResult } from '../types'

function loginSession(): LoginResult {
  return {
    access_token: 'unit-test-token', token_type: 'Bearer',
    expires_at: new Date(Date.now() + 3600000).toISOString(),
    admin: { id: 'admin-1', email: 'admin@example.com', status: 'ACTIVE', created_at: new Date().toISOString() },
  }
}

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('API client', () => {
  beforeEach(() => { clearSession(); vi.stubGlobal('fetch', vi.fn()) })
  afterEach(() => { clearSession(); vi.unstubAllGlobals() })

  it('does not send a protected request without a session', async () => {
    await expect(api.me()).rejects.toMatchObject({ status: 401 })
    expect(fetch).not.toHaveBeenCalled()
  })

  it('unwraps data and sends the Bearer token', async () => {
    setSession(loginSession())
    vi.mocked(fetch).mockResolvedValue(json({ data: { id: 'admin-1' }, request_id: 'trace-1' }))
    expect(await api.me()).toEqual({ id: 'admin-1' })
    const [path, options] = vi.mocked(fetch).mock.calls[0]!
    expect(path).toBe('/api/v1/admin/auth/me')
    expect(new Headers(options?.headers).get('Authorization')).toBe('Bearer unit-test-token')
  })

  it('sends login without a stored token and serializes JSON', async () => {
    setSession(loginSession())
    vi.mocked(fetch).mockResolvedValue(json({ data: loginSession() }))
    await api.login('admin@example.com', 'password')
    const [, options] = vi.mocked(fetch).mock.calls[0]!
    expect(new Headers(options?.headers).has('Authorization')).toBe(false)
    expect(JSON.parse(String(options?.body))).toEqual({ email: 'admin@example.com', password: 'password' })
  })

  it('clears an invalid protected session on 401', async () => {
    setSession(loginSession())
    vi.mocked(fetch).mockResolvedValue(json({ error: { code: 'unauthorized' }, request_id: 'trace-2' }, 401))
    await expect(api.me()).rejects.toMatchObject({ status: 401, requestID: 'trace-2' })
    expect(session.value).toBeNull()
  })

  it('does not clear a newer session after a stale request fails', async () => {
    setSession(loginSession())
    let respond!: (response: Response) => void
    vi.mocked(fetch).mockImplementation(() => new Promise<Response>((resolve) => { respond = resolve }))
    const pending = api.me()
    setSession({ ...loginSession(), access_token: 'new-token' })
    respond(json({ error: { code: 'unauthorized' } }, 401))
    await expect(pending).rejects.toBeInstanceOf(ApiError)
    expect(session.value?.access_token).toBe('new-token')
  })

  it('provides a localized conflict and a request ID', async () => {
    setSession(loginSession())
    vi.mocked(fetch).mockResolvedValue(json({ error: { code: 'conflict' }, request_id: 'trace-3' }, 409))
    await expect(api.createUser({ email: 'a@example.com', display_name: 'A' })).rejects.toMatchObject({ message: '该邮箱已存在，请使用其他邮箱。', requestID: 'trace-3' })
  })

  it('reports network failures without exposing raw exceptions', async () => {
    vi.mocked(fetch).mockRejectedValue(new Error('raw internal network detail'))
    await expect(api.health('ready')).rejects.toMatchObject({ code: 'network_error', status: 0 })
  })

  it('rejects invalid JSON or missing data', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response('proxy error', { status: 502 }))
    await expect(api.health('ready')).rejects.toMatchObject({ code: 'invalid_response' })
    vi.mocked(fetch).mockResolvedValueOnce(json({ request_id: 'trace-4' }))
    await expect(request('/health/ready', { auth: false })).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('does not invalidate a session on an unrelated health failure', async () => {
    setSession(loginSession())
    vi.mocked(fetch).mockResolvedValue(json({ error: { code: 'not_ready' } }, 503))
    await expect(api.health('ready')).rejects.toMatchObject({ status: 503 })
    expect(session.value).not.toBeNull()
  })

  it('rejects a null response envelope', async () => {
    vi.mocked(fetch).mockResolvedValue(json(null))
    await expect(api.health('ready')).rejects.toMatchObject({ code: 'invalid_response' })
  })
})
