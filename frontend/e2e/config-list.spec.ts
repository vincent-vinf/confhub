import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const headers = { Origin: 'http://127.0.0.1:18080' }

test('全部分组、全局排序、分页和跨分组导出，兼容键盘与窄屏明暗主题', async ({ page }, testInfo) => {
  await page.goto('/configs')
  await page.getByLabel('密码', { exact: true }).fill('ui-test-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  const namespace = `listing-${Date.now()}`
  expect(
    (await page.request.post('/api/admin/namespaces', { headers, data: { name: namespace } })).ok(),
  ).toBeTruthy()
  for (const group of ['A', 'B']) {
    expect(
      (
        await page.request.post(`/api/admin/namespaces/${namespace}/groups`, {
          headers,
          data: { name: group },
        })
      ).ok(),
    ).toBeTruthy()
    for (let i = 1; i <= 15; i++) {
      expect(
        (
          await page.request.put(
            `/api/admin/namespaces/${namespace}/groups/${group}/configs/item-${i}.yaml`,
            {
              headers,
              data: {
                expected_id: '',
                expected_revision: 0,
                content: `value: ${i}`,
                format: 'yaml',
                confirmed: true,
              },
            },
          )
        ).ok(),
      ).toBeTruthy()
    }
  }
  await page.goto(`/configs?namespace=${namespace}`)
  await expect(page.getByRole('combobox', { name: '分组', exact: true })).toContainText('全部分组')
  await expect(page.getByRole('row')).toHaveCount(26)
  await expect(page.getByRole('columnheader', { name: /分组/ })).toBeVisible()
  await expect(page.locator('tbody time').first()).toHaveAttribute('datetime', /^\d{4}-/)
  await expect(page.locator('tbody time').first()).not.toHaveText('—')
  await page.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(page.getByRole('row')).toHaveCount(6)
  const nameSort = page.getByRole('button', { name: '配置名称，点击降序排列' })
  await nameSort.focus()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('columnheader', { name: /配置名称/ })).toHaveAttribute(
    'aria-sort',
    'descending',
  )
  await expect(page.locator('tbody tr').first()).toContainText('item-15.yaml')
  await expect(page.getByRole('button', { name: '上一页', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '分组，点击升序排列' }).click()
  await expect(page.locator('tbody tr').first()).toContainText('A')
  await page.getByRole('button', { name: '分组，点击降序排列' }).click()
  await expect(page.locator('tbody tr').first()).toContainText('B')
  await page.getByRole('button', { name: '配置名称，点击升序排列' }).click()
  for (const group of ['A', 'B']) {
    await page.getByRole('checkbox', { name: `选择 ${group} / item-1.yaml`, exact: true }).check()
  }
  const selectionRequest = page.waitForRequest((request) =>
    request.url().includes('/configs/export?'),
  )
  const downloaded = page.waitForEvent('download')
  await page.getByRole('button', { name: '导出选中', exact: true }).click()
  const request = await selectionRequest
  const keys = JSON.parse(new URL(request.url()).searchParams.get('keys')!)
  expect(keys.map((key: { group: string }) => key.group).sort()).toEqual(['A', 'B'])
  await (await downloaded).saveAs(testInfo.outputPath('selected.zip'))
  await page.getByRole('button', { name: '取消选择', exact: true }).click()
  await page.getByRole('combobox', { name: '分组', exact: true }).click()
  await page.getByRole('option', { name: 'A', exact: true }).click()
  await expect(page.getByRole('columnheader', { name: /分组/ })).toHaveCount(0)
  await expect(page.getByRole('row')).toHaveCount(16)
  await page.getByRole('combobox', { name: '分组', exact: true }).click()
  await page.getByRole('option', { name: '全部分组', exact: true }).click()
  await expect(page.getByRole('row')).toHaveCount(26)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  for (const [theme, viewport] of [
    ['light', { width: 1440, height: 1000 }],
    ['dark', { width: 375, height: 812 }],
    ['dark', { width: 812, height: 375 }],
  ] as const) {
    await page.setViewportSize(viewport)
    if (theme === 'dark' && (await page.getByRole('button', { name: '切换深色主题' }).count()))
      await page.getByRole('button', { name: '切换深色主题' }).click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
      .toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`list-${theme}-${viewport.width}.png`) })
  }
})
