import { expect, test, type Page } from '@playwright/test'

const userID = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
const holderID = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd'
const cardID = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc'
const operationID = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'
const timestamp = '2026-10-11T02:00:00Z'
const profile = { id: userID, email: 'cards@example.com', display_name: '开卡测试用户', status: 'ACTIVE', must_change_password: false, created_at: timestamp }
const admin = { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', email: 'admin@example.com', status: 'ACTIVE', created_at: timestamp }

async function sessions(page: Page): Promise<void> {
  await page.addInitScript(({ profile, admin }) => {
    if (!sessionStorage.getItem('fcard.user.session')) sessionStorage.setItem('fcard.user.session', JSON.stringify({ access_token: 'user-card-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), user: profile }))
    if (!sessionStorage.getItem('fcard.admin.session')) sessionStorage.setItem('fcard.admin.session', JSON.stringify({ access_token: 'admin-card-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), admin }))
  }, { profile, admin })
}

async function mockCards(page: Page, initiallyBound = false, abortFirst = false) {
  let bound = initiallyBound
  let created = false
  let active = false
  const keys: string[] = []
  const responseCard = () => ({ id: cardID, nick_name: '浏览器沙盒卡', status: active ? 'ACTIVE' : 'PENDING', last_four: '1234', limit_cents: 1000, currency: 'USD', operation_id: operationID, created_at: timestamp, updated_at: timestamp })
  const responseOperation = () => ({ id: operationID, card_id: cardID, status: 'SUCCEEDED', attempts: 1, created_at: timestamp, updated_at: timestamp })
  await page.route('**/api/v1/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const respond = (data: unknown, status = 200) => route.fulfill({ status, json: { data, request_id: 'card-test-trace' } })
    if (path === '/api/v1/user/auth/me') return respond(profile)
    if (path === '/api/v1/admin/auth/me') return respond(admin)
    if (path === '/api/v1/admin/users') return respond({ items: [{ ...profile, updated_at: timestamp, login_enabled: true }], total: 1, page: 1, page_size: 20 })
    if (path === '/api/v1/admin/company/cardholder') {
      if (request.method() === 'POST') {
        expect(request.headers().authorization).toBe('Bearer admin-card-token')
        expect(request.postDataJSON()).toEqual({ provider_cardholder_id: holderID })
        bound = true
      }
      return respond(bound ? { bound: true, provider_cardholder_id: holderID, legal_name: 'John Smith' } : { bound: false })
    }
    if (path === '/api/v1/user/cards/config') return respond({ enabled: true, company_configured: bound, limit_cents: 1000, currency: 'USD', max_cards: 1, occupied_slots: created ? 1 : 0 })
    if (path === '/api/v1/user/cards' && request.method() === 'POST') {
      expect(request.headers().authorization).toBe('Bearer user-card-token')
      expect(request.postDataJSON()).toEqual({ nick_name: '浏览器沙盒卡' })
      keys.push(request.headers()['idempotency-key']!)
      created = true
      if (abortFirst && keys.length === 1) return route.abort('failed')
      return respond(responseOperation(), 202)
    }
    if (path === '/api/v1/user/cards') return respond({ items: created ? [responseCard()] : [], total: created ? 1 : 0, page: 1, page_size: 20 })
    if (path === `/api/v1/user/card-operations/${operationID}`) return respond(responseOperation())
    if (path === `/api/v1/user/cards/${cardID}/sync`) { active = true; return respond(responseCard()) }
    return route.fulfill({ status: 404, json: { error: { code: 'not_found' } } })
  })
  return keys
}

test('company issuing configuration and user sandbox card creation, sync and mobile layout', async ({ page }) => {
  await sessions(page)
  const keys = await mockCards(page)
  await page.goto('/portal/cards')
  await expect(page.getByRole('button', { name: '申请沙盒虚拟卡' })).toBeDisabled()
  await expect(page.getByText('请联系管理员配置公司统一发卡身份')).toBeVisible()
  await page.goto('/cardholders')
  await page.getByLabel('Airwallex 持卡人 ID').fill(holderID)
  await page.getByRole('button', { name: '核验并配置公司身份' }).click()
  await expect(page.getByRole('status')).toContainText('John Smith')
  await page.getByLabel('选择测试用户').selectOption(userID)
  await expect(page.getByRole('status')).toContainText('John Smith')
  await page.getByLabel('选择测试用户').selectOption('')
  await expect(page.getByRole('status')).toContainText('John Smith')
  await page.goto('/portal/cards')
  await page.getByLabel('卡片名称').fill('浏览器沙盒卡')
  await page.getByRole('button', { name: '申请沙盒虚拟卡' }).click()
  await expect(page.getByRole('heading', { name: '浏览器沙盒卡' })).toBeVisible()
  await expect(page.getByText('开卡操作：')).toContainText('开卡请求已完成')
  await expect(page.getByRole('button', { name: '申请沙盒虚拟卡' })).toBeDisabled()
  await page.getByRole('button', { name: '同步供应商状态' }).click()
  await expect(page.getByRole('heading', { name: '浏览器沙盒卡' })).toContainText('已激活')
  expect(keys).toHaveLength(1)
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: 'test-results/cards-mobile.png', fullPage: true })
})

test('closed card sync releases a slot and retains history across pagination', async ({ page }) => {
  await sessions(page)
  let closed = false
  let replacement = false
  const keys: string[] = []
  const replacementCardID = 'ffffffff-ffff-4fff-8fff-ffffffffffff'
  const replacementOperationID = '99999999-9999-4999-8999-999999999999'
  const oldCard = () => ({ id: cardID, nick_name: '旧员工卡', status: closed ? 'CLOSED' : 'PENDING', last_four: '1234', limit_cents: 1000, currency: 'USD', operation_id: operationID, created_at: timestamp, updated_at: timestamp })
  const newCard = () => ({ ...oldCard(), id: replacementCardID, nick_name: '新企业卡', status: 'ACTIVE', operation_id: replacementOperationID })
  const history = () => Array.from({ length: 9 }, (_, index) => ({ ...oldCard(), id: `77777777-7777-4777-8777-${String(index).padStart(12, '0')}`, operation_id: `88888888-8888-4888-8888-${String(index).padStart(12, '0')}`, nick_name: `历史企业卡 ${index + 1}`, status: 'CLOSED' }))
  const allCards = () => replacement ? [newCard(), ...history(), oldCard()] : [oldCard()]
  await page.route('**/api/v1/**', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const respond = (data: unknown, status = 200) => route.fulfill({ status, json: { data, request_id: 'slot-test-trace' } })
    if (path === '/api/v1/user/auth/me') return respond(profile)
    if (path === '/api/v1/user/cards/config') return respond({ enabled: true, company_configured: true, limit_cents: 1000, currency: 'USD', max_cards: 1, occupied_slots: replacement || !closed ? 1 : 0 })
    if (path === '/api/v1/user/cards' && request.method() === 'POST') {
      expect(request.postDataJSON()).toEqual({ nick_name: '新企业卡' })
      keys.push(request.headers()['idempotency-key']!)
      replacement = true
      return respond({ id: replacementOperationID, card_id: replacementCardID, status: 'SUCCEEDED', attempts: 1, created_at: timestamp, updated_at: timestamp }, 202)
    }
    if (path === '/api/v1/user/cards') {
      const pageNumber = Number(url.searchParams.get('page') ?? 1)
      const pageSize = Number(url.searchParams.get('page_size') ?? 20)
      const all = allCards()
      return respond({ items: all.slice((pageNumber - 1) * pageSize, pageNumber * pageSize), total: all.length, page: pageNumber, page_size: pageSize })
    }
    if (path.startsWith('/api/v1/user/card-operations/')) {
      const owner = allCards().find(item => path.endsWith(item.operation_id))!
      return respond({ id: owner.operation_id, card_id: owner.id, status: 'SUCCEEDED', attempts: 1, created_at: timestamp, updated_at: timestamp })
    }
    if (path === `/api/v1/user/cards/${cardID}/sync`) { closed = true; return respond(oldCard()) }
    return route.fulfill({ status: 404, json: { error: { code: 'not_found' } } })
  })
  await page.goto('/portal/cards')
  await expect(page.getByRole('button', { name: '申请沙盒虚拟卡' })).toBeDisabled()
  await page.getByRole('button', { name: '同步供应商状态' }).click()
  await expect(page.getByRole('heading', { name: '旧员工卡' })).toContainText('已关闭')
  await expect(page.getByText('开卡操作：')).toContainText('开卡请求已完成')
  await expect(page.getByRole('button', { name: '申请沙盒虚拟卡' })).toBeEnabled()
  await page.reload()
  await expect(page.getByRole('button', { name: '申请沙盒虚拟卡' })).toBeEnabled()
  await page.getByLabel('卡片名称').fill('新企业卡')
  await page.getByRole('button', { name: '申请沙盒虚拟卡' }).click()
  await expect(page.getByRole('heading', { name: '新企业卡' })).toContainText('已激活')
  await expect(page.getByRole('heading', { name: '旧员工卡' })).toContainText('已关闭')
  await expect(page.getByRole('button', { name: '申请沙盒虚拟卡' })).toBeDisabled()
  expect(keys).toHaveLength(1)
  await page.getByLabel('每页记录数').selectOption('10')
  await expect(page.getByRole('heading', { name: '新企业卡' })).toBeVisible()
  await page.getByRole('button', { name: '下一页' }).click()
  await expect(page.getByRole('heading', { name: '旧员工卡' })).toContainText('已关闭')
  await expect(page.getByRole('heading', { name: '新企业卡' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '申请沙盒虚拟卡' })).toBeDisabled()
})

test('ambiguous submission preserves its key across refresh and explicit retry', async ({ page }) => {
  await sessions(page)
  const keys = await mockCards(page, true, true)
  await page.goto('/portal/cards')
  await page.getByLabel('卡片名称').fill('浏览器沙盒卡')
  await page.getByRole('button', { name: '申请沙盒虚拟卡' }).click()
  await expect(page.getByRole('alert')).toContainText('无法连接后端')
  await expect(page.getByRole('button', { name: '重试原申请' })).toBeVisible()
  expect(keys).toHaveLength(1)
  await page.reload()
  await expect(page.getByLabel('卡片名称')).toHaveValue('浏览器沙盒卡')
  await expect(page.getByLabel('卡片名称')).toBeDisabled()
  await expect(page.getByRole('button', { name: '重试原申请' })).toBeEnabled()
  expect(keys).toHaveLength(1)
  await page.getByRole('button', { name: '重试原申请' }).click()
  await expect(page.getByText('申请已保存。')).toBeVisible()
  expect(keys).toHaveLength(2)
  expect(keys[1]).toBe(keys[0])
})
