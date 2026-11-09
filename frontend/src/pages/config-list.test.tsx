import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api, ApiError } from '../lib/api'
import { ConfigListPage } from './config-list'

vi.mock('../components/providers', () => ({
  useAuth: () => ({ user: { role: 'edge', read_only: true } }),
  usePermission: (action: string) => action === 'config.read',
  usePending: () => {},
  useToast: () => () => {},
}))

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
  vi.spyOn(api, 'namespaces').mockResolvedValue([])
  vi.spyOn(api, 'groups').mockResolvedValue(['DEFAULT_GROUP'])
  vi.spyOn(api, 'list').mockResolvedValue([])
})
afterEach(() => client.clear())

function open(path = '/configs') {
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <ConfigListPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

it('空镜像显示空状态，不误报命名空间权限或持续加载', async () => {
  open()
  expect(await screen.findByRole('heading', { name: '暂无已同步配置' })).toBeVisible()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.queryByText('正在读取配置…')).not.toBeInTheDocument()
  expect(api.groups).not.toHaveBeenCalled()
  expect(api.list).not.toHaveBeenCalled()
})

it('刷新空镜像后发现首次同步的命名空间、分组及配置', async () => {
  open()
  await screen.findByRole('heading', { name: '暂无已同步配置' })
  vi.mocked(api.namespaces).mockResolvedValue(['public'])
  vi.mocked(api.list).mockResolvedValue([
    {
      id: 'first',
      key: { namespace: 'public', group: 'DEFAULT_GROUP', name: 'first.yaml' },
      revision: 1,
      last_version: 1,
      global_version: 1,
    },
  ])
  fireEvent.click(screen.getByRole('button', { name: '刷新配置列表' }))
  expect(await screen.findByRole('link', { name: 'first.yaml' })).toBeVisible()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('删除最后一份配置后刷新，不残留旧列表或显示权限错误', async () => {
  vi.mocked(api.namespaces).mockResolvedValue(['public'])
  vi.mocked(api.list).mockResolvedValue([
    {
      id: 'last',
      key: { namespace: 'public', group: 'DEFAULT_GROUP', name: 'last.yaml' },
      revision: 1,
      last_version: 1,
      global_version: 1,
    },
  ])
  open()
  await screen.findByRole('link', { name: 'last.yaml' })
  vi.mocked(api.namespaces).mockResolvedValue([])
  fireEvent.click(screen.getByRole('button', { name: '刷新配置列表' }))
  expect(await screen.findByRole('heading', { name: '暂无已同步配置' })).toBeVisible()
  expect(screen.queryByRole('link', { name: 'last.yaml' })).not.toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('分组目录为空时显示空状态', async () => {
  vi.mocked(api.namespaces).mockResolvedValue(['public'])
  vi.mocked(api.groups).mockResolvedValue([])
  open()
  expect(await screen.findByRole('heading', { name: '暂无已同步配置' })).toBeVisible()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('非空目录中无权访问的命名空间仍显示错误', async () => {
  vi.mocked(api.namespaces).mockResolvedValue(['allowed'])
  open('/configs?namespace=forbidden&group=DEFAULT_GROUP')
  expect(await screen.findByRole('alert')).toHaveTextContent('此命名空间不可访问')
  expect(screen.queryByRole('heading', { name: '暂无已同步配置' })).not.toBeInTheDocument()
})

it('命名空间请求失败时保留真实错误', async () => {
  vi.mocked(api.namespaces).mockRejectedValue(new ApiError(503, '同步尚未就绪'))
  open()
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('同步尚未就绪'))
  expect(screen.queryByRole('heading', { name: '暂无已同步配置' })).not.toBeInTheDocument()
})
