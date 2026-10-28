import { useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { TagInput } from './tag-input'

vi.mock('./providers', () => ({ usePermission: () => true }))

let client: QueryClient
beforeEach(() => {
  vi.useFakeTimers()
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
})
afterEach(() => {
  client.clear()
  vi.useRealTimers()
})

function Input({ tag }: { tag?: string }) {
  const [value, setValue] = useState('')
  return (
    <QueryClientProvider client={client}>
      <TagInput label="标签" value={value} onChange={setValue} tag={tag} />
    </QueryClientProvider>
  )
}

function deferred() {
  let resolve!: (value: string[]) => void
  const promise = new Promise<string[]>((done) => (resolve = done))
  return { promise, resolve }
}

async function advance(ms = 1) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

it('前缀请求期间保留建议和方向键操作，返回后按标签内容保持选中项', async () => {
  client.setQueryData(['client-tags', undefined, ''], ['tag-01', 'tag-02', 'tag-03'])
  const pending = deferred()
  vi.spyOn(api, 'clientTags').mockReturnValue(pending.promise)
  render(<Input />)
  const input = screen.getByRole('combobox')
  fireEvent.focus(input)
  fireEvent.change(input, { target: { value: 'tag-' } })
  fireEvent.keyDown(input, { key: 'ArrowDown' })
  expect(screen.getByRole('option', { name: 'tag-01' })).toHaveAttribute('aria-selected', 'true')

  await advance(150)
  expect(api.clientTags).toHaveBeenCalledWith(undefined, 'tag-', expect.any(AbortSignal))
  expect(screen.getByRole('option', { name: 'tag-01' })).toHaveAttribute('aria-selected', 'true')
  fireEvent.keyDown(input, { key: 'ArrowDown' })
  expect(screen.getByRole('option', { name: 'tag-02' })).toHaveAttribute('aria-selected', 'true')

  pending.resolve(['tag-00', 'tag-01', 'tag-02', 'tag-03'])
  await advance()
  expect(screen.getByRole('option', { name: 'tag-02' })).toHaveAttribute('aria-selected', 'true')
  fireEvent.keyDown(input, { key: 'Enter' })
  expect(input).toHaveValue('tag-02')
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
})

it('刷新移除选中项后，Enter 不会误选原位置的另一个标签', async () => {
  client.setQueryData(['client-tags', undefined, ''], ['tag-01', 'tag-02'])
  const pending = deferred()
  vi.spyOn(api, 'clientTags').mockReturnValue(pending.promise)
  render(<Input />)
  const input = screen.getByRole('combobox')
  fireEvent.focus(input)
  fireEvent.change(input, { target: { value: 'tag-' } })
  fireEvent.keyDown(input, { key: 'ArrowDown' })
  await advance(150)
  pending.resolve(['tag-02'])
  await advance()
  expect(screen.getByRole('option', { name: 'tag-02' })).toHaveAttribute('aria-selected', 'false')
  fireEvent.keyDown(input, { key: 'Enter' })
  expect(input).toHaveValue('tag-')
})

it('更换标签名称时不沿用上一个标签的值建议', async () => {
  client.setQueryData(['client-tags', 'env', ''], ['production'])
  vi.spyOn(api, 'clientTags').mockReturnValue(new Promise(() => {}))
  const { rerender } = render(<Input tag="env" />)
  const input = screen.getByRole('combobox')
  fireEvent.focus(input)
  expect(screen.getByRole('option', { name: 'production' })).toBeInTheDocument()
  rerender(<Input tag="region" />)
  expect(screen.queryByRole('option', { name: 'production' })).not.toBeInTheDocument()
})
