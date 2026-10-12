import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const origin = 'http://127.0.0.1:18080'
const headers = { Origin: origin }
const path = (namespace: string, group: string, name: string) =>
  `/api/admin/namespaces/${namespace}/groups/${group}/configs/${name}`

async function choose(page: Page, label: string, value: string) {
  const trigger = page.getByRole('combobox', { name: label, exact: true })
  await trigger.click()
  await page.getByRole('option', { name: value, exact: true }).click()
  await expect(page.getByRole('listbox')).not.toBeVisible()
  await expect(trigger).toBeFocused()
}

async function setup(page: Page) {
  await page.goto('/configs')
  await page.getByLabel('密码', { exact: true }).fill('ui-test-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  const source = `batch-source-${Date.now()}`
  const destination = `batch-target-${Date.now()}`
  expect(
    (
      await page.request.post('/api/admin/namespaces/public/groups', {
        headers,
        data: { name: source },
      })
    ).ok(),
  ).toBeTruthy()
  expect(
    (
      await page.request.post('/api/admin/namespaces', { headers, data: { name: destination } })
    ).ok(),
  ).toBeTruthy()
  expect(
    (
      await page.request.post(`/api/admin/namespaces/${destination}/groups`, {
        headers,
        data: { name: 'apps' },
      })
    ).ok(),
  ).toBeTruthy()
  const names = ['a.json', 'b.json', 'c.json']
  for (const name of names) {
    const response = await page.request.put(path('public', source, name), {
      headers,
      data: {
        expected_id: '',
        expected_revision: 0,
        content: '{"v":1}',
        format: 'json',
        description: '',
        confirmed: true,
      },
    })
    expect(response.ok()).toBeTruthy()
  }
  await page.goto(`/configs?namespace=public&group=${source}`)
  await expect(page.getByRole('link', { name: '打开 a.json', exact: true })).toBeVisible()
  return { source, destination, names }
}

test('批量选择、取消、删除确认以及明暗窄屏可访问性', async ({ page }, testInfo) => {
  const { source, names } = await setup(page)
  const all = page.getByRole('checkbox', { name: '全选当前页配置', exact: true })
  await expect(page.getByRole('group', { name: '批量配置操作' })).toHaveCount(0)
  await page.getByRole('checkbox', { name: '选择 a.json', exact: true }).focus()
  await page.keyboard.press('Space')
  await expect(all).toHaveJSProperty('indeterminate', true)
  await expect(page.getByRole('group', { name: '批量配置操作' })).toContainText('已选择 1 项')
  await all.check()
  await expect(page.getByRole('group', { name: '批量配置操作' })).toContainText('已选择 3 项')
  await page.getByRole('button', { name: '取消选择', exact: true }).click()
  await expect(all).not.toBeChecked()
  await all.check()
  await page.getByRole('textbox', { name: '搜索本分组配置' }).fill('a.json')
  await expect(page.getByRole('link', { name: '打开 b.json', exact: true })).toHaveCount(0)
  await expect(page.getByRole('group', { name: '批量配置操作' })).toHaveCount(0)
  await page.getByRole('button', { name: '清除搜索', exact: true }).click()
  await expect(page.getByRole('link', { name: '打开 b.json', exact: true })).toBeVisible()
  await all.check()
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.setViewportSize({ width: 375, height: 812 })
  for (const dark of [false, true]) {
    if (dark) await page.getByRole('button', { name: '切换深色主题' }).click()
    await page.getByRole('button', { name: '删除配置', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: '批量删除配置', exact: true })
    await expect(dialog.getByRole('button', { name: '确认删除', exact: true })).toBeDisabled()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.screenshot({
      path: testInfo.outputPath(dark ? 'delete-mobile-dark.png' : 'delete-mobile-light.png'),
    })
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBeTruthy()
    await page.getByRole('button', { name: '取消', exact: true }).click()
    await expect(page.getByRole('button', { name: '删除配置', exact: true })).toBeFocused()
  }
  await page.getByRole('button', { name: '删除配置', exact: true }).click()
  await page.getByRole('checkbox', { name: '确认删除这 3 份配置及全部历史和灰度规则' }).check()
  await page.getByRole('button', { name: '确认删除', exact: true }).click()
  await expect(page.getByRole('dialog')).not.toBeVisible()
  await expect(page.getByRole('heading', { name: '这个分组还没有配置' })).toBeVisible()
  for (const name of names)
    expect((await page.request.get(path('public', source, name))).status()).toBe(404)
})

test('批量移动联动选择目标，保留历史与 beta', async ({ page }, testInfo) => {
  const { source, destination, names } = await setup(page)
  const sourcePath = path('public', source, names[0])
  let state = await (await page.request.get(sourcePath)).json()
  let response = await page.request.put(sourcePath, {
    headers,
    data: {
      expected_id: state.id,
      expected_revision: state.revision,
      content: '{"v":2}',
      format: 'json',
      description: '',
      confirmed: true,
    },
  })
  expect(response.ok()).toBeTruthy()
  state = (await response.json()).state
  response = await page.request.put(`${sourcePath}/rules`, {
    headers,
    data: {
      expected_id: state.id,
      expected_revision: state.revision,
      source_version: 1,
      rules: [
        {
          id: 'gray',
          name: 'gray',
          enabled: true,
          conditions: [{ tag: 'env', operator: 'eq', values: ['gray'] }],
        },
      ],
      confirmed: true,
    },
  })
  expect(response.ok()).toBeTruthy()
  state = (await response.json()).state
  await page.getByRole('button', { name: '刷新配置列表', exact: true }).click()
  await expect(
    page
      .getByRole('row')
      .filter({ has: page.getByRole('link', { name: '打开 a.json', exact: true }) }),
  ).toContainText('v2')
  await page.getByRole('checkbox', { name: '全选当前页配置', exact: true }).check()
  await page.screenshot({ path: testInfo.outputPath('selection-desktop.png') })
  await page.getByRole('button', { name: '移动配置', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '移动配置', exact: true })
  await expect(dialog.getByRole('button', { name: '确认移动', exact: true })).toBeDisabled()
  await dialog.getByRole('combobox', { name: '目标分组', exact: true }).click()
  await expect(page.getByRole('option', { name: source, exact: true })).toHaveAttribute(
    'data-disabled',
    '',
  )
  await page.keyboard.press('Escape')
  await expect(dialog.getByRole('combobox', { name: '目标分组', exact: true })).toBeFocused()
  await choose(page, '目标命名空间', destination)
  await expect(dialog.getByRole('combobox', { name: '目标分组', exact: true })).toContainText(
    '请选择分组',
  )
  await choose(page, '目标分组', 'apps')
  await page.screenshot({ path: testInfo.outputPath('move-desktop.png') })
  await dialog.getByRole('button', { name: '确认移动', exact: true }).click()
  await expect(dialog).not.toBeVisible()
  await expect(page.getByRole('group', { name: '批量配置操作' })).toHaveCount(0)
  for (const name of names) {
    expect((await page.request.get(path('public', source, name))).status()).toBe(404)
    const moved = await (await page.request.get(path(destination, 'apps', name))).json()
    if (name === names[0]) {
      expect(moved).toMatchObject({
        id: state.id,
        global_version: 2,
        last_version: 2,
        beta: state.beta,
        rules: state.rules,
      })
      expect(
        (await (await page.request.get(`${path(destination, 'apps', name)}/versions/1`)).json())
          .content,
      ).toBe('{"v":1}')
    }
  }
})

test('目标重名与并发编辑拒绝整批操作，刷新后清空选择', async ({ page }) => {
  const { source, destination } = await setup(page)
  const collision = await page.request.put(path(destination, 'apps', 'b.json'), {
    headers,
    data: {
      expected_id: '',
      expected_revision: 0,
      content: '{"keep":true}',
      format: 'json',
      confirmed: true,
    },
  })
  expect(collision.ok()).toBeTruthy()
  for (const name of ['a.json', 'b.json'])
    await page.getByRole('checkbox', { name: `选择 ${name}`, exact: true }).check()
  await page.getByRole('button', { name: '移动配置', exact: true }).click()
  await choose(page, '目标命名空间', destination)
  await choose(page, '目标分组', 'apps')
  await page.getByRole('button', { name: '确认移动', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('目标分组已存在同名配置，整批移动未执行')
  expect((await page.request.get(path(destination, 'apps', 'a.json'))).status()).toBe(404)
  await page.getByRole('button', { name: '取消', exact: true }).click()
  const initial = await (await page.request.get(path('public', source, 'b.json'))).json()
  expect(
    (
      await page.request.put(path('public', source, 'b.json'), {
        headers,
        data: {
          expected_id: initial.id,
          expected_revision: initial.revision,
          content: '{"v":3}',
          format: 'json',
          confirmed: true,
        },
      })
    ).ok(),
  ).toBeTruthy()
  await page.getByRole('button', { name: '删除配置', exact: true }).click()
  await page.getByRole('checkbox', { name: '确认删除这 2 份配置及全部历史和灰度规则' }).check()
  await page.getByRole('button', { name: '确认删除', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('配置或分组已发生变化，本次操作未执行')
  for (const name of ['a.json', 'b.json'])
    expect((await page.request.get(path('public', source, name))).ok()).toBeTruthy()
  await page.getByRole('button', { name: '取消', exact: true }).click()
  await page.getByRole('button', { name: '刷新配置列表', exact: true }).click()
  await expect(page.getByRole('group', { name: '批量配置操作' })).toHaveCount(0)
})

test('当前页全选，翻页与切换分组清空选择', async ({ page }) => {
  const { source } = await setup(page)
  for (let i = 0; i < 23; i++) {
    const response = await page.request.put(
      path('public', source, `extra-${String(i).padStart(2, '0')}.json`),
      {
        headers,
        data: {
          expected_id: '',
          expected_revision: 0,
          content: '{}',
          format: 'json',
          confirmed: true,
        },
      },
    )
    expect(response.ok()).toBeTruthy()
  }
  await page.getByRole('button', { name: '刷新配置列表', exact: true }).click()
  await expect(page.getByRole('row')).toHaveCount(26)
  await page.getByRole('checkbox', { name: '全选当前页配置', exact: true }).check()
  await expect(page.getByRole('group', { name: '批量配置操作' })).toContainText('已选择 25 项')
  await page.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(page.getByRole('row')).toHaveCount(2)
  await expect(page.getByRole('group', { name: '批量配置操作' })).toHaveCount(0)
  await expect(
    page.getByRole('checkbox', { name: '全选当前页配置', exact: true }),
  ).not.toBeChecked()
  await page.getByRole('checkbox', { name: '全选当前页配置', exact: true }).check()
  await expect(page.getByRole('group', { name: '批量配置操作' })).toContainText('已选择 1 项')
  await page.getByRole('button', { name: '上一页', exact: true }).click()
  await expect(page.getByRole('row')).toHaveCount(26)
  await expect(page.getByRole('group', { name: '批量配置操作' })).toHaveCount(0)
  await page.getByRole('checkbox', { name: '选择 a.json', exact: true }).check()
  await choose(page, '分组', 'DEFAULT_GROUP')
  await expect(page.getByRole('group', { name: '批量配置操作' })).toHaveCount(0)
  await choose(page, '分组', source)
  await expect(page.getByRole('checkbox', { name: '选择 a.json', exact: true })).not.toBeChecked()
})
