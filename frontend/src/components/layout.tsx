import * as Dialog from '@radix-ui/react-dialog'
import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Layers3,
  Files,
  FolderTree,
  Settings2,
  Moon,
  Sun,
  Menu,
  ChevronRight,
  LogOut,
  X,
  ArrowUpRight,
} from 'lucide-react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { api } from '../lib/api'
import { useAuth, useTheme } from './providers'
import { Button, ErrorNotice, Loading } from './ui'
import { LoginPage, Reauthenticate } from '../pages/login'

export function AppGate() {
  const auth = useAuth()
  if (auth.status === 'checking')
    return (
      <main className="startup">
        <span className="brand-mark">
          <Layers3 aria-hidden="true" />
        </span>
        {auth.bootstrapError ? (
          <ErrorNotice error={auth.bootstrapError} onRetry={auth.retry} />
        ) : (
          <Loading label="正在连接配置中心…" />
        )}
      </main>
    )
  if (auth.status === 'anonymous') return <LoginPage />
  return (
    <>
      <Layout />
      <Reauthenticate />
    </>
  )
}
function Layout() {
  const [menu, setMenu] = useState(false)
  const location = useLocation()
  const navigate = useNavigate()
  const { theme, toggle } = useTheme()
  const health = useQuery({
    queryKey: ['health'],
    queryFn: ({ signal }) => api.ready(signal),
    refetchInterval: 30000,
    refetchOnWindowFocus: true,
  })
  useEffect(() => setMenu(false), [location.pathname, location.search])
  const title = location.pathname.startsWith('/organization')
    ? '命名空间'
    : location.pathname.startsWith('/settings')
      ? '账号设置'
      : '配置管理'
  const sidebar = (
    <>
      <a
        href="/configs"
        className="brand"
        onClick={(e) => {
          e.preventDefault()
          navigate('/configs')
        }}
      >
        <span className="brand-mark">
          <Layers3 size={22} aria-hidden="true" />
        </span>
        <span>
          ConfHub<small>配置中心</small>
        </span>
      </a>
      <div className="nav-section-label">工作空间</div>
      <nav aria-label="主导航">
        <NavLink
          to="/configs"
          className={({ isActive }) => (isActive || location.pathname === '/new' ? 'active' : '')}
        >
          <Files size={19} aria-hidden="true" />
          配置管理
        </NavLink>
        <NavLink to="/organization">
          <FolderTree size={19} aria-hidden="true" />
          命名空间
        </NavLink>
        <NavLink to="/settings">
          <Settings2 size={19} aria-hidden="true" />
          账号设置
        </NavLink>
      </nav>
      <div className="sidebar-bottom">
        <div className="service-status">
          <span
            className={`status-dot ${health.data === true ? 'online' : ''}`}
            aria-hidden="true"
          />
          <span>
            {health.isPending ? '检查服务状态…' : health.data ? '服务可用' : '服务暂不可用'}
          </span>
        </div>
        {health.dataUpdatedAt > 0 && (
          <small>
            最近检查 {new Date(health.dataUpdatedAt).toLocaleTimeString('zh-CN', { hour12: false })}
          </small>
        )}
        <div className="sidebar-caption">
          轻量、专注的配置管理 <ArrowUpRight size={12} aria-hidden="true" />
        </div>
      </div>
    </>
  )
  return (
    <div className="app-shell">
      <a className="skip-link" href="#main">
        跳到主要内容
      </a>
      <aside className="sidebar desktop-sidebar">{sidebar}</aside>
      <Dialog.Root open={menu} onOpenChange={setMenu}>
        <Dialog.Portal>
          <Dialog.Overlay className="mobile-nav-scrim" />
          <Dialog.Content
            className="sidebar mobile-sidebar"
            aria-describedby={undefined}
            onCloseAutoFocus={(event) => {
              event.preventDefault()
              document.querySelector<HTMLButtonElement>('.mobile-menu')?.focus()
            }}
          >
            <Dialog.Title className="sr-only">导航菜单</Dialog.Title>
            <Button
              className="mobile-close icon-button"
              variant="ghost"
              aria-label="关闭导航"
              onClick={() => setMenu(false)}
            >
              <X size={20} aria-hidden="true" />
            </Button>
            {sidebar}
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
      <div className="workspace">
        <header className="topbar">
          <Button
            variant="ghost"
            className="icon-button mobile-menu"
            aria-label="打开导航"
            aria-expanded={menu}
            onClick={() => setMenu(true)}
          >
            <Menu size={20} aria-hidden="true" />
          </Button>
          <div className="topbar-breadcrumb">
            <span>工作空间</span>
            <ChevronRight size={14} aria-hidden="true" />
            <strong>{title}</strong>
          </div>
          <div className="topbar-actions">
            <Button
              variant="ghost"
              className="icon-button"
              aria-label={theme === 'light' ? '切换深色主题' : '切换浅色主题'}
              onClick={toggle}
            >
              {theme === 'light' ? (
                <Moon size={18} aria-hidden="true" />
              ) : (
                <Sun size={18} aria-hidden="true" />
              )}
            </Button>
            <span className="avatar" aria-hidden="true">
              A
            </span>
            <span className="account-name">admin</span>
            <Button
              variant="ghost"
              className="icon-button"
              aria-label="退出登录"
              onClick={() => navigate('/logout')}
            >
              <LogOut size={17} aria-hidden="true" />
            </Button>
          </div>
        </header>
        <main id="main" className="page" tabIndex={-1}>
          <Outlet />
        </main>
        <footer className="workspace-footer">
          <span>ConfHub</span>
          <span>让每次配置变更都有迹可循</span>
        </footer>
      </div>
    </div>
  )
}
export function LogoutPage() {
  const auth = useAuth()
  const navigate = useNavigate()
  const started = useRef(false)
  const [error, setError] = useState<unknown>()
  async function logout() {
    setError(undefined)
    try {
      await auth.logout()
      navigate('/login', { replace: true })
    } catch (error) {
      setError(error)
    }
  }
  useEffect(() => {
    if (started.current) return
    started.current = true
    void logout()
  }, [])
  return error ? (
    <ErrorNotice error={error} onRetry={() => void logout()} />
  ) : (
    <Loading label="正在退出登录…" />
  )
}
