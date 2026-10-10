import { test, expect, type Page, type APIRequestContext } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const origin = 'http://127.0.0.1:18080'
const password = 'ui-test-password'
const path = (name: string) =>
  `/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/${encodeURIComponent(name)}`
const url = (name: string) => `/configs/public/DEFAULT_GROUP/${encodeURIComponent(name)}`
async function login(page: Page) {
  await page.goto('/configs')
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
}
async function state(request: APIRequestContext, name: string) {
  const response = await request.get(path(name))
  expect(response.ok()).toBeTruthy()
  return response.json()
}
async function save(
  request: APIRequestContext,
  name: string,
  content: string,
  previous?: { id: string; revision: number },
  ruleId?: string,
) {
  const response = await request.put(path(name), {
    headers: { Origin: origin },
    data: {
      expected_id: previous?.id ?? '',
      expected_revision: previous?.revision ?? 0,
      content,
      format: 'json',
      target: ruleId ? 'beta' : undefined,
      description: '',
      confirmed: true,
    },
  })
  expect(response.ok()).toBeTruthy()
  return (await response.json()).state
}
async function edit(page: Page, content: string) {
  await page.getByRole('textbox', { name: '配置内容', exact: true }).fill(content)
}
async function confirm(page: Page, button = '确认发布') {
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByTestId('config-diff').locator('.cm-mergeView')).toBeVisible()
  await expect(dialog.getByRole('button', { name: button, exact: true })).toBeDisabled()
  await dialog.getByRole('checkbox').check()
  await dialog.getByRole('button', { name: button, exact: true }).click()
  await expect(dialog).not.toBeVisible()
}
async function confirmRule(page: Page) {
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('checkbox').check()
  await dialog.getByRole('button', { name: '确认生效', exact: true }).click()
  await expect(dialog).not.toBeVisible()
}

test('登录失败、真实创建、语法校验、格式化撤销和保存确认', async ({ page }) => {
  await page.goto('/configs')
  await page.getByLabel('密码', { exact: true }).fill('wrong-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('alert')).toContainText('用户名或密码不正确')
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录控制台' }).click()
  await page.getByRole('link', { name: '新建配置', exact: true }).click()
  await page.getByLabel('配置名称').fill('创建-测试.json')
  await page.getByLabel('配置格式').selectOption('json')
  await edit(page, '{invalid')
  await page.getByRole('button', { name: '保存并发布' }).click()
  await expect(page.getByRole('alert')).toContainText('JSON 语法错误')
  await expect(page.getByRole('dialog')).not.toBeVisible()
  await edit(page, '{"enabled":true}')
  await page.getByRole('button', { name: '格式化', exact: true }).click()
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toContainText(
    '  "enabled"',
  )
  await page.getByRole('textbox', { name: '配置内容', exact: true }).press('Control+z')
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(
    '{"enabled":true}',
  )
  await page.getByRole('button', { name: '保存并发布' }).click()
  await confirm(page)
  await expect(page.getByRole('heading', { name: '创建-测试.json' })).toBeVisible()
  expect((await state(page.request, '创建-测试.json')).global_version).toBe(1)
  await page.reload()
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(
    '{"enabled":true}',
  )
})

test('历史查看、任意版本比较、切换比较保持保存基准、回退生成新版本', async ({ page }) => {
  await login(page)
  const name = 'history.json'
  let s = await save(page.request, name, '{"v":1}')
  s = await save(page.request, name, '{"v":2}', s)
  await page.goto(url(name))
  await edit(page, '{"v":3}')
  await page.getByRole('button', { name: '保存并发布' }).click()
  await page.getByLabel('对比版本', { exact: true }).selectOption('1')
  const saveRequest = page.waitForRequest(
    (request) => request.method() === 'PUT' && request.url().endsWith(path(name)),
  )
  await confirm(page)
  expect((await saveRequest).postDataJSON()).toMatchObject({
    expected_id: s.id,
    expected_revision: s.revision,
  })
  await page.getByRole('tab', { name: '版本历史' }).click()
  await page.getByRole('button', { name: '查看 v1', exact: true }).click()
  await expect(page.getByRole('textbox', { name: '历史 v1 内容' })).toHaveText('{"v":1}')
  await page.getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page.getByRole('button', { name: '查看 v1', exact: true })).toBeFocused()
  await page.getByRole('button', { name: '比较 v2', exact: true }).click()
  await page.getByLabel('左侧版本').selectOption('1')
  await page.getByLabel('右侧版本').selectOption('3')
  await expect(page.getByRole('textbox', { name: '对比版本内容' })).toHaveText('{"v":1}')
  await expect(page.getByRole('textbox', { name: '待发布内容' })).toHaveText('{"v":3}')
  await page.getByRole('button', { name: '关闭', exact: true }).click()
  await page.getByRole('button', { name: '回退到 v1', exact: true }).click()
  await confirm(page, '确认回退并发布')
  s = await state(page.request, name)
  expect(s.global_version).toBe(4)
  expect(s.versions[4].content).toBe('{"v":1}')
  expect(s.versions[4].source_version).toBe(1)
  expect(s.versions[4].description).toBeTruthy()
})

