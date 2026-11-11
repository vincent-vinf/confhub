import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { readAppearance, type Palette, type Theme, type ThemeMode } from '../lib/appearance'

const ThemeContext = createContext<{
  theme: Theme
  mode: ThemeMode
  palette: Palette
  setMode: (mode: ThemeMode) => void
  setPalette: (palette: Palette) => void
}>({ theme: 'light', mode: 'system', palette: 'pine', setMode: () => {}, setPalette: () => {} })

export const useTheme = () => useContext(ThemeContext)

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setPreference] = useState(readAppearance)
  const [systemDark, setSystemDark] = useState(
    () => window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false,
  )
  const theme = preference.mode === 'system' ? (systemDark ? 'dark' : 'light') : preference.mode

  useEffect(() => {
    const media = window.matchMedia?.('(prefers-color-scheme: dark)')
    if (!media) return
    const update = () => setSystemDark(media.matches)
    update()
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])

  useLayoutEffect(() => {
    const root = document.documentElement
    root.dataset.theme = theme
    root.dataset.palette = preference.palette
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute('content', getComputedStyle(root).getPropertyValue('--bg').trim())
  }, [theme, preference.palette])

  useEffect(() => {
    try {
      localStorage.setItem('confhub.theme', preference.mode)
      localStorage.setItem('confhub.palette', preference.palette)
    } catch {
      // Preferences remain usable when browser storage is unavailable.
    }
  }, [preference])

  useEffect(() => {
    const update = (event: StorageEvent) => {
      if (event.key === null || event.key === 'confhub.theme' || event.key === 'confhub.palette')
        setPreference(readAppearance())
    }
    window.addEventListener('storage', update)
    return () => window.removeEventListener('storage', update)
  }, [])

  const value = useMemo(
    () => ({
      theme,
      ...preference,
      setMode: (mode: ThemeMode) => setPreference((previous) => ({ ...previous, mode })),
      setPalette: (palette: Palette) => setPreference((previous) => ({ ...previous, palette })),
    }),
    [theme, preference],
  )

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}
