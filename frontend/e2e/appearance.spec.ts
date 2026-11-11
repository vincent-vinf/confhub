import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { setDisplayMode } from './appearance'

test('four palettes have accessible light/dark previews and persist through reload and login', async ({
  page,
}, testInfo) => {
  test.setTimeout(120000)
  await page.goto('/login')
  await page.getByRole('button', { name: '外观', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '外观设置' })
  for (const mode of ['浅色', '深色']) {
    await dialog.getByRole('radio', { name: mode, exact: true }).check()
    for (const [name, id] of [
      ['岩墨', 'slate'],
      ['海蓝', 'ocean'],
      ['松雾', 'pine'],
      ['暖砂', 'sand'],
    ]) {
      await dialog.getByRole('radio', { name, exact: true }).check()
      await expect(page.locator('html')).toHaveAttribute('data-palette', id)
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
      await page.screenshot({
        path: testInfo.outputPath(`${id}-${mode === '浅色' ? 'light' : 'dark'}.png`),
      })
    }
  }
  await dialog.getByRole('button', { name: '完成', exact: true }).click()
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'sand')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.getByLabel('密码', { exact: true }).fill('ui-test-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await expect(page.getByRole('heading', { name: '配置管理', exact: true })).toBeVisible()
  await expect(page.locator('html')).toHaveAttribute('data-palette', 'sand')
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
})

test('system changes, keyboard selection, Escape focus and narrow-screen layouts', async ({
  page,
}, testInfo) => {
  test.setTimeout(90000)
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await page.goto('/login')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.getByRole('button', { name: '外观', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '外观设置' })
  await dialog.getByRole('radio', { name: '跟随系统', exact: true }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(dialog.getByRole('radio', { name: '浅色', exact: true })).toBeChecked()
  await page.emulateMedia({ colorScheme: 'light' })
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await dialog.getByRole('radio', { name: '松雾', exact: true }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(dialog.getByRole('radio', { name: '海蓝', exact: true })).toBeChecked()
  await page.keyboard.press('Escape')
  await expect(dialog).not.toBeVisible()
  await expect(page.getByRole('button', { name: '外观', exact: true })).toBeFocused()
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 812, height: 375 },
  ]) {
    await page.setViewportSize(viewport)
    for (const mode of ['light', 'dark'] as const) {
      await setDisplayMode(page, mode)
      await page.getByRole('button', { name: '外观', exact: true }).click()
      await expect(dialog.getByRole('button', { name: '完成', exact: true })).toBeInViewport()
      await expect(dialog.getByRole('radio', { name: '跟随系统' })).toBeInViewport()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      )
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
      await page.screenshot({
        path: testInfo.outputPath(`appearance-${viewport.width}-${mode}.png`),
      })
      await page.keyboard.press('Escape')
    }
  }
})

test('theme changes preserve the configuration draft and editor undo history', async ({ page }) => {
  await page.goto('/configs')
  await page.getByLabel('密码', { exact: true }).fill('ui-test-password')
  await page.getByRole('button', { name: '登录控制台' }).click()
  await page.getByRole('link', { name: '新建配置', exact: true }).click()
  const editor = page.getByRole('textbox', { name: '配置内容', exact: true })
  await editor.fill('draft: before')
  await editor.press('Control+End')
  await editor.press('Enter')
  await editor.pressSequentially('extra: retained')
  await setDisplayMode(page, 'dark')
  await expect(editor.locator('.cm-line')).toHaveText(['draft: before', 'extra: retained'])
  await editor.press('Control+z')
  await expect(editor).not.toContainText('extra: retained')
})
