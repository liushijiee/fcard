import { expect, test } from '@playwright/test'

test('Fcard wallet entry points reflect current capabilities on desktop and mobile', async ({ page }) => {
  const profile = { id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', email: 'demo@fcard.test', display_name: 'Fcard 用户', status: 'ACTIVE', must_change_password: false, created_at: '2026-10-15T02:00:00Z' }
  await page.addInitScript(profile => {
    sessionStorage.setItem('fcard.user.session', JSON.stringify({ access_token: 'ui-token', token_type: 'Bearer', expires_at: new Date(Date.now() + 3600000).toISOString(), user: profile }))
  }, profile)
  await page.route('**/api/v1/user/**', async route => {
    const path = new URL(route.request().url()).pathname
    const data = path.endsWith('/auth/me') ? profile
      : path.endsWith('/cards/config') ? { enabled: false, company_configured: false, limit_cents: 1000, currency: 'USD', max_cards: 1, occupied_slots: 0 }
      : path.endsWith('/deposits/config') ? { available: false, enabled: false, environment: 'nile', balance_crediting: false }
      : path.endsWith('/cards') ? { items: [], total: 0, page: 1, page_size: 20 } : null
    if (!data) throw new Error(`Unexpected API call: ${path}`)
    await route.fulfill({ json: { data } })
  })
  await page.goto('/portal')
  await expect(page.getByRole('heading', { name: '资产估值' })).toBeVisible()
  await expect(page.getByText('钱包余额尚未接入')).toBeVisible()
  await expect(page.getByText('你好，Fcard 用户')).toBeVisible()
  await expect(page.getByText('礼品券', { exact: true })).toHaveCount(0)
  await expect(page.getByText('邀请好友', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '隐藏资产' }).click()
  await expect(page.locator('.wallet-amount')).toContainText('••••')
  await page.getByRole('button', { name: '显示资产' }).click()
  await expect(page.locator('.wallet-amount')).toContainText('—')
  await page.getByRole('button', { name: '提币' }).click()
  await expect(page.getByRole('status')).toContainText('提币暂未开放')
  await page.getByRole('link', { name: '账单 资金流水' }).click()
  await expect(page).toHaveURL(/#recent-bills$/)
  await page.screenshot({ path: 'test-results/fcard-home-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: 'test-results/fcard-home-mobile.png', fullPage: true })
  await page.getByRole('link', { name: '充币 Nile 测试' }).click()
  await expect(page).toHaveURL(/\/portal\/deposits$/)
  await page.goto('/portal/cards')
  await expect(page.getByRole('heading', { name: '让支付，多一种可能' })).toBeVisible()
  await page.getByRole('link', { name: '申请我的卡片' }).click()
  await expect(page).toHaveURL(/#card-application$/)
  await expect(page.getByRole('button', { name: '申请沙盒虚拟卡' })).toBeDisabled()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: 'test-results/fcard-cards-mobile.png', fullPage: true })
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.screenshot({ path: 'test-results/fcard-cards-desktop.png', fullPage: true })
})
