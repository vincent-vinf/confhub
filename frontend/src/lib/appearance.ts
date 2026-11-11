export const palettes = [
  { id: 'pine', name: '松雾' },
  { id: 'ocean', name: '海蓝' },
  { id: 'slate', name: '岩墨' },
  { id: 'sand', name: '暖砂' },
] as const

export type Palette = (typeof palettes)[number]['id']
export type ThemeMode = 'light' | 'dark' | 'system'
export type Theme = Exclude<ThemeMode, 'system'>

export function readAppearance(): { mode: ThemeMode; palette: Palette } {
  try {
    const mode = localStorage.getItem('confhub.theme')
    const palette = localStorage.getItem('confhub.palette')
    return {
      mode: mode === 'light' || mode === 'dark' ? mode : 'system',
      palette: palettes.find((item) => item.id === palette)?.id ?? 'pine',
    }
  } catch {
    return { mode: 'system', palette: 'pine' }
  }
}
