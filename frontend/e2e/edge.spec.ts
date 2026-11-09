import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const cloud = 'http://127.0.0.1:18080'
const edge = 'http://127.0.0.1:18090'
const key = { namespace: 'public', group: 'DEFAULT_GROUP', name: 'edge-console.yaml' }
const path = `/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/${key.name}`

test('边缘登录后只读查看镜像、beta、规则、客户端及同步状态', async ({ page, request }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(
    (
      await page.request.post(`${cloud}/api/admin/login`, {
        headers: { Origin: cloud },
        data: { username: 'admin', password: 'ui-test-password' },
      })
    ).ok(),
  ).toBeTruthy()
  let state: { id: string; revision: number }
  const save = async (content: string, target?: 'beta') => {
    const response = await page.request.put(cloud + path, {
      headers: { Origin: cloud },
      data: {
        expected_id: state?.id ?? '',
        expected_revision: state?.revision ?? 0,
        confirmed: true,
        content,
        format: 'yaml',
        description: '边缘只读体验',
        target,
      },
    })
    expect(response.ok()).toBeTruthy()
    state = (await response.json()).state
  }
  await save('message: first\n')
  await save('message: current\n')
  const rules = await page.request.put(cloud + path + '/rules', {
    headers: { Origin: cloud },
    data: {
      expected_id: state!.id,
      expected_revision: state!.revision,
      confirmed: true,
      source_version: 1,
      rules: [
        {
          id: 'edge-beta',
          name: 'beta 客户端',
          enabled: true,
          conditions: [{ tag: 'env', operator: 'eq', values: ['beta'] }],
        },
      ],
    },
  })
  expect(rules.ok()).toBeTruthy()
  state = (await rules.json()).state
  await save('message: beta\n', 'beta')
  await expect.poll(async () => (await request.get(edge + '/health/ready')).status()).toBe(200)
  expect((await request.get(edge + path)).status()).toBe(401)
  await page.goto(edge + '/configs')
  await page.getByLabel('密码', { exact: true }).fill('wrong-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('alert')).toContainText('用户名或密码不正确')
  await page.getByLabel('密码', { exact: true }).fill('ui-edge-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '导入配置', exact: true })).toHaveCount(0)
  const archive = await page.request.get(
    `${edge}/api/admin/configs/export?namespace=public&group=DEFAULT_GROUP&name=${key.name}`,
  )
  expect(archive.ok()).toBeTruthy()
  expect(archive.headers()['content-type']).toBe('application/zip')
  expect((await archive.body()).subarray(0, 2).toString()).toBe('PK')
  await expect(page.getByText('边缘 · 只读', { exact: true })).toBeVisible()
  for (const name of ['新建配置', '命名空间', '用户管理', '操作审计', '账号设置']) {
    await expect(page.getByRole('link', { name, exact: true })).toHaveCount(0)
  }
  await page.getByRole('link', { name: key.name, exact: true }).click()
  const content = page.getByRole('textbox', { name: '配置内容', exact: true })
  await expect(content).toHaveAttribute('aria-readonly', 'true')
  await expect(content).toHaveText('message: current\n')
  await expect(page.getByLabel('版本说明')).toHaveValue('边缘只读体验')
  await save('message: updated\n')
  await expect(content).toHaveText('message: updated\n')
  for (const name of ['保存并发布', '删除配置', '格式化', '还原编辑']) {
    await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0)
  }
  await page.getByRole('combobox', { name: '查看目标', exact: true }).click()
  await page.getByRole('option', { name: 'beta 配置', exact: true }).click()
  await expect(content).toHaveText('message: beta\n')
  await page.getByRole('tab', { name: '本地版本', exact: true }).click()
  await expect(page.getByRole('button', { name: '查看 v3', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '查看 v1', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '转为全量', exact: true })).toHaveCount(0)
  await page.getByRole('tab', { name: /^灰度规则/ }).click()
  await expect(page.getByRole('heading', { name: 'beta 客户端', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '新增规则', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '停用规则 beta 客户端', exact: true })).toHaveCount(
    0,
  )
  await page.getByLabel('试算标签 1 名称').fill('env')
  await page.getByLabel('试算标签 1 值').fill('beta')
  await page.getByRole('button', { name: '开始试算', exact: true }).click()
  await expect(page.getByText('beta 客户端', { exact: true })).toHaveCount(2)
  for (const operation of ['rollback', 'promote']) {
    expect(
      (
        await page.request.post(edge + path + '/' + operation, {
          headers: { Origin: edge },
          data: { confirmed: true },
        })
      ).status(),
    ).toBe(403)
  }
  expect(
    (
      await page.request.put(edge + path, { headers: { Origin: edge }, data: { confirmed: true } })
    ).status(),
  ).toBe(403)
  expect(
    (
      await page.request.post(edge + '/api/admin/password', { headers: { Origin: edge }, data: {} })
    ).status(),
  ).toBe(403)
  await page.evaluate((key) => {
    const socket = new WebSocket(
      `${location.origin.replace('http', 'ws')}/api/client/watch?tags=${encodeURIComponent(JSON.stringify({ env: 'beta', 'sys.hostname': 'edge-ui-client' }))}`,
    )
    ;(window as unknown as { edgeClient: WebSocket }).edgeClient = socket
    socket.onopen = () => socket.send(JSON.stringify({ op: 'subscribe', key }))
  }, key)
  await page.getByRole('link', { name: '在线客户端', exact: true }).click()
  const card = page.getByTestId('client-card').filter({ hasText: 'edge-ui-client' })
  await expect(card).toBeVisible({ timeout: 15000 })
  await expect(card).toContainText('beta')
  await page.getByRole('link', { name: '同步状态', exact: true }).click()
  await expect(page.getByRole('heading', { name: '同步状态', exact: true })).toBeVisible()
  await expect(page.getByText('可提供服务', { exact: true })).toBeVisible()
  await expect(page.getByText('已连接', { exact: true })).toBeVisible()
  expect(
    (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations,
  ).toEqual([])
  await page.screenshot({ path: '../test-results/edge-console/sync-light.png', fullPage: true })
  await page.getByRole('button', { name: '切换深色主题' }).click()
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(
    (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations,
  ).toEqual([])
  await page.screenshot({
    path: '../test-results/edge-console/sync-mobile-dark.png',
    fullPage: true,
  })
  await page.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByRole('button', { name: '登录控制台' })).toBeVisible()
  expect((await page.request.get(edge + path)).status()).toBe(401)
  // Logging in/out at the edge must not overwrite the cloud's browser session.
  expect((await page.request.get(cloud + '/api/admin/me')).status()).toBe(200)
})

test('无灰度规则的配置可从边缘列表打开，查看内容、本地版本并自动刷新', async ({ page }) => {
  const plainKey = { ...key, name: 'edge-no-rules.yaml' }
  const plainPath = path.replace(key.name, plainKey.name)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  expect(
    (
      await page.request.post(cloud + '/api/admin/login', {
        headers: { Origin: cloud },
        data: { username: 'admin', password: 'ui-test-password' },
      })
    ).ok(),
  ).toBeTruthy()
  const created = await page.request.put(cloud + plainPath, {
    headers: { Origin: cloud },
    data: {
      confirmed: true,
      content: 'message: no-rules\n',
      format: 'yaml',
      description: '无灰度规则',
    },
  })
  expect(created.ok()).toBeTruthy()
  const initial = (await created.json()).state
  expect(
    (
      await page.request.post(edge + '/api/admin/login', {
        headers: { Origin: edge },
        data: { username: 'admin', password: 'ui-edge-password' },
      })
    ).ok(),
  ).toBeTruthy()
  await expect.poll(async () => (await page.request.get(edge + plainPath)).status()).toBe(200)
  await page.goto(edge + '/configs')
  await page.getByRole('link', { name: `打开 ${plainKey.name}`, exact: true }).click()
  const content = page.getByRole('textbox', { name: '配置内容', exact: true })
  await expect(content).toHaveText('message: no-rules\n')
  await expect(content).toHaveAttribute('aria-readonly', 'true')
  await expect(page.getByRole('heading', { name: '页面暂时无法显示' })).toHaveCount(0)
  expect((await (await page.request.get(edge + plainPath)).json()).rules).toEqual([])
  await page.getByRole('tab', { name: /^灰度规则/ }).click()
  await expect(page.getByRole('heading', { name: '当前使用全量发布' })).toBeVisible()
  await expect(page.getByRole('button', { name: '新增规则', exact: true })).toHaveCount(0)
  await page.getByRole('tab', { name: '本地版本', exact: true }).click()
  await expect(page.getByRole('button', { name: '查看 v1', exact: true })).toBeVisible()
  await page.getByRole('tab', { name: '配置内容', exact: true }).click()
  expect(
    (
      await page.request.put(cloud + plainPath, {
        headers: { Origin: cloud },
        data: {
          expected_id: initial.id,
          expected_revision: initial.revision,
          confirmed: true,
          content: 'message: updated-no-rules\n',
          format: 'yaml',
        },
      })
    ).ok(),
  ).toBeTruthy()
  await expect(content).toHaveText('message: updated-no-rules\n')
  await page.reload()
  await expect(content).toHaveText('message: updated-no-rules\n')
  await expect(page.getByRole('button', { name: '保存并发布', exact: true })).toHaveCount(0)
  expect(errors).toEqual([])
})
