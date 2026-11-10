import {
  focusManager,
  onlineManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { ClientsPage } from './clients'

vi.mock('../components/providers', () => ({ usePermission: () => true }))
let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
  vi.spyOn(api, 'clients').mockResolvedValue({ clients: [] })
  vi.spyOn(api, 'clientTags').mockResolvedValue([])
})
afterEach(() => {
  client.clear()
  vi.useRealTimers()
  focusManager.setFocused(undefined)
  onlineManager.setOnline(true)
})
function open() {
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <ClientsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

it('筛选按查询应用，刷新保留条件，重置恢复未筛选状态', async () => {
  open()
  await screen.findByRole('heading', { name: '暂无在线客户端' })
  expect(screen.getByRole('combobox', { name: '筛选标签值' })).toBeDisabled()
  fireEvent.change(screen.getByLabelText('客户端搜索'), { target: { value: ' node-1 ' } })
  fireEvent.change(screen.getByLabelText('Namespace'), { target: { value: 'public' } })
  fireEvent.change(screen.getByLabelText('Group'), { target: { value: 'prod' } })
  fireEvent.change(screen.getByLabelText('配置名称'), { target: { value: 'app.yaml' } })
  fireEvent.change(screen.getByRole('combobox', { name: '筛选标签名称' }), {
    target: { value: 'env' },
  })
  fireEvent.change(screen.getByRole('combobox', { name: '筛选标签值' }), {
    target: { value: 'Gray ' },
  })
  expect(api.clients).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole('button', { name: '查询' }))
  await screen.findByRole('heading', { name: '没有符合条件的在线客户端' })
  expect(api.clients).toHaveBeenLastCalledWith(
    expect.objectContaining({
      search: 'node-1',
      namespace: 'public',
      group: 'prod',
      name: 'app.yaml',
      tag: 'env',
      tag_value: 'Gray ',
    }),
    '',
    expect.any(AbortSignal),
  )
  const count = vi.mocked(api.clients).mock.calls.length
  fireEvent.click(screen.getByRole('button', { name: '刷新' }))
  await waitFor(() => expect(api.clients).toHaveBeenCalledTimes(count + 1))
  expect(api.clients).toHaveBeenLastCalledWith(
    expect.objectContaining({ search: 'node-1', tag_value: 'Gray ' }),
    '',
    expect.any(AbortSignal),
  )
  fireEvent.click(screen.getByRole('button', { name: '重置筛选' }))
  await screen.findByRole('heading', { name: '暂无在线客户端' })
  expect(screen.getByLabelText('客户端搜索')).toHaveValue('')
  expect(api.clients).toHaveBeenLastCalledWith(
    expect.objectContaining({ search: '', tag: '', tag_value: '' }),
    '',
    expect.any(AbortSignal),
  )
})

it('翻页带上已应用筛选，重新查询回到第一页，更换标签清空旧值', async () => {
  vi.mocked(api.clients).mockResolvedValue({ clients: [], next_after: 'cursor-1' })
  open()
  await screen.findByRole('heading', { name: '暂无在线客户端' })
  fireEvent.change(screen.getByLabelText('客户端搜索'), { target: { value: 'node' } })
  fireEvent.click(screen.getByRole('button', { name: '查询' }))
  await screen.findByRole('heading', { name: '没有符合条件的在线客户端' })
  await waitFor(() => expect(screen.getByRole('button', { name: '下一页客户端' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: '下一页客户端' }))
  await waitFor(() =>
    expect(api.clients).toHaveBeenLastCalledWith(
      expect.objectContaining({ search: 'node' }),
      'cursor-1',
      expect.any(AbortSignal),
    ),
  )
  await waitFor(() => expect(screen.getByRole('button', { name: '查询' })).toBeEnabled())
  fireEvent.change(screen.getByRole('combobox', { name: '筛选标签名称' }), {
    target: { value: 'env' },
  })
  fireEvent.change(screen.getByRole('combobox', { name: '筛选标签值' }), {
    target: { value: 'prod' },
  })
  fireEvent.change(screen.getByRole('combobox', { name: '筛选标签名称' }), {
    target: { value: 'region' },
  })
  expect(screen.getByRole('combobox', { name: '筛选标签值' })).toHaveValue('')
  fireEvent.click(screen.getByRole('button', { name: '查询' }))
  await waitFor(() =>
    expect(api.clients).toHaveBeenLastCalledWith(
      expect.objectContaining({ search: 'node', tag: 'region', tag_value: '' }),
      '',
      expect.any(AbortSignal),
    ),
  )
  expect(screen.getByRole('button', { name: '上一页客户端' })).toBeDisabled()
})

it('自动刷新默认关闭，开启定时刷新，关闭后仍可手动刷新', async () => {
  vi.useFakeTimers()
  focusManager.setFocused(true)
  async function advance(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms)
    })
  }
  open()
  await advance(1)
  const toggle = screen.getByRole('switch', { name: '自动刷新' })
  expect(toggle).not.toBeChecked()
  expect(api.clients).toHaveBeenCalledTimes(1)
  await advance(15000)
  await act(async () => {
    focusManager.setFocused(false)
    focusManager.setFocused(true)
    onlineManager.setOnline(false)
    onlineManager.setOnline(true)
  })
  await advance(1)
  expect(api.clients).toHaveBeenCalledTimes(1)
  fireEvent.click(toggle)
  await advance(1)
  expect(toggle).toBeChecked()
  expect(api.clients).toHaveBeenCalledTimes(2)
  await advance(5000)
  expect(api.clients).toHaveBeenCalledTimes(3)
  fireEvent.click(toggle)
  await advance(15000)
  expect(toggle).not.toBeChecked()
  expect(api.clients).toHaveBeenCalledTimes(3)
  fireEvent.click(screen.getByRole('button', { name: '刷新' }))
  await advance(1)
  expect(api.clients).toHaveBeenCalledTimes(4)
  expect(toggle).not.toBeChecked()
})
