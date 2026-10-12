import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
const origin = 'http://127.0.0.1:18080'
const adminPassword = 'ui-test-password'
async function adminLogin(page: Page) {
  const res = await page.request.post('/api/admin/login', {
    headers: { Origin: origin },
    data: { username: 'admin', password: adminPassword },
  })
  expect(res.ok()).toBeTruthy()
  await page.goto('/users')
  await expect(page.getByRole('heading', { name: '用户管理', exact: true })).toBeVisible()
}
async function choose(page: Page, label: string, option: string) {
  const trigger = page.getByRole('combobox', { name: label, exact: true })
  await trigger.click()
  await page.getByRole('option', { name: option, exact: true }).click()
  await expect(page.getByRole('listbox')).not.toBeVisible()
  await expect(trigger).toBeFocused()
}
async function confirmAction(page: Page, label: string) {
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('checkbox').check()
  await dialog.getByRole('button', { name: label, exact: true }).click()
  await expect(dialog).not.toBeVisible()
}
test('创建用户、范围授权、只读浏览及权限更新后的编辑', async ({ page, browser }, testInfo) => {
  await adminLogin(page)
  const name = 'permission-ui.json',
    path = `/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/${name}`
  let res = await page.request.put(path, {
    headers: { Origin: origin },
    data: { content: '{"v":1}', format: 'json', confirmed: true },
  })
  expect(res.ok()).toBeTruthy()
  let state = (await res.json()).state
  res = await page.request.put(path, {
    headers: { Origin: origin },
    data: {
      content: '{"v":2}',
      format: 'json',
      confirmed: true,
      expected_id: state.id,
      expected_revision: state.revision,
    },
  })
  expect(res.ok()).toBeTruthy()
  state = (await res.json()).state
  res = await page.request.put(`${path}/rules`, {
    headers: { Origin: origin },
    data: {
      expected_id: state.id,
      expected_revision: state.revision,
      source_version: 1,
      confirmed: true,
      rules: [
        {
          id: 'test-rule',
          name: '测试灰度',
          enabled: true,
          conditions: [{ tag: 'env', operator: 'eq', values: ['test'] }],
        },
      ],
    },
  })
  expect(res.ok()).toBeTruthy()
  await page.getByRole('button', { name: '创建用户', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('用户名', { exact: true }).fill('ui-reader')
  await dialog.getByLabel('初始密码', { exact: true }).fill('reader-password')
  await dialog.getByRole('button', { name: '添加授权', exact: true }).click()
  await choose(page, '授权 1 命名空间', 'public')
  await choose(page, '授权 1 分组', 'DEFAULT_GROUP')
  expect((await new AxeBuilder({ page }).include('[role="dialog"]').analyze()).violations).toEqual(
    [],
  )
  await page.setViewportSize({ width: 375, height: 812 })
  await page.screenshot({ path: testInfo.outputPath('user-create-mobile.png') })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  )
  await dialog.getByRole('button', { name: '创建用户', exact: true }).click()
  await expect(dialog).not.toBeVisible()
  const row = page
    .getByRole('row')
    .filter({ has: page.getByRole('cell', { name: 'ui-reader', exact: true }) })
  await expect(row).toContainText('只读')
  await page.setViewportSize({ width: 1440, height: 1000 })
  const context = await browser.newContext({ baseURL: origin })
  const reader = await context.newPage()
  try {
    await reader.goto('/configs')
    await reader.getByLabel('用户名', { exact: true }).fill('ui-reader')
    await reader.getByLabel('密码', { exact: true }).fill('reader-password')
    await reader.getByRole('button', { name: '登录控制台' }).click()
    await expect(reader.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
    await expect(reader.getByRole('link', { name: '新建配置', exact: true })).not.toBeVisible()
    await expect(reader.getByRole('checkbox', { name: '全选当前页配置' })).not.toBeVisible()
    await expect(reader.getByRole('link', { name: '用户管理', exact: true })).not.toBeVisible()
    await expect(reader.getByRole('link', { name: '在线客户端', exact: true })).not.toBeVisible()
    await reader.goto(`/configs/public/DEFAULT_GROUP/${name}`)
    await expect(reader.getByRole('textbox', { name: '配置内容', exact: true })).toHaveAttribute(
      'aria-readonly',
      'true',
    )
    await expect(reader.getByRole('button', { name: '保存并发布', exact: true })).not.toBeVisible()
    await expect(reader.getByRole('button', { name: '删除配置', exact: true })).not.toBeVisible()
    await reader.getByRole('tab', { name: '版本历史' }).click()
    await expect(reader.getByRole('button', { name: '回退到 v1', exact: true })).not.toBeVisible()
    await expect(reader.getByRole('button', { name: '转为全量', exact: true })).not.toBeVisible()
    await reader.getByRole('button', { name: '比较 v1', exact: true }).click()
    await expect(reader.getByRole('dialog').getByTestId('config-diff')).toBeVisible()
    await reader.getByRole('dialog').getByRole('button', { name: '关闭', exact: true }).click()
    await reader.getByRole('button', { name: '查看 beta', exact: true }).click()
    await expect(reader.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(
      '{"v":1}',
    )
    await reader.getByRole('tab', { name: /灰度规则/ }).click()
    await expect(
      reader.getByRole('button', { name: '新增灰度规则', exact: true }),
    ).not.toBeVisible()
    await reader.getByRole('button', { name: '开始试算', exact: true }).click()
    await expect(reader.locator('.simulation-result')).toContainText('使用全量配置')
    expect((await reader.request.get('/api/admin/client-tags')).status()).toBe(403)
    await reader.goto('/users')
    await expect(reader.getByRole('alert')).toContainText('没有访问此页面的权限')
    await row.getByRole('button', { name: '编辑权限', exact: true }).click()
    await choose(page, '授权 1 权限', '读写')
    await page.getByRole('dialog').getByRole('button', { name: '保存权限', exact: true }).click()
    await expect(page.getByRole('dialog')).not.toBeVisible()
    await reader.goto(`/configs/public/DEFAULT_GROUP/${name}`)
    await expect(reader.getByRole('textbox', { name: '配置内容', exact: true })).toHaveAttribute(
      'aria-readonly',
      'false',
    )
    await reader.getByRole('textbox', { name: '配置内容', exact: true }).fill('{"v":3}')
    await reader.getByRole('button', { name: '保存并发布', exact: true }).click()
    const review = reader.getByRole('dialog')
    await review.getByRole('checkbox').check()
    await review.getByRole('button', { name: '确认发布', exact: true }).click()
    await expect(review).not.toBeVisible()
    expect((await (await reader.request.get(path)).json()).global_version).toBe(3)
    await row.getByRole('button', { name: '禁用', exact: true }).click()
    await confirmAction(page, '确认禁用用户')
    expect((await reader.request.get('/api/admin/me')).status()).toBe(401)
    await reader.getByRole('tab', { name: '版本历史' }).click()
    // Viewing the newly published version forces a request rather than using cached metadata.
    await reader.getByRole('button', { name: '查看 v3', exact: true }).click()
    await expect(reader.getByRole('heading', { name: '登录已过期', exact: true })).toBeVisible()
    await reader.getByRole('button', { name: '放弃编辑并退出', exact: true }).click()
    await expect(reader.getByLabel('用户名', { exact: true })).toBeEditable()
    await row.getByRole('button', { name: '启用', exact: true }).click()
    await confirmAction(page, '确认启用用户')
    expect((await reader.request.get('/api/admin/me')).status()).toBe(401)
    await row.getByRole('button', { name: '重置密码', exact: true }).click()
    await page
      .getByRole('dialog')
      .getByLabel('新密码', { exact: true })
      .fill('replacement-password')
    await confirmAction(page, '确认重置密码')
    await reader.goto('/configs')
    await reader.getByLabel('用户名', { exact: true }).fill('ui-reader')
    await reader.getByLabel('密码', { exact: true }).fill('reader-password')
    await reader.getByRole('button', { name: '登录控制台' }).click()
    await expect(reader.getByRole('alert')).toContainText('用户名或密码不正确')
    await reader.getByLabel('密码', { exact: true }).fill('replacement-password')
    await reader.getByRole('button', { name: '登录控制台' }).click()
    await expect(reader.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
    await row.getByRole('button', { name: '删除', exact: true }).click()
    await confirmAction(page, '确认删除用户')
    await expect(row).not.toBeVisible()
    expect((await reader.request.get('/api/admin/me')).status()).toBe(401)
  } finally {
    await context.close()
  }
})
test('无授权用户可登录、保护 admin 及深色授权弹窗', async ({ page, browser }, testInfo) => {
  await adminLogin(page)
  const res = await page.request.post('/api/admin/users', {
    headers: { Origin: origin },
    data: { username: 'ui-empty', password: 'empty-password', enabled: true, grants: [] },
  })
  expect(res.ok()).toBeTruthy()
  const context = await browser.newContext({ baseURL: origin })
  const empty = await context.newPage()
  try {
    await empty.goto('/configs')
    await empty.getByLabel('用户名', { exact: true }).fill('ui-empty')
    await empty.getByLabel('密码', { exact: true }).fill('empty-password')
    await empty.getByRole('button', { name: '登录控制台' }).click()
    await expect(empty.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
    await expect(empty.getByRole('alert')).toContainText('不可访问')
    expect((await empty.request.get('/api/admin/namespaces')).status()).toBe(200)
    expect(await (await empty.request.get('/api/admin/namespaces')).json()).toEqual([])
    await empty.goto('/new')
    await expect(empty.getByRole('alert')).toContainText('没有此分组的写入权限')
  } finally {
    await context.close()
  }
  const adminRow = page
    .getByRole('row')
    .filter({ has: page.getByRole('cell', { name: 'admin', exact: true }) })
  await expect(adminRow.getByRole('button', { name: '删除', exact: true })).not.toBeVisible()
  await expect(adminRow.getByRole('button', { name: '禁用', exact: true })).not.toBeVisible()
  await page.getByRole('button', { name: '切换深色主题' }).click()
  await adminRow.getByRole('button', { name: '编辑权限', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('checkbox', { name: '启用账号', exact: true })).toBeDisabled()
  await expect(
    dialog.getByRole('checkbox', { name: '系统管理员（全部权限）', exact: true }),
  ).toBeDisabled()
  expect((await new AxeBuilder({ page }).include('[role="dialog"]').analyze()).violations).toEqual(
    [],
  )
  await page.screenshot({ path: testInfo.outputPath('user-admin-dark.png') })
  await dialog.getByRole('button', { name: '取消', exact: true }).click()
})
