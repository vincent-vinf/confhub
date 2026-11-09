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

it('默认展示全部分组，保留同名配置和各自的完整路径', async () => {
  vi.mocked(api.namespaces).mockResolvedValue(['public'])
  vi.mocked(api.groups).mockResolvedValue(['A', 'B'])
  vi.mocked(api.list).mockImplementation(async (namespace, group) => [
    {
      id: group,
      key: { namespace, group, name: 'app.yaml' },
      revision: 1,
      last_version: 1,
      global_version: 1,
      published_at: '2026-11-09T09:00:00Z',
    },
  ])
  open()
  await waitFor(() => expect(screen.getAllByRole('link', { name: 'app.yaml' })).toHaveLength(2))
  expect(screen.getByRole('combobox', { name: '分组' })).toHaveTextContent('全部分组')
  expect(screen.getByRole('columnheader', { name: /分组/ })).toBeVisible()
  expect(
    screen.getAllByRole('link', { name: 'app.yaml' }).map((el) => el.getAttribute('href')),
  ).toEqual(['/configs/public/A/app.yaml', '/configs/public/B/app.yaml'])
  expect(screen.getByRole('checkbox', { name: '选择 A / app.yaml' })).toBeVisible()
  expect(screen.getByRole('checkbox', { name: '选择 B / app.yaml' })).toBeVisible()
  expect(document.querySelector('time')).toHaveAttribute('datetime', '2026-11-09T09:00:00Z')
  expect(api.list).toHaveBeenCalledWith('public', 'A', '', 200, expect.any(AbortSignal))
})

it('对完整结果排序后分页，切换排序返回第一页并清空选择', async () => {
  vi.mocked(api.namespaces).mockResolvedValue(['public'])
  vi.mocked(api.list).mockResolvedValue(
    Array.from({ length: 30 }, (_, i) => ({
      id: String(i),
      key: { namespace: 'public', group: 'DEFAULT_GROUP', name: `item-${i + 1}.yaml` },
      revision: 1,
      last_version: i + 1,
      global_version: i + 1,
      published_at: new Date(Date.UTC(2026, 10, i + 1)).toISOString(),
    })),
  )
  open()
  await screen.findByRole('link', { name: 'item-1.yaml' })
  fireEvent.click(screen.getByRole('button', { name: '下一页' }))
  await screen.findByRole('link', { name: 'item-30.yaml' })
  fireEvent.click(screen.getByRole('checkbox', { name: '全选当前页配置' }))
  fireEvent.click(screen.getByRole('button', { name: '配置名称，点击降序排列' }))
  await waitFor(() =>
    expect(screen.getAllByRole('link', { name: /^item-/ })[0]).toHaveTextContent('item-30.yaml'),
  )
  expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled()
  expect(screen.queryByRole('group', { name: '批量配置操作' })).not.toBeInTheDocument()
  expect(screen.getByRole('columnheader', { name: /配置名称/ })).toHaveAttribute(
    'aria-sort',
    'descending',
  )
  for (const column of ['全量版本', '全量发布时间']) {
    fireEvent.click(screen.getByRole('button', { name: `${column}，点击升序排列` }))
    expect(screen.getAllByRole('link', { name: /^item-/ })[0]).toHaveTextContent('item-1.yaml')
    fireEvent.click(screen.getByRole('button', { name: `${column}，点击降序排列` }))
    expect(screen.getAllByRole('link', { name: /^item-/ })[0]).toHaveTextContent('item-30.yaml')
  }
})

it('显式指定分组时隐藏分组列且只请求该分组', async () => {
  vi.mocked(api.namespaces).mockResolvedValue(['public'])
  vi.mocked(api.groups).mockResolvedValue(['DEFAULT_GROUP', 'OTHER'])
  vi.mocked(api.list).mockResolvedValue([
    {
      id: 'one',
      key: { namespace: 'public', group: 'OTHER', name: 'one.yaml' },
      revision: 1,
      last_version: 1,
      global_version: 1,
    },
  ])
  open('/configs?namespace=public&group=OTHER')
  await screen.findByRole('link', { name: 'one.yaml' })
  expect(screen.queryByRole('columnheader', { name: /分组/ })).not.toBeInTheDocument()
  expect(api.list).toHaveBeenCalledTimes(1)
  expect(api.list).toHaveBeenCalledWith('public', 'OTHER', '', 200, expect.any(AbortSignal))
})
