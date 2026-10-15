import { expect, test, type Page } from '@playwright/test'

const userID = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
const orderID = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc'
const timestamp = '2026-10-13T02:00:00Z'
const profile = { id: userID, email: 'deposit@example.com', display_name: '测试收款用户', status: 'ACTIVE', must_change_password: false, created_at: timestamp }
const admin = { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', email: 'admin@example.com', status: 'ACTIVE', created_at: timestamp }
async function sessions(page: Page): Promise<void> {
  await page.addInitScript(({ profile, admin }) => {
    sessionStorage.setItem('fcard.user.session', JSON.stringify({ access_token: 'deposit-user-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), user: profile }))
    sessionStorage.setItem('fcard.admin.session', JSON.stringify({ access_token: 'deposit-admin-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), admin }))
  }, { profile, admin })
}

test('ambiguous Fcard submission retains its key across reload, and only WAITING reveals exact payment instructions', async ({ page }) => {
  await sessions(page)
  let created = false
  let waiting = false
  const keys: string[] = []
  const order = () => ({ id: orderID, user_id: userID, amount: '20.010001', status: waiting ? 'WAITING' : 'CREATING', environment: 'nile', network: 'tron', contract: 'synthetic-contract', created_at: timestamp, ...(waiting ? { actual_amount: '20.010002', receive_address: 'TD5gsCwxykWsLN9aPrq2TAfNjByuZKYp4E', payment_url: 'https://gateway.example/pay/checkout-counter/test', expires_at: new Date(Date.now() + 60000).toISOString() } : {}) })
  await page.route('**/api/v1/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const respond = (data: unknown, status = 200) => route.fulfill({ status, json: { data, request_id: 'synthetic-deposit-request' } })
    if (path === '/api/v1/user/auth/me') return respond(profile)
    if (path === '/api/v1/user/deposits/config') return respond({ available: true, enabled: true, environment: 'nile', balance_crediting: false })
    if (path === '/api/v1/user/deposits' && request.method() === 'POST') {
      expect(request.headers().authorization).toBe('Bearer deposit-user-token')
      expect(request.postDataJSON()).toEqual({ amount: '20.010001' })
      keys.push(request.headers()['idempotency-key']!)
      created = true
      if (keys.length === 1) return route.abort('failed')
      waiting = true
      return respond(order(), 202)
    }
    if (path === '/api/v1/user/deposits') return respond({ items: created ? [order()] : [], total: created ? 1 : 0, page: 1, page_size: 20 })
    return route.fulfill({ status: 404, json: { error: { code: 'not_found' } } })
  })
  await page.goto('/portal/deposits')
  await page.getByLabel('测试金额（最多六位小数）').fill('20.010001')
  await page.getByRole('button', { name: '创建测试订单', exact: true }).click()
  await expect(page.getByRole('button', { name: '查询原提交结果' })).toBeVisible()
  await expect(page.getByText('Nile 收款地址：', { exact: false })).toHaveCount(0)
  await page.reload()
  await expect(page.getByLabel('测试金额（最多六位小数）')).toBeDisabled()
  await page.getByRole('button', { name: '查询原提交结果' }).click()
  await expect(page.getByText('TD5gsCwxykWsLN9aPrq2TAfNjByuZKYp4E', { exact: false })).toBeVisible()
  expect(keys).toHaveLength(2)
  expect(keys[0]).toBe(keys[1])
  await expect(page.getByRole('link', { name: '打开审核来源收银台' })).toHaveAttribute('rel', 'noopener noreferrer')
})

test('disabled runtime never offers a create button or requests deposit history', async ({ page }) => {
  await sessions(page)
  let history = 0
  await page.route('**/api/v1/**', async route => {
    const path = new URL(route.request().url()).pathname
    const data = path === '/api/v1/user/auth/me' ? profile : { available: false, enabled: false, environment: 'nile', balance_crediting: false }
    if (path === '/api/v1/user/deposits') history++
    return route.fulfill({ status: 200, json: { data, request_id: 'disabled-test' } })
  })
  await page.goto('/portal/deposits')
  await expect(page.getByRole('button', { name: '创建测试订单', exact: true })).toBeDisabled()
  await expect(page.getByText('收款未启用或扫描器尚未就绪。')).toBeVisible()
  expect(history).toBe(0)
})

test('administrator review exposes no CREATE retry or automatic unmatched-fund assignment', async ({ page }) => {
  await sessions(page)
  await page.route('**/api/v1/**', async route => {
    const path = new URL(route.request().url()).pathname
    const data = path === '/api/v1/admin/auth/me' ? admin : { orders: { items: [], total: 0, page: 1, page_size: 20 }, counts: { UnresolvedCreates: 1, ReviewJobs: 2, PendingJobs: 0 }, review_jobs: [{ ID: orderID, Kind: 'CREATE', LastError: 'CREATE_UNKNOWN', Attempts: 1 }, { ID: userID, Kind: 'VERIFY_CHAIN', LastError: 'PROOF_PENDING', Attempts: 10 }], unmatched_transfers: [{ TxHash: 'ab'.repeat(32), EventIndex: 0, ToAddress: 'synthetic-address', RawUnits: '20010002', BlockTime: timestamp }] }
    return route.fulfill({ status: 200, json: { data, request_id: 'admin-review-test' } })
  })
  await page.goto('/deposits')
  await expect(page.getByRole('button', { name: '受控重排核验' })).toHaveCount(1)
  await expect(page.getByText('没有自动归属按钮', { exact: false })).toBeVisible()
})