test('共享 beta 原位编辑、前后 diff、主历史、转全量及关闭重开删除', async ({ page }, testInfo) => {
  await login(page)
  const name = 'gray.json'
  let s = await save(page.request, name, '{"v":1}')
  s = await save(page.request, name, '{"v":2}', s)
  await page.goto(`${url(name)}?tab=rules`)
  await page.getByRole('button', { name: '新增规则', exact: true }).click()
  await page.getByLabel('规则名称', { exact: true }).fill('预发布')
  await expect(page.getByLabel('固定版本', { exact: true })).toHaveCount(0)
  await page.getByLabel('条件 1 标签名称', { exact: true }).fill('env')
  await page.getByLabel('条件 1 标签值 1', { exact: true }).fill('gray')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  await confirmRule(page)
  await page.getByLabel('试算标签 1 名称').fill('env')
  await page.getByLabel('试算标签 1 值').fill('gray')
  await page.getByRole('button', { name: '开始试算' }).click()
  await expect(page.locator('.simulation-version')).toHaveText('beta')
  await page.getByRole('tab', { name: '配置内容' }).click()
  await edit(page, '{"v":3}')
  await page.getByRole('button', { name: '保存并发布' }).click()
  await confirm(page)
  s = await state(page.request, name)
  expect(s.global_version).toBe(3)
  expect(s.beta.content).toBe('{"v":2}')
  await page.getByLabel('编辑目标', { exact: true }).selectOption('beta')
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText('{"v":2}')
  for (const [before, after] of [
    [2, 4],
    [4, 5],
  ]) {
    if (after === 5) {
      await page.getByRole('button', { name: '切换深色主题' }).click()
      await page.setViewportSize({ width: 375, height: 812 })
    }
    await edit(page, `{"v":${after}}`)
    await page.getByRole('button', { name: '保存并发布' }).click()
    await expect(page.getByRole('dialog')).toContainText('更新 beta 配置（无编号）')
    await expect(page.getByLabel('对比版本', { exact: true })).toHaveCount(0)
    if (after === 5) {
      await expect
        .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
        .toBe(true)
      expect(
        (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze())
          .violations,
      ).toEqual([])
      await expect(page.locator('.cm-mergeViewEditors')).toHaveCSS('flex-direction', 'column')
      await expect
        .poll(() =>
          page
            .locator('.cm-merge-a')
            .evaluate((element) => getComputedStyle(element, '::before').content),
        )
        .toContain('修改前 · 当前灰度内容')
      await expect
        .poll(() =>
          page
            .locator('.cm-merge-b')
            .evaluate((element) => getComputedStyle(element, '::before').content),
        )
        .toContain('修改后 · 将覆盖临时内容')
    }
    await page.screenshot({
      path: testInfo.outputPath(`beta-confirm-${after}.png`),
      fullPage: true,
    })
    await expect(page.getByRole('textbox', { name: '对比版本内容' })).toHaveText(`{"v":${before}}`)
    await confirm(page)
    s = await state(page.request, name)
    expect(s.global_version).toBe(3)
    expect(s.last_version).toBe(3)
    expect(s.beta).toMatchObject({ content: `{"v":${after}}` })
  }
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.getByRole('tab', { name: '版本历史' }).click()
  await expect(page.getByLabel('回退目标', { exact: true })).toHaveCount(0)
  await expect(page.locator('tbody tr')).toHaveCount(4)
  await expect(page.locator('tbody tr').first()).toContainText('beta')
  await page.getByRole('tab', { name: '灰度规则' }).click()
  await page.getByLabel('试算标签 1 名称').fill('env')
  await page.getByLabel('试算标签 1 值').fill('gray')
  await page.screenshot({ path: testInfo.outputPath('rules-beta.png'), fullPage: true })
  await page.getByRole('button', { name: '转为全量', exact: true }).click()
  await expect(page.getByRole('textbox', { name: '待发布内容' })).toHaveText('{"v":5}')
  await confirm(page, '确认转为全量')
  s = await state(page.request, name)
  expect(s.global_version).toBe(4)
  expect(s.versions[4].content).toBe('{"v":5}')
  expect(s.rules[0].enabled).toBe(true)
  await page.getByRole('button', { name: '停用规则 预发布', exact: true }).click()
  await confirmRule(page)
  await page.getByRole('button', { name: '开始试算' }).click()
  await expect(page.locator('.simulation-version')).toHaveText('v4')
  await page.getByRole('button', { name: '启用规则 预发布', exact: true }).click()
  await confirmRule(page)
  await page.getByRole('button', { name: '开始试算' }).click()
  await expect(page.locator('.simulation-version')).toHaveText('beta')
  expect((await state(page.request, name)).beta.content).toBe('{"v":5}')
  await page.getByRole('button', { name: '删除规则 预发布', exact: true }).click()
  await confirmRule(page)
  expect((await state(page.request, name)).rules).toEqual([])
})

