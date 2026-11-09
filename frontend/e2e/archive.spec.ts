import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const headers = { Origin: 'http://127.0.0.1:18080' }

test('导出选中与筛选全部，上传预览后整包发布，明暗窄屏可用', async ({ page }, testInfo) => {
  await page.goto('/configs')
  await page.getByLabel('密码', { exact: true }).fill('ui-test-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  const group = `archive-${Date.now()}`
  const target = `${group}-target`
  expect(
    (
      await page.request.post('/api/admin/namespaces/public/groups', {
        headers,
        data: { name: group },
      })
    ).ok(),
  ).toBeTruthy()
  expect(
    (await page.request.post('/api/admin/namespaces', { headers, data: { name: target } })).ok(),
  ).toBeTruthy()
  for (const name of ['a.yaml', 'b.yaml']) {
    expect(
      (
        await page.request.put(`/api/admin/namespaces/public/groups/${group}/configs/${name}`, {
          headers,
          data: { content: 'value: 1\r\n', format: 'yaml', confirmed: true },
        })
      ).ok(),
    ).toBeTruthy()
  }
  await page.goto(`/configs?namespace=public&group=${group}`)
  await page.getByRole('checkbox', { name: '选择 a.yaml', exact: true }).check()
  const downloaded = page.waitForEvent('download')
  await page.getByRole('button', { name: '导出选中', exact: true }).click()
  expect((await downloaded).suggestedFilename()).toMatch(/\.zip$/)
  await page.getByRole('button', { name: '取消选择', exact: true }).click()
  await page.getByRole('textbox', { name: '搜索本分组配置' }).fill('b.yaml')
  await expect(page.getByRole('link', { name: '打开 a.yaml', exact: true })).toHaveCount(0)
  const allDownload = page.waitForEvent('download')
  await page.getByRole('button', { name: '导出全部', exact: true }).click()
  const zipPath = await (await allDownload).path()
  await page.getByRole('button', { name: '导入配置', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '导入配置', exact: true })
  await dialog.getByLabel('ZIP 文件').setInputFiles(zipPath!)
  await dialog.getByRole('combobox', { name: '目标命名空间', exact: true }).click()
  await page.getByRole('option', { name: target, exact: true }).click()
  await dialog.getByRole('button', { name: '预览导入', exact: true }).click()
  await expect(dialog).toContainText('b.yaml')
  await expect(dialog).not.toContainText('a.yaml')
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.setViewportSize({ width: 375, height: 812 })
  for (const dark of [false, true]) {
    await page.evaluate(
      (value) => (document.documentElement.dataset.theme = value ? 'dark' : 'light'),
      dark,
    )
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBeTruthy()
    await page.screenshot({
      path: testInfo.outputPath(dark ? 'import-dark.png' : 'import-light.png'),
    })
  }
  await dialog.getByRole('checkbox', { name: '确认导入并立即发布这些配置' }).check()
  await dialog.getByRole('button', { name: '导入并发布', exact: true }).click()
  await expect(dialog).not.toBeVisible()
  const imported = await page.request.get(
    `/api/admin/namespaces/${target}/groups/${group}/configs/b.yaml`,
  )
  expect(imported.ok()).toBeTruthy()
  expect((await imported.json()).versions['1'].content).toBe('value: 1\r\n')
  expect(
    (
      await page.request.get(`/api/admin/namespaces/${target}/groups/${group}/configs/a.yaml`)
    ).status(),
  ).toBe(404)
})

test('同名冲突切换策略，提交前并发修改要求重新预览', async ({ page }) => {
  await page.goto('/configs')
  await page.getByLabel('密码', { exact: true }).fill('ui-test-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  const group = `archive-conflict-${Date.now()}`
  expect(
    (
      await page.request.post('/api/admin/namespaces/public/groups', {
        headers,
        data: { name: group },
      })
    ).ok(),
  ).toBeTruthy()
  const path = `/api/admin/namespaces/public/groups/${group}/configs/app`
  const created = await page.request.put(path, {
    headers,
    data: { content: 'original', format: 'text', confirmed: true },
  })
  expect(created.ok()).toBeTruthy()
  const state = (await created.json()).state
  const zip = await page.request.get(`/api/admin/configs/export?namespace=public&group=${group}`)
  expect(zip.ok()).toBeTruthy()
  await page.goto(`/configs?namespace=public&group=${group}`)
  await page.getByRole('button', { name: '导入配置', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '导入配置', exact: true })
  await dialog
    .getByLabel('ZIP 文件')
    .setInputFiles({ name: 'config.zip', mimeType: 'application/zip', buffer: await zip.body() })
  await dialog.getByRole('button', { name: '预览导入', exact: true }).click()
  await expect(dialog).toContainText('同名冲突')
  await expect(dialog.getByRole('button', { name: '导入并发布', exact: true })).toBeDisabled()
  await dialog.getByRole('combobox', { name: '同名配置', exact: true }).click()
  await page.getByRole('option', { name: '覆盖并发布', exact: true }).click()
  await expect(dialog.getByRole('button', { name: '导入并发布', exact: true })).toHaveCount(0)
  await dialog.getByRole('button', { name: '预览导入', exact: true }).click()
  await expect(dialog).toContainText('不变')
  const changed = await page.request.put(path, {
    headers,
    data: {
      expected_id: state.id,
      expected_revision: state.revision,
      content: 'concurrent',
      format: 'text',
      confirmed: true,
    },
  })
  expect(changed.ok()).toBeTruthy()
  await dialog.getByRole('checkbox', { name: '确认导入并立即发布这些配置' }).check()
  await dialog.getByRole('button', { name: '导入并发布', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('整包未提交')
  await expect(dialog.getByRole('button', { name: '导入并发布', exact: true })).toHaveCount(0)
  await dialog.getByRole('button', { name: '预览导入', exact: true }).click()
  await expect(dialog).toContainText('覆盖 1')
  await expect(
    dialog.getByRole('checkbox', { name: '确认导入并立即发布这些配置' }),
  ).not.toBeChecked()
  await dialog.getByRole('button', { name: '取消', exact: true }).click()
  expect((await (await page.request.get(path)).json()).versions['2'].content).toBe('concurrent')
})
