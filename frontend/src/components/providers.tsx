import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useBlocker } from 'react-router-dom'
import { CheckCircle2, X } from 'lucide-react'
import { api, ApiError } from '../lib/api'
import { Button, Modal } from './ui'

type Theme = 'light' | 'dark'
const ThemeContext = createContext<{ theme: Theme; toggle: () => void }>({
  theme: 'light',
  toggle: () => {},
})
export const useTheme = () => useContext(ThemeContext)
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(() => {
    try {
      return localStorage.getItem('confhub.theme') === 'dark' ? 'dark' : 'light'
    } catch {
      return 'light'
    }
  })
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    try {
      localStorage.setItem('confhub.theme', theme)
    } catch {
      /* The preference is optional. */
    }
  }, [theme])
  return (
    <ThemeContext.Provider
      value={{ theme, toggle: () => setTheme((t) => (t === 'light' ? 'dark' : 'light')) }}
    >
      {children}
    </ThemeContext.Provider>
  )
}
const ToastContext = createContext<(message: string) => void>(() => {})
export const useToast = () => useContext(ToastContext)
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toast, setToast] = useState<{ message: string; id: number }>()
  const show = useCallback((message: string) => setToast({ message, id: Date.now() }), [])
  useEffect(() => {
    if (!toast) return
    const timer = setTimeout(() => setToast(undefined), 5000)
    return () => clearTimeout(timer)
  }, [toast])
  return (
    <ToastContext.Provider value={show}>
      {children}
      <div className="toast-region" role="status" aria-live="polite" aria-atomic="true">
        {toast && (
          <div className="toast">
            <CheckCircle2 size={20} aria-hidden="true" />
            <span>{toast.message}</span>
            <Button
              variant="ghost"
              aria-label="关闭提示"
              className="icon-button"
              onClick={() => setToast(undefined)}
            >
              <X size={16} aria-hidden="true" />
            </Button>
          </div>
        )}
      </div>
    </ToastContext.Provider>
  )
}
type AuthStatus = 'checking' | 'anonymous' | 'authenticated' | 'expired'
type Auth = {
  status: AuthStatus
  bootstrapError?: Error
  login: (password: string) => Promise<void>
  logout: () => Promise<void>
  retry: () => void
}
const AuthContext = createContext<Auth>(null!)
export const useAuth = () => useContext(AuthContext)
export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<AuthStatus>('checking')
  const [bootstrapError, setBootstrapError] = useState<Error>()
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    api
      .namespaces(controller.signal)
      .then((namespaces) => {
        if (controller.signal.aborted) return
        queryClient.setQueryData(['namespaces'], namespaces)
        setStatus('authenticated')
        setBootstrapError(undefined)
      })
      .catch((error) => {
        if (controller.signal.aborted) return
        if (error instanceof ApiError && error.status === 401) setStatus('anonymous')
        else setBootstrapError(error)
      })
    return () => controller.abort()
  }, [attempt, queryClient])
  useEffect(() => {
    const expire = () =>
      setStatus((s) => (s === 'authenticated' || s === 'expired' ? 'expired' : 'anonymous'))
    window.addEventListener('confhub:unauthorized', expire)
    return () => window.removeEventListener('confhub:unauthorized', expire)
  }, [])
  const login = async (password: string) => {
    await api.login('admin', password)
    setStatus('authenticated')
    setBootstrapError(undefined)
    await queryClient.invalidateQueries()
  }
  const logout = async () => {
    await api.logout()
    queryClient.clear()
    setStatus('anonymous')
  }
  return (
    <AuthContext.Provider
      value={{
        status,
        bootstrapError,
        login,
        logout,
        retry: () => {
          setBootstrapError(undefined)
          setStatus('checking')
          setAttempt((a) => a + 1)
        },
      }}
    >
      {children}
    </AuthContext.Provider>
  )
}
const DirtyContext = createContext<{
  owners: Map<string, string>
  update: (id: string, label?: string) => void
  pending: Map<string, string>
  markPending: (id: string, label?: string) => void
  permit: () => void
  consumePermit: () => boolean
}>(null!)
export function DirtyProvider({ children }: { children: ReactNode }) {
  const [owners, setOwners] = useState(new Map<string, string>())
  const [pending, setPending] = useState(new Map<string, string>())
  const permitted = useRef(false)
  const permit = useCallback(() => {
    permitted.current = true
  }, [])
  const consumePermit = useCallback(() => {
    const allowed = permitted.current
    permitted.current = false
    return allowed
  }, [])
  const markPending = useCallback(
    (id: string, label?: string) =>
      setPending((previous) => {
        if (previous.get(id) === label) return previous
        const next = new Map(previous)
        if (label) next.set(id, label)
        else next.delete(id)
        return next
      }),
    [],
  )
  const update = useCallback(
    (id: string, label?: string) =>
      setOwners((previous) => {
        if (previous.get(id) === label) return previous
        const next = new Map(previous)
        if (label) next.set(id, label)
        else next.delete(id)
        return next
      }),
    [],
  )
  const value = useMemo(
    () => ({ owners, update, pending, markPending, permit, consumePermit }),
    [owners, update, pending, markPending, permit, consumePermit],
  )
  useEffect(() => {
    if (!owners.size && !pending.size) return
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [owners.size, pending.size])
  return <DirtyContext.Provider value={value}>{children}</DirtyContext.Provider>
}
export function useDirty(label: string, dirty: boolean) {
  const id = useId()
  const { update } = useContext(DirtyContext)
  useEffect(() => {
    update(id, dirty ? label : undefined)
    return () => update(id)
  }, [id, label, dirty, update])
}
export const useNavigationPermit = () => useContext(DirtyContext).permit
export function usePending(label: string, busy: boolean) {
  const id = useId()
  const { markPending } = useContext(DirtyContext)
  useEffect(() => {
    markPending(id, busy ? label : undefined)
    return () => markPending(id)
  }, [id, label, busy, markPending])
}
export function LeaveGuard() {
  const { owners, pending, consumePermit } = useContext(DirtyContext)
  const blocker = useBlocker(({ currentLocation, nextLocation }) => {
    if (consumePermit()) return false
    if (!owners.size && !pending.size) return false
    if (currentLocation.pathname !== nextLocation.pathname) return true
    const current = new URLSearchParams(currentLocation.search),
      next = new URLSearchParams(nextLocation.search)
    return (
      current.get('namespace') !== next.get('namespace') ||
      current.get('group') !== next.get('group')
    )
  })
  return (
    <Modal
      open={blocker.state === 'blocked'}
      title={pending.size ? '正在提交变更' : '离开并放弃更改？'}
      description={
        pending.size
          ? '请等待操作完成后再离开，避免无法确认提交结果。'
          : '尚未保存的编辑会被丢弃。你可以留在这里继续编辑。'
      }
      onClose={() => blocker.state === 'blocked' && blocker.reset()}
      footer={
        <>
          <Button onClick={() => blocker.state === 'blocked' && blocker.reset()}>继续编辑</Button>
          <Button
            variant="danger"
            disabled={pending.size > 0}
            onClick={() => blocker.state === 'blocked' && blocker.proceed()}
          >
            放弃并离开
          </Button>
        </>
      }
    >
      <ul className="plain-list">
        {[...new Set([...owners.values(), ...pending.values()])].map((label, index) => (
          <li key={index}>{label}</li>
        ))}
      </ul>
    </Modal>
  )
}