test('并发写入产生冲突，保留草稿并重取基准后重新确认', async ({ page }) => {
  await login(page)
  const name = 'conflict.json'
  const old = await save(page.request, name, '{"v":1}')
  await page.goto(url(name))
  await edit(page, '{"v":"draft"}')
  await save(page.request, name, '{"v":"other"}', old)
  await page.getByRole('button', { name: '保存并发布' }).click()
  await page.getByRole('checkbox').check()
  await page.getByRole('button', { name: '确认发布', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('被其他人修改')
  await page.getByRole('button', { name: '读取最新版本并重新对比' }).click()
  await expect(page.getByRole('textbox', { name: '对比版本内容' })).toHaveText('{"v":"other"}')
  await expect(page.getByRole('textbox', { name: '待发布内容' })).toHaveText('{"v":"draft"}')
  await confirm(page)
  expect((await state(page.request, name)).versions[3].content).toBe('{"v":"draft"}')
})

test('离开保护与会话过期后重新登录保留草稿', async ({ page }) => {
  await login(page)
  const name = 'draft.json'
  await save(page.request, name, '{"v":1}')
  await page.goto(url(name))
  await edit(page, '{"v":"unsaved"}')
  await page.getByRole('link', { name: '账号设置', exact: true }).click()
  await expect(page.getByRole('dialog')).toContainText('离开并放弃更改')
  await page.getByRole('button', { name: '继续编辑', exact: true }).click()
  await page.context().clearCookies()
  await page.getByRole('tab', { name: '版本历史' }).click()
  await expect(page.getByRole('dialog')).toContainText('登录已过期')
  await expect(page.getByRole('button', { name: '关闭对话框' })).not.toBeVisible()
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('dialog')).not.toBeVisible()
  await page.getByRole('tab', { name: '配置内容' }).click()
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(
    '{"v":"unsaved"}',
  )
  await page.getByRole('link', { name: '账号设置', exact: true }).click()
  await page.getByRole('button', { name: '放弃并离开', exact: true }).click()
  await expect(page.getByRole('heading', { name: '账号设置', exact: true })).toBeVisible()
})

test('组织管理拒绝非空删除，配置删除需输入名称', async ({ page }) => {
  await login(page)
  await page.getByRole('link', { name: '命名空间', exact: true }).click()
  await page.getByRole('button', { name: '新建命名空间', exact: true }).click()
  await page.getByLabel('命名空间名称', { exact: true }).fill('ui-space')
  await page.getByRole('button', { name: '创建', exact: true }).click()
  await page.getByRole('button', { name: '新建分组', exact: true }).click()
  await page.getByLabel('分组名称', { exact: true }).fill('ui-group')
  await page.getByRole('button', { name: '创建', exact: true }).click()
  await page.getByRole('button', { name: '删除命名空间 ui-space' }).click()
  await page.getByRole('dialog').getByRole('textbox').fill('ui-space')
  await page.getByRole('button', { name: '确认删除', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('仍有分组或配置')
  await page.getByRole('button', { name: '取消', exact: true }).click()
  await page.getByRole('button', { name: '删除分组 ui-group' }).click()
  await page.getByRole('dialog').getByRole('textbox').fill('ui-group')
  await page.getByRole('button', { name: '确认删除', exact: true }).click()
  await page.getByRole('button', { name: '删除命名空间 ui-space' }).click()
  await page.getByRole('dialog').getByRole('textbox').fill('ui-space')
  await page.getByRole('button', { name: '确认删除', exact: true }).click()
  const name = 'delete.json'
  await save(page.request, name, '{"v":1}')
  await page.goto(url(name))
  await page.getByRole('button', { name: '删除配置', exact: true }).click()
  await expect(page.getByRole('button', { name: '永久删除', exact: true })).toBeDisabled()
  await page.getByLabel('输入配置名称以确认').fill(name)
  await page.getByRole('button', { name: '永久删除', exact: true }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  expect((await page.request.get(path(name))).status()).toBe(404)
})

test('密码错误不误判会话过期，修改密码并退出后重新登录', async ({ page }) => {
  await login(page)
  await page.getByRole('link', { name: '账号设置', exact: true }).click()
  await page.getByLabel('当前密码', { exact: true }).fill('wrong-password')
  await page.getByLabel('新密码', { exact: true }).fill('ui-changed-password')
  await page.getByLabel('再次输入新密码', { exact: true }).fill('ui-changed-password')
  await page.getByRole('button', { name: '更新密码' }).click()
  await expect(page.getByRole('alert')).toContainText('当前密码不正确')
  await expect(page.getByRole('dialog')).not.toBeVisible()
  await page.getByLabel('当前密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '更新密码' }).click()
  await expect(page.getByLabel('当前密码', { exact: true })).toHaveValue('')
  await page.getByRole('button', { name: '退出登录' }).click()
  await page.getByLabel('密码', { exact: true }).fill('ui-changed-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  expect(
    (
      await page.request.post('/api/admin/password', {
        headers: { Origin: origin },
        data: { old_password: 'ui-changed-password', new_password: password },
      })
    ).ok(),
  ).toBeTruthy()
})

test('浅深主题、响应式、键盘导航与无障碍检查', async ({ page }, testInfo) => {
  await login(page)
  const name = 'visual.json'
  await save(page.request, name, '{"service":"confhub","enabled":true,"retries":3}')
  await page.goto(url(name))
  await page.getByRole('tab', { name: '配置内容' }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByRole('tab', { name: '版本历史' })).toBeFocused()
  await page.keyboard.press('Home')
  for (const dark of [false, true]) {
    if (dark) await page.getByRole('button', { name: '切换深色主题' }).click()
    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
      .analyze()
    expect(results.violations).toEqual([])
    await page.screenshot({
      path: testInfo.outputPath(`editor-${dark ? 'dark' : 'light'}.png`),
      fullPage: true,
    })
    await page.getByRole('link', { name: '配置管理', exact: true }).click()
    expect(
      (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze())
        .violations,
    ).toEqual([])
    await page.goto(url(name))
  }
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toBeVisible()
  await page.emulateMedia({ reducedMotion: 'reduce' })
  for (const width of [375, 768, 1024, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
      .toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`editor-${width}.png`), fullPage: true })
  }
  await page.setViewportSize({ width: 375, height: 812 })
  await page.getByRole('button', { name: '打开导航' }).click()
  await expect(page.getByRole('dialog', { name: '导航菜单' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('button', { name: '打开导航' })).toBeFocused()
  await edit(page, '{"service":"changed"}')
  await page.getByRole('button', { name: '保存并发布' }).click()
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
    .toBe(true)
  expect(
    (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze())
      .violations,
  ).toEqual([])
  await expect
    .poll(() =>
      page.getByRole('dialog').evaluate((element) => element.contains(document.activeElement)),
    )
    .toBe(true)
  await expect(page.locator('.cm-mergeViewEditors')).toHaveCSS('flex-direction', 'column')
  await page.screenshot({ path: testInfo.outputPath('diff-mobile-dark.png'), fullPage: true })
  await page.setViewportSize({ width: 812, height: 375 })
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
    .toBe(true)
})

test('多条件规则编辑重排、共享 beta 及主版本回退保留灰度和草稿', async ({ page }) => {
  await login(page)
  const name = 'rule-order.json'
  let s = await save(page.request, name, '{"v":1}')
  s = await save(page.request, name, '{"v":2}', s)
  const rules = [
    {
      id: 'first',
      name: '规则一',
      enabled: true,
      conditions: [
        { tag: 'env', operator: 'in', values: ['gray', 'staging'] },
        { tag: 'region', operator: 'eq', values: ['east'] },
      ],
    },
    {
      id: 'second',
      name: '规则二',
      enabled: true,
      conditions: [{ tag: 'env', operator: 'eq', values: ['gray'] }],
    },
  ]
  expect(
    (
      await page.request.put(`${path(name)}/rules`, {
        headers: { Origin: origin },
        data: {
          expected_id: s.id,
          expected_revision: s.revision,
          rules,
          source_version: s.global_version,
          confirmed: true,
        },
      })
    ).ok(),
  ).toBeTruthy()
  s = await state(page.request, name)
  s = await save(page.request, name, '{"rule":"first"}', s, 'first')
  s = await save(page.request, name, '{"rule":"second"}', s, 'second')
  await page.goto(`${url(name)}?tab=rules`)
  await page.getByLabel('试算标签 1 名称').fill('env')
  await page.getByLabel('试算标签 1 值').fill('gray')
  await page.getByRole('button', { name: '开始试算' }).click()
  await expect(page.locator('.simulation-version')).toHaveText('beta')
  await expect(page.locator('.simulation-result p')).toHaveText('规则二')
  await page.getByRole('button', { name: '添加标签', exact: true }).click()
  await page.getByLabel('试算标签 2 名称').fill('region')
  await page.getByLabel('试算标签 2 值').fill('east')
  await page.getByRole('button', { name: '开始试算' }).click()
  await expect(page.locator('.simulation-version')).toHaveText('beta')
  await expect(page.locator('.simulation-result p')).toHaveText('规则一')
  await page.getByRole('button', { name: '上移规则 规则二', exact: true }).click()
  await confirmRule(page)
  await page.getByRole('button', { name: '开始试算' }).click()
  await expect(page.locator('.simulation-version')).toHaveText('beta')
  await expect(page.locator('.simulation-result p')).toHaveText('规则二')
  await page.getByRole('button', { name: '修改规则 规则一', exact: true }).click()
  await page.getByLabel('条件 1 标签值 2', { exact: true }).fill('preview')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  await confirmRule(page)
  expect((await state(page.request, name)).rules[1].conditions[0].values).toEqual([
    'gray',
    'preview',
  ])
  await page.getByRole('tab', { name: '配置内容' }).click()
  await edit(page, '{"v":"kept draft"}')
  await page.getByRole('tab', { name: '版本历史' }).click()
  await expect(page.getByLabel('回退目标', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '回退到 v1', exact: true }).click()
  await confirm(page, '确认回退并发布')
  s = await state(page.request, name)
  expect(s.global_version).toBe(3)
  expect(s.beta).toMatchObject({ content: '{"rule":"second"}' })
  expect(s.rules.every((rule: Record<string, unknown>) => !('beta' in rule))).toBeTruthy()
  await page.getByRole('tab', { name: '配置内容' }).click()
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(
    '{"v":"kept draft"}',
  )
})

test('删除重建的配置身份变化禁止重新确认覆盖，保留编辑内容', async ({ page }) => {
  await login(page)
  const name = 'recreated.json'
  const s = await save(page.request, name, '{"v":1}')
  await page.goto(url(name))
  await edit(page, '{"v":"old draft"}')
  expect(
    (
      await page.request.delete(path(name), {
        headers: { Origin: origin },
        data: { expected_id: s.id, expected_revision: s.revision, confirmed: true },
      })
    ).ok(),
  ).toBeTruthy()
  await save(page.request, name, '{"v":"new identity"}')
  await page.getByRole('button', { name: '保存并发布' }).click()
  await page.getByRole('checkbox').check()
  await page.getByRole('button', { name: '确认发布', exact: true }).click()
  await page.getByRole('button', { name: '读取最新版本并重新对比' }).click()
  await expect(page.getByRole('alert')).toContainText('删除后重建')
  await page.getByRole('checkbox').check()
  await expect(page.getByRole('button', { name: '确认发布', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '返回编辑', exact: true }).click()
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(
    '{"v":"old draft"}',
  )
  expect((await state(page.request, name)).versions[1].content).toBe('{"v":"new identity"}')
})

test('YAML 多文档格式化可撤销，所有支持格式均能切换高亮', async ({ page }) => {
  await login(page)
  await page.getByRole('link', { name: '新建配置', exact: true }).click()
  const text = '# docs\na: [1,2]\n---\nb: 2'
  await edit(page, text)
  await page.getByRole('button', { name: '格式化', exact: true }).click()
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toContainText('---')
  await page.getByRole('textbox', { name: '配置内容', exact: true }).press('Control+z')
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(text, {
    useInnerText: true,
  })
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  for (const [format, content] of [
    ['toml', 'a = "test"'],
    ['xml', '<a value="test"/>'],
    ['properties', 'a=test'],
    ['ini', '[main]\na=test'],
    ['json', '{"a":"test"}'],
    ['yaml', 'a: test'],
  ]) {
    await page.getByLabel('配置格式').selectOption(format)
    await edit(page, content)
    await expect(page.locator('.cm-line span').first()).toBeVisible()
  }
  await page.getByLabel('配置格式').selectOption('text')
  await edit(page, 'plain content')
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(
    'plain content',
  )
  expect(errors).toEqual([])
})

test('分组搜索涵盖分页之外的配置，失败退出可重试', async ({ page }) => {
  await login(page)
  for (let i = 0; i < 28; i++)
    await save(page.request, `pagination-${String(i).padStart(2, '0')}.json`, '{"v":1}')
  await page.getByRole('button', { name: '刷新配置列表' }).click()
  await expect(page.locator('tbody tr')).toHaveCount(25)
  await page.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(page.locator('tbody')).toContainText('pagination-27.json')
  await page.getByRole('textbox', { name: '搜索本分组配置' }).fill('pagination-00')
  await expect(page.locator('tbody tr')).toHaveCount(1)
  await expect(page.locator('tbody')).toContainText('pagination-00.json')
  await page.route('**/api/admin/logout', (route) => route.abort())
  await page.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByRole('alert')).toContainText('无法连接服务')
  await page.unroute('**/api/admin/logout')
  await page.getByRole('button', { name: '重试', exact: true }).click()
  await expect(page.getByLabel('密码', { exact: true })).toBeVisible()
})

test('同名 beta 并发冲突重新比较当前正文，保留草稿且不创建主版本', async ({ page }) => {
  await login(page)
  const name = 'beta-conflict.json'
  let s = await save(page.request, name, '{"v":1}')
  const response = await page.request.put(`${path(name)}/rules`, {
    headers: { Origin: origin },
    data: {
      expected_id: s.id,
      expected_revision: s.revision,
      confirmed: true,
      source_version: s.global_version,
      rules: [
        {
          id: 'beta',
          name: '灰度',
          enabled: true,
          conditions: [{ tag: 'env', operator: 'eq', values: ['gray'] }],
        },
      ],
    },
  })
  expect(response.ok()).toBeTruthy()
  s = (await response.json()).state
  await page.goto(url(name))
  await page.getByLabel('编辑目标', { exact: true }).selectOption('beta')
  await edit(page, '{"v":"draft"}')
  await save(page.request, name, '{"v":"concurrent"}', s, 'beta')
  await page.getByRole('button', { name: '保存并发布' }).click()
  await expect(page.getByLabel('对比版本', { exact: true })).toHaveCount(0)
  await page.getByRole('checkbox').check()
  await page.getByRole('button', { name: '确认发布', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('被其他人修改')
  await page.getByRole('button', { name: '读取最新版本并重新对比' }).click()
  await expect(page.getByRole('textbox', { name: '对比版本内容' })).toHaveText('{"v":"concurrent"}')
  await expect(page.getByRole('textbox', { name: '待发布内容' })).toHaveText('{"v":"draft"}')
  await expect(page.getByRole('checkbox')).not.toBeChecked()
  await confirm(page)
  s = await state(page.request, name)
  expect(s.last_version).toBe(1)
  expect(s.beta).toMatchObject({ content: '{"v":"draft"}' })
  await expect(page.getByRole('button', { name: '保存并发布' })).toBeDisabled()
})

test('灰度转全量冲突后重新核对最新 beta 内容', async ({ page }) => {
  await login(page)
  const name = 'beta-promote-conflict.json'
  let s = await save(page.request, name, '{"v":1}')
  const response = await page.request.put(`${path(name)}/rules`, {
    headers: { Origin: origin },
    data: {
      expected_id: s.id,
      expected_revision: s.revision,
      confirmed: true,
      source_version: s.global_version,
      rules: [
        {
          id: 'beta',
          name: '灰度',
          enabled: true,
          conditions: [{ tag: 'env', operator: 'eq', values: ['gray'] }],
        },
      ],
    },
  })
  expect(response.ok()).toBeTruthy()
  s = (await response.json()).state
  s = await save(page.request, name, '{"v":"old beta"}', s, 'beta')
  await page.goto(`${url(name)}?tab=rules`)
  await page.getByRole('button', { name: '转为全量', exact: true }).click()
  await save(page.request, name, '{"v":"latest beta"}', s, 'beta')
  await page.getByRole('checkbox').check()
  await page.getByRole('button', { name: '确认转为全量', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('被其他人修改')
  await page.getByRole('button', { name: '读取最新版本并重新对比' }).click()
  await expect(page.getByRole('textbox', { name: '待发布内容' })).toHaveText('{"v":"latest beta"}')
  await confirm(page, '确认转为全量')
  s = await state(page.request, name)
  expect(s.global_version).toBe(2)
  expect(s.versions[2].content).toBe('{"v":"latest beta"}')
  expect(s.rules[0].enabled).toBe(true)
})

test('新建灰度规则遇全量并发更新仍保留显式复制来源', async ({ page }) => {
  await login(page)
  const name = 'rule-create-conflict.json'
  let s = await save(page.request, name, '{"v":1}')
  await page.goto(`${url(name)}?tab=rules`)
  await page.getByRole('button', { name: '新增规则', exact: true }).click()
  await page.getByLabel('规则名称', { exact: true }).fill('新灰度')
  await page.getByLabel('条件 1 标签名称', { exact: true }).fill('env')
  await page.getByLabel('条件 1 标签值 1', { exact: true }).fill('gray')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  s = await save(page.request, name, '{"v":2}', s)
  await page.getByRole('checkbox').check()
  await page.getByRole('button', { name: '确认生效', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('被其他人修改')
  await page.getByRole('button', { name: '读取最新规则并保留当前编辑' }).click()
  await expect(page.getByRole('dialog')).toContainText('从主版本 v1 复制')
  await confirmRule(page)
  s = await state(page.request, name)
  expect(s.beta).toMatchObject({ content: '{"v":1}' })
})

test('在线客户端只读展示、标签补全与 IP 区间规则', async ({ page }) => {
  await login(page)
  const name = 'ip-clients.json'
  await save(page.request, name, '{"global":true}')
  await page.evaluate(
    ({ name }) => {
      const client = new WebSocket(
        `${location.origin.replace('http', 'ws')}/api/client/watch?tags=${encodeURIComponent(JSON.stringify({ env: 'canary', 'sys.ip': '192.168.2.3', 'sys.hostname': 'node-online' }))}`,
      )
      ;(window as unknown as { testClient: WebSocket }).testClient = client
      client.onopen = () =>
        client.send(
          JSON.stringify({
            op: 'subscribe',
            key: { namespace: 'public', group: 'DEFAULT_GROUP', name },
          }),
        )
    },
    { name },
  )
  await page.getByRole('link', { name: '在线客户端', exact: true }).click()
  await expect(page.getByRole('heading', { name: '在线客户端', exact: true })).toBeVisible()
  const card = page.getByTestId('client-card').filter({ hasText: 'node-online' })
  await expect(card).toBeVisible({ timeout: 15000 })
  await expect(card).toContainText('canary')
  await expect(card).toContainText('v1')
  await page.screenshot({ path: '../docs/images/frontend/clients-light.png', fullPage: true })
  expect(
    (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations,
  ).toEqual([])
  // Preserve this connection while navigating: use the SPA link on its subscription.
  await card.getByRole('link', { name }).click()
  await page.getByRole('tab', { name: '灰度规则' }).click()
  await page.getByRole('button', { name: '新增规则', exact: true }).click()
  const tag = page.getByRole('combobox', { name: '条件 1 标签名称' })
  await tag.fill('en')
  await page.getByRole('option', { name: 'env', exact: true }).click()
  const value = page.getByRole('combobox', { name: '条件 1 标签值 1' })
  await value.fill('ca')
  await page.getByRole('option', { name: 'canary', exact: true }).click()
  await tag.fill('sys.i')
  await tag.press('ArrowDown')
  await tag.press('Enter')
  await expect(tag).toHaveValue('sys.ip')
  await page.getByLabel('条件 1 匹配方式').selectOption('ip_range')
  await page.getByLabel('条件 1 起始 IP').fill('192.168.2.5')
  await page.getByLabel('条件 1 结束 IP').fill('192.168.2.1')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  await expect(page.getByRole('alert')).toContainText('IP 区间')
  await page.getByLabel('条件 1 起始 IP').fill('192.168.2.1')
  await page.getByLabel('条件 1 结束 IP').fill('192.168.2.5')
  await page.getByLabel('规则名称', { exact: true }).fill('IP 范围')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  await confirmRule(page)
  const s = await state(page.request, name)
  const condition = s.rules[0].conditions[0]
  expect(condition).toEqual({
    tag: 'sys.ip',
    operator: 'ip_range',
    values: ['192.168.2.1', '192.168.2.5'],
  })
  for (const [ip, match] of [
    ['192.168.2.1', true],
    ['192.168.2.5', true],
    ['192.168.2.10', false],
  ] as const) {
    const r = await page.request.get(
      `/api/client/config?name=${name}&tags=${encodeURIComponent(JSON.stringify({ 'sys.ip': ip }))}`,
    )
    expect((await r.json()).rule_id === s.rules[0].id).toBe(match)
  }
  await page.setViewportSize({ width: 375, height: 812 })
  await page.getByRole('button', { name: '打开导航' }).click()
  await page.getByRole('link', { name: '在线客户端', exact: true }).click()
  await expect(card).toBeVisible()
  await expect(card).toContainText('beta', { timeout: 15000 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
  await page.getByRole('button', { name: '切换深色主题' }).click()
  await expect(page.getByRole('button', { name: '刷新', exact: true })).toHaveCSS(
    'background-color',
    'rgb(28, 43, 53)',
  )
  expect(
    (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations,
  ).toEqual([])
  await page.screenshot({ path: '../docs/images/frontend/clients-mobile-dark.png', fullPage: true })
  await page.evaluate(() => (window as unknown as { testClient: WebSocket }).testClient.close())
  await expect(card).not.toBeVisible({ timeout: 15000 })
})

test('长标签建议列表的键盘选中项保持可见', async ({ page }) => {
  await login(page)
  const name = 'keyboard-tags.json'
  await save(page.request, name, '{"ok":true}')
  await page.goto(url(name))
  await page.evaluate(() => {
    const tags: Record<string, string> = {}
    for (let i = 1; i <= 30; i++) tags[`tag-${String(i).padStart(2, '0')}`] = 'value'
    const client = new WebSocket(
      `${location.origin.replace('http', 'ws')}/api/client/watch?tags=${encodeURIComponent(JSON.stringify(tags))}`,
    )
    ;(window as unknown as { testClient: WebSocket }).testClient = client
  })
  await page.getByRole('tab', { name: '灰度规则' }).click()
  await page.getByRole('button', { name: '新增规则', exact: true }).click()
  const input = page.getByRole('combobox', { name: '条件 1 标签名称' })
  await input.fill('tag-')
  // First request may precede the five-second presence sync; reopen to query again.
  await expect
    .poll(
      async () => {
        await input.blur()
        await input.focus()
        return page.getByRole('option', { name: 'tag-30', exact: true }).count()
      },
      { timeout: 15000, intervals: [1000] },
    )
    .toBe(1)
  for (let i = 0; i < 30; i++) await input.press('ArrowDown')
  const selected = page.getByRole('option', { name: 'tag-30', exact: true })
  await expect(selected).toHaveAttribute('aria-selected', 'true')
  const popup = page.locator('.tag-suggestion-popup')
  await expect
    .poll(async () => {
      const a = await selected.boundingBox(),
        b = await popup.boundingBox()
      return !!a && !!b && a.y >= b.y && a.y + a.height <= b.y + b.height
    })
    .toBeTruthy()
  await input.press('Enter')
  await expect(input).toHaveValue('tag-30')
  await expect(popup).not.toBeVisible()
  await page.evaluate(() => (window as unknown as { testClient: WebSocket }).testClient.close())
})

test('唯一 beta 历史置顶编辑、选择旧来源、多规则共享与删除后重新复制', async ({ page }) => {
  await login(page)
  const name = 'shared-history-beta.json'
  let s = await save(page.request, name, '{"source":1}')
  s = await save(page.request, name, '{"global":2}', s)
  await page.goto(`${url(name)}?tab=rules`)
  await page.getByRole('button', { name: '新增规则', exact: true }).click()
  await page.getByLabel('beta 复制来源', { exact: true }).selectOption('1')
  await page.getByLabel('规则名称', { exact: true }).fill('范围 A')
  await page.getByLabel('条件 1 标签名称', { exact: true }).fill('env')
  await page.getByLabel('条件 1 标签值 1', { exact: true }).fill('a')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  await expect(page.getByRole('dialog')).toContainText('从主版本 v1 复制')
  await confirmRule(page)
  await page.getByRole('button', { name: '新增规则', exact: true }).click()
  await expect(page.getByLabel('beta 复制来源', { exact: true })).toHaveCount(0)
  await page.getByLabel('规则名称', { exact: true }).fill('范围 B')
  await page.getByLabel('条件 1 标签名称', { exact: true }).fill('env')
  await page.getByLabel('条件 1 标签值 1', { exact: true }).fill('b')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  await confirmRule(page)
  await page.getByRole('tab', { name: '配置内容' }).click()
  await expect(page.getByLabel('编辑目标', { exact: true }).locator('option')).toHaveCount(2)
  await expect(
    page.getByLabel('编辑目标', { exact: true }).locator('option[value=beta]'),
  ).toHaveText('beta 配置')
  await page.getByRole('tab', { name: '版本历史' }).click()
  const first = page.locator('tbody tr').first()
  await expect(first).toContainText('beta')
  await expect(first.getByRole('button', { name: /回退/ })).toHaveCount(0)
  await first.getByRole('button', { name: '编辑 beta', exact: true }).click()
  await expect(page.getByLabel('编辑目标', { exact: true })).toHaveValue('beta')
  await expect(page.getByRole('textbox', { name: '配置内容', exact: true })).toHaveText(
    '{"source":1}',
  )
  await edit(page, '{"shared":true}')
  await page.getByRole('button', { name: '保存并发布' }).click()
  await expect(page.getByRole('dialog')).toContainText('所有命中灰度规则的客户端')
  await expect(page.getByLabel('对比版本', { exact: true })).toHaveCount(0)
  await confirm(page)
  s = await state(page.request, name)
  expect(s.last_version).toBe(2)
  expect(s.beta.content).toBe('{"shared":true}')
  expect(s.rules).toHaveLength(2)
  for (const env of ['a', 'b']) {
    const response = await page.request.get(
      `/api/client/config?name=${name}&tags=${encodeURIComponent(JSON.stringify({ env }))}`,
    )
    expect(await response.json()).toMatchObject({
      beta: true,
      version: 0,
      content: '{"shared":true}',
    })
  }
  await page.getByRole('tab', { name: '版本历史' }).click()
  await expect(page.locator('tbody tr')).toHaveCount(3)
  expect(
    (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations,
  ).toEqual([])
  await page.screenshot({
    path: '../docs/images/frontend/shared-beta-history-light.png',
    fullPage: true,
  })
  await page.getByRole('tab', { name: '灰度规则' }).click()
  await page.getByRole('button', { name: '删除规则 范围 A', exact: true }).click()
  await confirmRule(page)
  expect((await state(page.request, name)).beta.content).toBe('{"shared":true}')
  await page.getByRole('button', { name: '删除规则 范围 B', exact: true }).click()
  await confirmRule(page)
  expect((await state(page.request, name)).beta).toBeUndefined()
  await page.getByRole('button', { name: '新增规则', exact: true }).click()
  await expect(page.getByLabel('beta 复制来源', { exact: true })).toBeVisible()
  await page.getByLabel('beta 复制来源', { exact: true }).selectOption('2')
  await page.getByLabel('条件 1 标签名称', { exact: true }).fill('env')
  await page.getByLabel('条件 1 标签值 1', { exact: true }).fill('a')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  await confirmRule(page)
  expect((await state(page.request, name)).beta.content).toBe('{"global":2}')
})

test('规则冲突时 beta 被删除需重选来源并保留草稿，元数据操作终止', async ({ page }) => {
  await login(page)
  const name = 'beta-removed-rule-conflict.json'
  let s = await save(page.request, name, '{"source":1}')
  const firstRule = {
    id: 'first',
    name: '原规则',
    enabled: true,
    conditions: [{ tag: 'env', operator: 'eq', values: ['first'] }],
  }
  let response = await page.request.put(`${path(name)}/rules`, {
    headers: { Origin: origin },
    data: {
      expected_id: s.id,
      expected_revision: s.revision,
      confirmed: true,
      source_version: 1,
      rules: [firstRule],
    },
  })
  expect(response.ok()).toBeTruthy()
  await page.goto(`${url(name)}?tab=rules`)
  await page.getByRole('button', { name: '新增规则', exact: true }).click()
  await page.getByLabel('规则名称', { exact: true }).fill('保留的草稿')
  await page.getByLabel('条件 1 标签名称', { exact: true }).fill('env')
  await page.getByLabel('条件 1 标签值 1', { exact: true }).fill('draft')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  const removeAll = async () => {
    s = await state(page.request, name)
    response = await page.request.put(`${path(name)}/rules`, {
      headers: { Origin: origin },
      data: { expected_id: s.id, expected_revision: s.revision, confirmed: true, rules: [] },
    })
    expect(response.ok()).toBeTruthy()
  }
  const triggerConflict = async () => {
    await page.getByRole('checkbox').check()
    await page.getByRole('button', { name: '确认生效', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText('被其他人修改')
    await page.getByRole('button', { name: '读取最新规则并保留当前编辑' }).click()
  }
  await removeAll()
  await triggerConflict()
  await expect(page.getByLabel('规则名称', { exact: true })).toHaveValue('保留的草稿')
  await expect(page.getByLabel('beta 复制来源', { exact: true })).toHaveValue('0')
  await page.getByLabel('beta 复制来源', { exact: true }).selectOption('1')
  await page.getByRole('button', { name: '查看影响并确认' }).click()
  await confirmRule(page)
  s = await state(page.request, name)
  expect(s.beta.content).toBe('{"source":1}')
  expect(s.rules).toHaveLength(1)
  expect(s.rules[0].name).toBe('保留的草稿')
  await page.getByRole('button', { name: '停用规则 保留的草稿', exact: true }).click()
  await removeAll()
  await triggerConflict()
  await expect(page.getByRole('dialog')).not.toBeVisible()
  await expect(page.getByRole('alert')).toContainText('原规则操作无法继续')
  expect((await state(page.request, name)).beta).toBeUndefined()
})
