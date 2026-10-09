import { useState, type FormEvent } from 'react'
import { KeyRound, ShieldCheck } from 'lucide-react'
import { api } from '../lib/api'
import { bytes } from '../lib/config'
import { Button, ErrorNotice } from '../components/ui'
import { useDirty, useToast, usePending } from '../components/providers'

export function SettingsPage() {
  const [oldPassword, setOldPassword] = useState(''),
    [newPassword, setNewPassword] = useState(''),
    [repeat, setRepeat] = useState('')
  const [error, setError] = useState<unknown>()
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  usePending('正在修改密码', busy)
  useDirty('账号设置中尚未提交的密码修改', !!oldPassword || !!newPassword || !!repeat)
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    const errors: Record<string, string> = {}
    if (!oldPassword) errors.old = '请输入当前密码。'
    if (bytes(newPassword) < 8 || bytes(newPassword) > 72) errors.new = '新密码应为 8–72 字节。'
    if (repeat !== newPassword) errors.repeat = '两次新密码不一致。'
    setFieldErrors(errors)
    setError(undefined)
    if (Object.keys(errors).length) {
      document.getElementById(`password-${Object.keys(errors)[0]}`)?.focus()
      return
    }
    setBusy(true)
    try {
      await api.password(oldPassword, newPassword)
      setOldPassword('')
      setNewPassword('')
      setRepeat('')
      toast('密码已更新，下次登录请使用新密码。')
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <span className="eyebrow">ACCOUNT SETTINGS</span>
          <h1>账号设置</h1>
          <p>管理管理员账户的登录密码。</p>
        </div>
      </div>
      <div className="settings-layout">
        <section className="panel">
          <div className="panel-toolbar">
            <div className="panel-title">
              <KeyRound size={18} aria-hidden="true" />
              修改密码
            </div>
          </div>
          <form className="settings-form form-stack" onSubmit={submit}>
            <label>
              当前密码
              <input
                id="password-old"
                type="password"
                autoComplete="current-password"
                value={oldPassword}
                onChange={(e) => setOldPassword(e.target.value)}
                aria-invalid={!!fieldErrors.old}
                aria-describedby={fieldErrors.old ? 'error-old' : undefined}
              />
              {fieldErrors.old && (
                <small className="field-error" id="error-old">
                  {fieldErrors.old}
                </small>
              )}
            </label>
            <label>
              新密码
              <input
                id="password-new"
                type="password"
                autoComplete="new-password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                aria-invalid={!!fieldErrors.new}
                aria-describedby={fieldErrors.new ? 'error-new' : 'password-help'}
              />
              {fieldErrors.new && (
                <small className="field-error" id="error-new">
                  {fieldErrors.new}
                </small>
              )}
            </label>
            <small id="password-help">8–72 字节。支持粘贴和密码管理器。</small>
            <label>
              再次输入新密码
              <input
                id="password-repeat"
                type="password"
                autoComplete="new-password"
                value={repeat}
                onChange={(e) => setRepeat(e.target.value)}
                aria-invalid={!!fieldErrors.repeat}
                aria-describedby={fieldErrors.repeat ? 'error-repeat' : undefined}
              />
              {fieldErrors.repeat && (
                <small className="field-error" id="error-repeat">
                  {fieldErrors.repeat}
                </small>
              )}
            </label>
            <ErrorNotice error={error} />
            <div>
              <Button variant="primary" busy={busy} type="submit">
                更新密码
              </Button>
            </div>
          </form>
        </section>
        <aside className="account-summary">
          <span className="avatar large">A</span>
          <h2>admin</h2>
          <BadgeLine />
          <div className="account-note">
            <ShieldCheck size={22} aria-hidden="true" />
            <p>
              修改密码会影响后续登录。已登录的会话在到期前仍可使用；共享管理设备使用后请退出登录。
            </p>
          </div>
        </aside>
      </div>
    </>
  )
}
function BadgeLine() {
  return <span className="badge success">管理员</span>
}
