import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, userApi } from './api'
import { clearSession, setSession } from './session'
import { clearUserSession, setUserSession } from './user-session'

describe('sandbox card API', () => {
  beforeEach(() => {
    setSession({ access_token: 'admin-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), admin: { id: 'admin', email: 'admin@example.com', status: 'ACTIVE', created_at: '' } })
    setUserSession({ access_token: 'user-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), user: { id: 'user', email: 'user@example.com', display_name: 'User', status: 'ACTIVE', must_change_password: false, created_at: '' } })
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ data: { status: 'PENDING' } }), { status: 202 }))))
  })
  afterEach(() => { clearSession(); clearUserSession(); vi.unstubAllGlobals() })

  it('uses the user realm and the caller-stable idempotency key without controlled fields', async () => {
    await userApi.createCard('Test card', 'stable-request-key')
    await userApi.createCard('Test card', 'stable-request-key')
    for (const [path, options] of vi.mocked(fetch).mock.calls) {
      expect(path).toBe('/api/v1/user/cards')
      expect(new Headers(options?.headers).get('Authorization')).toBe('Bearer user-token')
      expect(new Headers(options?.headers).get('Idempotency-Key')).toBe('stable-request-key')
      expect(JSON.parse(String(options?.body))).toEqual({ nick_name: 'Test card' })
    }
  })

  it('does not automatically repeat an ambiguous provider failure', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ error: { code: 'provider_unavailable' } }), { status: 502 }))
    await expect(userApi.createCard('Card', 'key')).rejects.toMatchObject({ status: 502 })
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('configures the company identity only through an administrator endpoint', async () => {
    await api.bindCardholder('holder-id')
    const [path, options] = vi.mocked(fetch).mock.calls[0]!
    expect(path).toBe('/api/v1/admin/company/cardholder')
    expect(new Headers(options?.headers).get('Authorization')).toBe('Bearer admin-token')
    expect(JSON.parse(String(options?.body))).toEqual({ provider_cardholder_id: 'holder-id' })
  })
})
