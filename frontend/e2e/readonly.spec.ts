import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const readonly = 'http://127.0.0.1:18082'
const cloud = 'http://127.0.0.1:18080'
const path = '/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/readonly-ui.yaml'

test('只读实例登录、历史与灰度试算、自动刷新和写入限制', async ({ page, request }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(
    (
      await page.request.post(cloud + '/api/admin/login', {
        headers: { Origin: cloud },
        data: { username: 'admin', password: 'ui-test-password' },
      })
    ).ok(),
  ).toBeTruthy()
  let state: { id: string; revision: number }
  const save = async (content: string, target?: 'beta') => {
    const res = await page.request.put(cloud + path, {
      headers: { Origin: cloud },
      data: {
        expected_id: state?.id ?? '',
        expected_revision: state?.revision ?? 0,
        confirmed: true,
        content,
        format: 'yaml',
        description: '只读版本说明',
        target,
      },
    })
    expect(res.ok()).toBeTruthy()
    state = (await res.json()).state
  }
  await save('value: first\n')
  await save('value: current\n')
  const rules = await page.request.put(cloud + path + '/rules', {
    headers: { Origin: cloud },
    data: {
      expected_id: state!.id,
      expected_revision: state!.revision,
      confirmed: true,
      source_version: 1,
      rules: [
        {
          id: 'readonly-beta',
          name: 'beta 客户端',
          enabled: true,
          conditions: [{ tag: 'env', operator: 'eq', values: ['beta'] }],
        },
      ],
    },
  })
  expect(rules.ok()).toBeTruthy()
  state = (await rules.json()).state
  await save('value: beta\n', 'beta')
  await expect.poll(async () => (await request.get(readonly + '/health/ready')).status()).toBe(200)
  expect((await request.get(readonly + path)).status()).toBe(401)
  await page.goto(readonly + '/configs')
  await page.getByLabel('密码', { exact: true }).fill('ui-test-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  await expect(page.getByText('只读模式', { exact: true })).toBeVisible()
  for (const name of ['新建配置', '命名空间', '用户管理', '账号设置', '在线客户端']) {
    await expect(page.getByRole('link', { name, exact: true })).toHaveCount(0)
  }
  await expect(page.getByRole('checkbox')).toHaveCount(0)
  await page.getByRole('textbox', { name: '搜索本分组配置' }).fill('readonly-ui.yaml')
  await page.getByRole('link', { name: 'readonly-ui.yaml', exact: true }).click()
  const content = page.getByRole('textbox', { name: '配置内容', exact: true })
  await expect(content).toHaveAttribute('aria-readonly', 'true')
  await expect(content).toHaveText('value: current\n')
  await expect(page.getByLabel('版本说明')).toHaveValue('只读版本说明')
  for (const name of ['保存并发布', '删除配置', '格式化', '还原编辑']) {
    await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0)
  }
  await save('value: updated\n')
  await expect(content).toHaveText('value: updated\n')
  await page.getByRole('combobox', { name: '查看目标', exact: true }).click()
  await page.getByRole('option', { name: 'beta 配置', exact: true }).click()
  await expect(content).toHaveText('value: beta\n')
  await save('value: beta-updated\n', 'beta')
  await expect(content).toHaveText('value: beta-updated\n')
  await page.getByRole('tab', { name: '版本历史', exact: true }).click()
  await expect(page.getByRole('button', { name: '查看 v1', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '查看 v3', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '转为全量', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /回退到/ })).toHaveCount(0)
  await page.getByRole('tab', { name: /^灰度规则/ }).click()
  await expect(page.getByRole('heading', { name: 'beta 客户端', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '新增规则', exact: true })).toHaveCount(0)
  await expect(page.getByRole('switch')).toHaveCount(0)
  await page.getByLabel('试算标签 1 名称').fill('env')
  await page.getByLabel('试算标签 1 值').fill('beta')
  await page.getByRole('button', { name: '开始试算', exact: true }).click()
  await expect(page.getByText('beta 客户端', { exact: true })).toHaveCount(2)
  for (const [method, route] of [
    ['PUT', path],
    ['POST', path + '/rollback'],
    ['POST', '/api/admin/password'],
  ]) {
    expect(
      (
        await page.request.fetch(readonly + route, {
          method,
          headers: { Origin: readonly },
          data: { confirmed: true },
        })
      ).status(),
    ).toBe(403)
  }
  expect(
    (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations,
  ).toEqual([])
  await page.getByRole('button', { name: '切换深色主题' }).click()
  await page.setViewportSize({ width: 375, height: 812 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(
    (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations,
  ).toEqual([])
  await page.screenshot({ path: '../test-results/readonly/mobile-dark.png', fullPage: true })
  await page.setViewportSize({ width: 812, height: 375 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.goto(readonly + '/new')
  await expect(page.getByRole('alert')).toContainText('你没有访问此页面的权限')
  await page.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByRole('button', { name: '登录控制台' })).toBeVisible()
  expect((await page.request.get(readonly + path)).status()).toBe(401)
  expect((await page.request.get(cloud + '/api/admin/me')).status()).toBe(200)
})
