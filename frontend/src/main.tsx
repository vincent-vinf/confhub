import React, { lazy, Suspense } from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createBrowserRouter,
  Link,
  Navigate,
  RouterProvider,
  useRouteError,
} from 'react-router-dom'
import {
  AuthProvider,
  DirtyProvider,
  LeaveGuard,
  ThemeProvider,
  ToastProvider,
} from './components/providers'
import { AppGate, LogoutPage } from './components/layout'
import { Empty, Loading } from './components/ui'
import '@fontsource/ibm-plex-sans/latin-400.css'
import '@fontsource/ibm-plex-sans/latin-500.css'
import '@fontsource/ibm-plex-sans/latin-600.css'
import '@fontsource/jetbrains-mono/latin-400.css'
import './styles.css'

const ConfigListPage = lazy(() =>
  import('./pages/config-list').then((module) => ({ default: module.ConfigListPage })),
)
const ConfigDetailPage = lazy(() =>
  import('./pages/config-detail').then((module) => ({ default: module.ConfigDetailPage })),
)
const ConfigCreatePage = lazy(() =>
  import('./pages/config-create').then((module) => ({ default: module.ConfigCreatePage })),
)
const OrganizationPage = lazy(() =>
  import('./pages/organization').then((module) => ({ default: module.OrganizationPage })),
)
const ClientsPage = lazy(() =>
  import('./pages/clients').then((module) => ({ default: module.ClientsPage })),
)
const SettingsPage = lazy(() =>
  import('./pages/settings').then((module) => ({ default: module.SettingsPage })),
)

const client = new QueryClient({
  defaultOptions: {
    queries: { retry: false, staleTime: 15000, refetchOnWindowFocus: false },
    mutations: { retry: false },
  },
})
function Root() {
  return (
    <>
      <AppGate />
      <LeaveGuard />
    </>
  )
}
function RouteFailure() {
  useRouteError()
  return (
    <main className="startup">
      <Empty
        title="页面暂时无法显示"
        description="请重新打开配置列表。当前版本不会被修改。"
        action={
          <Link className="button primary" to="/configs">
            返回配置列表
          </Link>
        }
      />
    </main>
  )
}
const router = createBrowserRouter([
  {
    element: <Root />,
    errorElement: <RouteFailure />,
    children: [
      { path: '/', element: <Navigate to="/configs" replace /> },
      { path: '/login', element: <Navigate to="/configs" replace /> },
      {
        path: '/configs',
        element: (
          <Suspense fallback={<Loading />}>
            <ConfigListPage />
          </Suspense>
        ),
      },
      {
        path: '/new',
        element: (
          <Suspense fallback={<Loading />}>
            <ConfigCreatePage />
          </Suspense>
        ),
      },
      {
        path: '/configs/:namespace/:group/:name',
        element: (
          <Suspense fallback={<Loading />}>
            <ConfigDetailPage />
          </Suspense>
        ),
      },
      {
        path: '/organization',
        element: (
          <Suspense fallback={<Loading />}>
            <OrganizationPage />
          </Suspense>
        ),
      },
      {
        path: '/settings',
        element: (
          <Suspense fallback={<Loading />}>
            <SettingsPage />
          </Suspense>
        ),
      },
      {
        path: '/clients',
        element: (
          <Suspense fallback={<Loading />}>
            <ClientsPage />
          </Suspense>
        ),
      },
      { path: '/logout', element: <LogoutPage /> },
      {
        path: '*',
        element: (
          <Empty
            title="找不到这个页面"
            description="检查地址，或回到配置列表。"
            action={
              <Link className="button primary" to="/configs">
                返回配置列表
              </Link>
            }
          />
        ),
      },
    ],
  },
])
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={client}>
      <ThemeProvider>
        <ToastProvider>
          <AuthProvider>
            <DirtyProvider>
              <RouterProvider router={router} />
            </DirtyProvider>
          </AuthProvider>
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </React.StrictMode>,
)
