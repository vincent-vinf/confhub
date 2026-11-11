import { useId, useState } from 'react'
import { Check, Monitor, Moon, Palette, Sun } from 'lucide-react'
import { palettes, type ThemeMode } from '../lib/appearance'
import { useTheme } from './theme-provider'
import { Button, Modal } from './ui'

const modes = [
  { id: 'light', name: '浅色', icon: Sun },
  { id: 'dark', name: '深色', icon: Moon },
  { id: 'system', name: '跟随系统', icon: Monitor },
] as const satisfies readonly { id: ThemeMode; name: string; icon: typeof Sun }[]

export function AppearanceSettings() {
  const [open, setOpen] = useState(false)
  const { theme, mode, palette, setMode, setPalette } = useTheme()
  const id = useId()
  return (
    <>
      <Button
        variant="ghost"
        className="appearance-trigger"
        aria-haspopup="dialog"
        onClick={() => setOpen(true)}
      >
        <Palette size={18} aria-hidden="true" />
        外观
      </Button>
      <Modal
        open={open}
        title="外观设置"
        onClose={() => setOpen(false)}
        footer={
          <>
            <span className="appearance-saved">
              <Check size={15} aria-hidden="true" />
              即时生效，自动保存
            </span>
            <Button variant="primary" onClick={() => setOpen(false)}>
              完成
            </Button>
          </>
        }
      >
        <div className="appearance-settings">
          <fieldset>
            <legend>显示模式</legend>
            <div className="appearance-modes">
              {modes.map(({ id: value, name, icon: Icon }) => (
                <label key={value} className="appearance-mode">
                  <input
                    className="appearance-radio"
                    type="radio"
                    name={`${id}-mode`}
                    value={value}
                    checked={mode === value}
                    onChange={() => setMode(value)}
                  />
                  <Icon size={17} aria-hidden="true" />
                  <span>{name}</span>
                </label>
              ))}
            </div>
          </fieldset>
          <fieldset>
            <legend>主题配色</legend>
            <div className="appearance-palettes">
              {palettes.map((item) => (
                <label key={item.id} className="appearance-palette">
                  <input
                    className="appearance-radio"
                    type="radio"
                    name={`${id}-palette`}
                    value={item.id}
                    checked={palette === item.id}
                    onChange={() => setPalette(item.id)}
                  />
                  <span
                    className="theme-preview"
                    data-palette={item.id}
                    data-theme={theme}
                    aria-hidden="true"
                  >
                    <span className="preview-nav">
                      <i />
                      <i />
                      <i />
                    </span>
                    <span className="preview-workspace">
                      <span className="preview-toolbar">
                        <i />
                        <i />
                      </span>
                      <span className="preview-cards">
                        <i />
                        <i />
                      </span>
                    </span>
                  </span>
                  <span className="palette-caption">
                    <span>{item.name}</span>
                    <span className="palette-indicator" aria-hidden="true">
                      {palette === item.id && <Check size={13} />}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </fieldset>
        </div>
      </Modal>
    </>
  )
}
