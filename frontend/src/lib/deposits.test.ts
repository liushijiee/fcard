import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { deposits, payableDeposit, validDepositAmount, type DepositOrder } from './deposits'
import { clearSession, setSession } from './session'
import { clearUserSession, setUserSession } from './user-session'

describe('Nile deposits', () => {
  beforeEach(() => {
    setSession({ access_token: 'admin-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), admin: { id: 'admin', email: 'admin@example.com', status: 'ACTIVE', created_at: '' } })
    setUserSession({ access_token: 'user-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), user: { id: 'user', email: 'user@example.com', display_name: 'User', status: 'ACTIVE', must_change_password: false, created_at: '' } })
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ data: { status: 'CREATING' } }), { status: 202 }))))
  })
  afterEach(() => { clearSession(); clearUserSession(); vi.unstubAllGlobals() })
  it('keeps six-decimal amounts exact and bounded', () => {
    for (const amount of ['20.010001', '0.010001', '1000000']) expect(validDepositAmount(amount)).toBe(true)
    for (const amount of ['0.01', '1000000.000001', '1e2', '01', '-1', '1.1234567', '', 'NaN']) expect(validDepositAmount(amount)).toBe(false)
  })
  it('never advertises unknown or expired payment instructions', () => {
    const order = { status: 'WAITING', receive_address: 'T-test', actual_amount: '20.010002', expires_at: new Date(Date.now() + 60000).toISOString() } as DepositOrder
    expect(payableDeposit(order)).toBe(true)
    expect(payableDeposit({ ...order, status: 'CREATE_UNKNOWN' })).toBe(false)
    expect(payableDeposit({ ...order, expires_at: '2020-01-01' })).toBe(false)
  })
  it('sends only amount and the stable key in the user realm', async () => {
    await deposits.create('20.010001', 'original-key')
    const [path, options] = vi.mocked(fetch).mock.calls[0]!
    expect(path).toBe('/api/v1/user/deposits')
    expect(new Headers(options?.headers).get('Authorization')).toBe('Bearer user-token')
    expect(new Headers(options?.headers).get('Idempotency-Key')).toBe('original-key')
    expect(JSON.parse(String(options?.body))).toEqual({ amount: '20.010001' })
  })
  it('does not retry a failed POST automatically', async () => {
    vi.mocked(fetch).mockRejectedValue(new Error('lost response'))
    await expect(deposits.create('20.010001', 'key')).rejects.toMatchObject({ code: 'network_error' })
    expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('recovery and requeue use administrator credentials', async () => {
    await deposits.recover('order', { claim_token: 'claim', reason: 'reviewed quote', evidence_reference: 'controlled-export', quote: {} })
    expect(new Headers(vi.mocked(fetch).mock.calls[0]![1]?.headers).get('Authorization')).toBe('Bearer admin-token')
    await deposits.requeue('job', 'reviewed chain proof')
    expect(new Headers(vi.mocked(fetch).mock.calls[1]![1]?.headers).get('Authorization')).toBe('Bearer admin-token')
  })
})
