import { expect, type Page } from '@playwright/test'

export async function setDisplayMode(page: Page, mode: 'light' | 'dark' | 'system') {
  const names = { light: '浅色', dark: '深色', system: '跟随系统' }
  const trigger = page.getByRole('button', { name: '外观', exact: true })
  await trigger.click()
  const dialog = page.getByRole('dialog', { name: '外观设置' })
  await dialog.getByRole('radio', { name: names[mode], exact: true }).check()
  await dialog.getByRole('button', { name: '完成', exact: true }).click()
  await expect(dialog).not.toBeVisible()
  await expect(trigger).toBeFocused()
}
