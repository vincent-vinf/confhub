import { useState, type FormEvent } from 'react'
import { ArrowRight, Layers3, LockKeyhole } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../components/providers'
import { Button, ErrorNotice, Modal } from '../components/ui'

function LoginForm({ expired = false }: { expired?: boolean }) {
  const auth = useAuth()
  const navigate = useNavigate()
  const [password, setPassword] = useState('')
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError(undefined)
    try {
      await auth.login(password)
      setPassword('')
      if (!expired && ['/login', '/logout', '/'].includes(window.location.pathname))
        navigate('/configs', { replace: true })
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form onSubmit={submit} className="form-stack">
      <label>
        用户名
        <input name="username" autoComplete="username" value="admin" readOnly />
      </label>
      <label>
        密码
        <input
          name="password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(event) => {
            setPassword(event.target.value)
            setError(undefined)
          }}
          autoFocus={expired}
          aria-describedby={error ? 'login-error' : undefined}
        />
      </label>
      <div id="login-error">
        <ErrorNotice error={error} />
      </div>
      <Button type="submit" variant="primary" busy={busy} disabled={!password}>
        登录控制台 <ArrowRight size={18} aria-hidden="true" />
      </Button>
    </form>
  )
}
export function LoginPage() {
  return (
    <main className="login-page">
      <section className="login-story">
        <a className="brand" href="/login">
          <span className="brand-mark">
            <Layers3 size={23} aria-hidden="true" />
          </span>
          <span>ConfHub</span>
        </a>
        <div className="login-copy">
          <h1>
            每一次变更，
            <br />
            都清晰可控。
          </h1>
        </div>
        <small>ConfHub · 轻量配置中心</small>
      </section>
      <section className="login-panel">
        <div className="login-card">
          <div className="login-lock">
            <LockKeyhole size={22} aria-hidden="true" />
          </div>
          <h2>登录控制台</h2>
          <LoginForm />
        </div>
      </section>
    </main>
  )
}
export function Reauthenticate() {
  const auth = useAuth()
  return (
    <Modal
      closable={false}
      open={auth.status === 'expired'}
      title="登录已过期"
      description="重新登录后继续操作。当前编辑和待确认的变更都已保留。"
      onClose={() => {}}
    >
      <LoginForm expired />
    </Modal>
  )
}
