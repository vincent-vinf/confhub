import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { AppearanceSettings } from './appearance-settings'
import { ThemeProvider } from './theme-provider'

let media: EventTarget & { matches: boolean }
beforeEach(() => {
  localStorage.clear()
  media = Object.assign(new EventTarget(), { matches: false })
  vi.stubGlobal('matchMedia', () => media)
})
afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
  delete document.documentElement.dataset.theme
  delete document.documentElement.dataset.palette
})

test('system mode follows changes; an explicit mode stays fixed and palette persists', async () => {
  const user = userEvent.setup()
  render(
    <ThemeProvider>
      <AppearanceSettings />
    </ThemeProvider>,
  )
  const root = document.documentElement
  expect(root.dataset.theme).toBe('light')
  await user.click(screen.getByRole('button', { name: '外观' }))
  expect(screen.getByRole('radio', { name: '跟随系统' })).toBeChecked()
  await user.click(screen.getByRole('radio', { name: '海蓝' }))
  expect(root.dataset.palette).toBe('ocean')
  expect(localStorage.getItem('confhub.palette')).toBe('ocean')
  act(() => {
    media.matches = true
    media.dispatchEvent(new Event('change'))
  })
  expect(root.dataset.theme).toBe('dark')
  await user.click(screen.getByRole('radio', { name: '浅色' }))
  expect(localStorage.getItem('confhub.theme')).toBe('light')
  act(() => {
    media.matches = false
    media.dispatchEvent(new Event('change'))
  })
  act(() => {
    media.matches = true
    media.dispatchEvent(new Event('change'))
  })
  expect(root.dataset.theme).toBe('light')
})

test('existing dark-mode preference survives the appearance upgrade', () => {
  localStorage.setItem('confhub.theme', 'dark')
  render(
    <ThemeProvider>
      <AppearanceSettings />
    </ThemeProvider>,
  )
  expect(document.documentElement.dataset.theme).toBe('dark')
  expect(document.documentElement.dataset.palette).toBe('pine')
})

test('invalid or blocked storage still allows appearance changes', async () => {
  localStorage.setItem('confhub.theme', 'invalid')
  localStorage.setItem('confhub.palette', 'invalid')
  const user = userEvent.setup()
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
    throw new Error('unavailable')
  })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new Error('unavailable')
  })
  render(
    <ThemeProvider>
      <AppearanceSettings />
    </ThemeProvider>,
  )
  await user.click(screen.getByRole('button', { name: '外观' }))
  await user.click(screen.getByRole('radio', { name: '暖砂' }))
  await user.click(screen.getByRole('radio', { name: '深色' }))
  expect(document.documentElement.dataset.palette).toBe('sand')
  expect(document.documentElement.dataset.theme).toBe('dark')
})
