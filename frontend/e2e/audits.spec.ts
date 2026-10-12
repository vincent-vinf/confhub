import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import type { ConfigState } from '../src/lib/types'

const origin = 'http://127.0.0.1:18080'
const headers = { Origin: origin }
async function login(page: Page) {
  const res = await page.request.post('/api/admin/login', {
    headers,
    data: { username: 'admin', password: 'ui-test-password' },
  })
  expect(res.ok()).toBeTruthy()
}
async function choose(page: Page, label: string, value: string) {
  const trigger = page.getByRole('combobox', { name: label, exact: true })
  await trigger.click()
  await page.getByRole('option', { name: value, exact: true }).click()
  await expect(page.getByRole('listbox')).not.toBeVisible()
  await expect(trigger).toBeFocused()
}
async function query(page: Page, button = '查询') {
  const response = page.waitForResponse(
    (r) => r.url().includes('/api/admin/audits?') && r.request().method() === 'GET',
  )
  await page.getByRole('button', { name: button, exact: true }).click()
  expect((await response).ok()).toBeTruthy()
  await expect(page.locator('.audit-table tbody')).toBeVisible()
}

test('审计真实操作、组合筛选、详情及游标分页', async ({ page }, testInfo) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await login(page)
  const path = '/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/audit-flow.json'
  let state: ConfigState | undefined
  for (let i = 0; i < 27; i++) {
    const res = await page.request.put(path, {
      headers,
      data: {
        content: JSON.stringify({ secret: `audit-config-secret-${i}` }),
        format: 'json',
        confirmed: true,
        ...(state ? { expected_id: state.id, expected_revision: state.revision } : {}),
      },
    })
    expect(res.ok()).toBeTruthy()
    state = (await res.json()).state
  }
  const conflict = await page.request.put(path, {
    headers,
    data: {
      content: '{}',
      format: 'json',
      confirmed: true,
      expected_id: state!.id,
      expected_revision: state!.revision - 1,
    },
  })
  expect(conflict.status()).toBe(409)
  const denied = await page.request.post('/api/admin/login', {
    headers,
    data: { username: 'audit-attempt', password: 'audit-password-secret' },
  })
  expect(denied.status()).toBe(401)
  await page.goto('/audits')
  await expect(page.getByRole('heading', { name: '操作审计', exact: true })).toBeVisible()
  await expect(
    page.getByRole('cell', { name: 'audit-attempt（尝试登录）', exact: true }),
  ).toBeVisible()
  const firstIDs = await page
    .locator('.audit-table button')
    .evaluateAll((buttons) => buttons.map((b) => b.getAttribute('aria-label')))
  await query(page, '下一页审计')
  await expect(page.getByText('第 2 页', { exact: true })).toBeVisible()
  const secondIDs = await page
    .locator('.audit-table button')
    .evaluateAll((buttons) => buttons.map((b) => b.getAttribute('aria-label')))
  expect(secondIDs.every((id) => !firstIDs.includes(id))).toBe(true)
  await query(page, '上一页审计')
  await page.getByLabel('用户', { exact: true }).fill('admin')
  await page.getByLabel('来源 IP', { exact: true }).fill('127.0.0.1')
  await choose(page, '筛选操作', '全量发布')
  await choose(page, '筛选结果', '失败')
  await query(page)
  const rows = page.locator('.audit-table tbody tr')
  await expect(rows).toHaveCount(1)
  await expect(rows.first()).toContainText('audit-flow.json')
  await rows.first().getByRole('button').click()
  const detail = page.getByRole('dialog')
  await expect(detail).toContainText('HTTP 409')
  await expect(detail).toContainText('请求 ID')
  await expect(detail).toContainText('127.0.0.1')
  await expect(detail).not.toContainText('audit-config-secret')
  await expect(detail).not.toContainText('audit-password-secret')
  expect((await new AxeBuilder({ page }).include('[role="dialog"]').analyze()).violations).toEqual(
    [],
  )
  await detail.getByRole('button', { name: '关闭', exact: true }).click()
  await page.getByLabel('开始时间', { exact: true }).fill('2099-01-01T00:00')
  await page.getByLabel('结束时间', { exact: true }).fill('2098-01-01T00:00')
  await page.getByRole('button', { name: '查询', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('结束时间应晚于开始时间')
  await page.getByLabel('结束时间', { exact: true }).fill('')
  const response = page.waitForResponse(
    (r) => r.url().includes('/api/admin/audits?') && r.request().method() === 'GET',
  )
  await page.getByRole('button', { name: '查询', exact: true }).click()
  expect((await response).ok()).toBeTruthy()
  await expect(page.getByText('没有符合条件的审计记录')).toBeVisible()
  await query(page, '重置筛选')
  await expect(page.getByLabel('用户', { exact: true })).toHaveValue('')
  await expect(page.getByText('第 1 页', { exact: true })).toBeVisible()
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('audits-light.png') })
  await page.getByRole('button', { name: '切换深色主题' }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.setViewportSize({ width: 375, height: 812 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  )
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('audits-dark-mobile.png') })
})

test('普通用户不能查看审计，删除账号后保留操作身份', async ({ page, browser }) => {
  await login(page)
  const created = await page.request.post('/api/admin/users', {
    headers,
    data: {
      username: 'audit-reader',
      password: 'audit-reader-password',
      enabled: true,
      grants: [{ namespace: 'public', access: 'read' }],
    },
  })
  expect(created.ok()).toBeTruthy()
  const user = await created.json()
  const context = await browser.newContext({ baseURL: origin })
  try {
    const reader = await context.newPage()
    expect(
      (
        await reader.request.post('/api/admin/login', {
          headers,
          data: { username: user.username, password: 'audit-reader-password' },
        })
      ).ok(),
    ).toBeTruthy()
    await reader.goto('/configs')
    await expect(reader.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
    await expect(reader.getByRole('link', { name: '操作审计', exact: true })).not.toBeVisible()
    expect((await reader.request.get('/api/admin/audits')).status()).toBe(403)
    await reader.goto('/audits')
    await expect(reader.getByRole('alert')).toContainText('没有访问此页面的权限')
    const deleted = await page.request.delete(`/api/admin/users/${user.id}`, {
      headers,
      data: { confirmed: true, expected_revision: user.revision },
    })
    expect(deleted.ok()).toBeTruthy()
    await page.goto('/audits')
    await page.getByLabel('用户', { exact: true }).fill('audit-reader')
    await query(page)
    await expect(page.locator('.audit-table tbody tr')).toHaveCount(1)
    await expect(page.locator('.audit-table tbody')).toContainText('登录')
    await page.locator('.audit-table tbody button').click()
    await expect(page.getByRole('dialog')).toContainText(user.id)
  } finally {
    await context.close()
  }
})
