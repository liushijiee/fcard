import { expect, test, type Page } from '@playwright/test'

const timestamp = '2026-10-10T02:00:00Z'
const admin = { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', email: 'admin@example.com', status: 'ACTIVE', created_at: timestamp }
const person = { id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', email: 'person@example.com', display_name: '测试用户', status: 'ACTIVE', created_at: timestamp, updated_at: timestamp }

async function mockBackend(page: Page): Promise<void> {
  let users = [person]
  await page.route('**/health/**', (route) => route.fulfill({ json: { data: { status: route.request().url().endsWith('/ready') ? 'ready' : 'ok' }, request_id: 'trace' } }))
  await page.route('**/api/v1/admin/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    const response = (data: unknown, status = 200) => route.fulfill({ status, json: { data, request_id: 'mock-request-id' } })
    if (path.endsWith('/auth/login')) return response({ access_token: 'mock-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), admin })
    if (path.endsWith('/auth/me')) return response(admin)
    if (path.endsWith('/auth/logout')) return response({ logged_out: true })
    if (path.endsWith('/audit-logs')) return response({ items: [{ id: 'audit-1', admin_id: admin.id, action: 'user.create', target_id: person.id, request_id: 'audit-trace', changes: { fields: ['email', 'display_name', 'status'] }, created_at: timestamp }], total: 1, page: 1, page_size: 10 })
    if (path.endsWith('/users') && request.method() === 'POST') {
      const input = request.postDataJSON() as { email: string; display_name: string }
      if (users.some((item) => item.email === input.email)) return route.fulfill({ status: 409, json: { error: { code: 'conflict', message: 'resource already exists' }, request_id: 'conflict-trace' } })
      const created = { ...person, ...input, id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc' }
      users = [created, ...users]
      return response(created, 201)
    }
    if (path.endsWith('/users')) return response({ items: users, total: users.length, page: 1, page_size: 10 })
    const id = path.split('/').at(-1)
    const current = users.find((item) => item.id === id)
    if (current && request.method() === 'PATCH') {
      const updated = { ...current, ...request.postDataJSON() }
      users = users.map((item) => item.id === id ? updated : item)
      return response(updated)
    }
    if (current) return response(current)
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: 'not found' } } })
  })
}

async function login(page: Page, email = 'admin@example.com', password = 'test-password'): Promise<void> {
  await page.goto('/users')
  await expect(page.getByRole('heading', { name: '登录管理控制台' })).toBeVisible()
  await page.getByLabel('管理员邮箱').fill(email)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '进入控制台' }).click()
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible()
}

test('login, users, conflict, edit, disable, audit and logout', async ({ page }) => {
  await mockBackend(page)
  await page.goto('/login')
  await page.screenshot({ path: 'test-results/login-desktop.png', fullPage: true })
  await login(page)
  await expect(page.getByText('运行正常')).toBeVisible()
  await page.screenshot({ path: 'test-results/overview-desktop.png', fullPage: true })
  await page.getByRole('link', { name: '用户管理', exact: true }).click()
  await page.getByRole('button', { name: '创建用户', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('显示名称').fill('新用户')
  await dialog.getByLabel('邮箱地址').fill('new@example.com')
  await dialog.getByRole('button', { name: '确认创建' }).click()
  await expect(page.getByRole('status').filter({ hasText: '测试用户已创建' })).toBeVisible()
  const row = page.getByRole('row').filter({ hasText: 'new@example.com' })
  await expect(row).toBeVisible()
  await row.getByRole('button', { name: '编辑', exact: true }).click()
  await dialog.getByLabel('显示名称').fill('已编辑用户')
  await dialog.getByRole('button', { name: '保存修改' }).click()
  await expect(row).toContainText('已编辑用户')
  await row.getByRole('button', { name: '停用', exact: true }).click()
  await dialog.getByRole('button', { name: '确认停用' }).click()
  await expect(row).toContainText('已停用')
  await row.getByRole('button', { name: '详情', exact: true }).click()
  await expect(dialog).toContainText('cccccccc-cccc-4ccc-8ccc-cccccccccccc')
  await dialog.getByRole('button', { name: '关闭', exact: true }).click()
  await page.getByRole('button', { name: '创建用户', exact: true }).click()
  await dialog.getByLabel('显示名称').fill('重复用户')
  await dialog.getByLabel('邮箱地址').fill('new@example.com')
  await dialog.getByRole('button', { name: '确认创建' }).click()
  await expect(dialog.getByRole('alert')).toContainText('该邮箱已存在')
  await dialog.getByRole('button', { name: '取消' }).click()
  await page.getByRole('link', { name: '操作审计', exact: true }).click()
  await expect(page.getByRole('heading', { name: /^操作记录/ })).toBeVisible()
  await page.getByRole('button', { name: '详情', exact: true }).click()
  await expect(dialog).toContainText('audit-trace')
  await dialog.getByRole('button', { name: '关闭', exact: true }).click()
  await page.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByRole('heading', { name: '登录管理控制台' })).toBeVisible()
})

test('refresh restores and verifies a session; 401 returns to login', async ({ page }) => {
  await mockBackend(page)
  await login(page)
  await page.reload()
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible()
  await expect(page.getByText('运行正常')).toBeVisible()
  await page.route('**/api/v1/admin/users?**', (route) => route.fulfill({ status: 401, json: { error: { code: 'unauthorized' }, request_id: 'expired' } }))
  await page.getByRole('link', { name: '用户管理', exact: true }).click()
  await expect(page.getByRole('heading', { name: '登录管理控制台' })).toBeVisible()
})

test('mobile navigation and dialogs fit the viewport', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockBackend(page)
  await login(page)
  await page.getByRole('button', { name: '展开导航' }).click()
  await page.getByRole('link', { name: '用户管理', exact: true }).click()
  await page.getByRole('button', { name: '创建用户', exact: true }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await expect(page.getByRole('dialog').getByLabel('显示名称')).toBeVisible()
  await page.screenshot({ path: 'test-results/users-mobile.png', fullPage: true })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('real Go backend through the Vite proxy', async ({ page }) => {
  const password = process.env.FCARD_E2E_PASSWORD
  test.skip(!password, 'run scripts/test-frontend-integration.ps1 to use an isolated Go backend')
  await login(page, 'e2e@fcard.test', password!)
  await expect(page.getByText('运行正常')).toBeVisible()
  await page.getByRole('link', { name: '用户管理', exact: true }).click()
  await page.getByRole('button', { name: '创建用户', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('显示名称').fill('真实联调用户')
  await dialog.getByLabel('邮箱地址').fill('real-e2e@example.com')
  await dialog.getByRole('button', { name: '确认创建' }).click()
  const row = page.getByRole('row').filter({ hasText: 'real-e2e@example.com' })
  await expect(row).toBeVisible()
  await row.getByRole('button', { name: '编辑', exact: true }).click()
  await dialog.getByLabel('显示名称').fill('联调已编辑')
  await dialog.getByRole('button', { name: '保存修改' }).click()
  await expect(row).toContainText('联调已编辑')
  await row.getByRole('button', { name: '停用', exact: true }).click()
  await dialog.getByRole('button', { name: '确认停用' }).click()
  await expect(row).toContainText('已停用')
  await page.getByRole('link', { name: '操作审计', exact: true }).click()
  await expect(page.getByRole('row').filter({ hasText: 'user.create' })).toBeVisible()
  await page.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByRole('heading', { name: '登录管理控制台' })).toBeVisible()
})
