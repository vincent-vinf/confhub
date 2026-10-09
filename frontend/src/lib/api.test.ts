import { afterEach, expect, it, vi } from 'vitest'
import { api, configPath, groupIndex, allVersions, request } from './api'

afterEach(() => vi.unstubAllGlobals())
it('配置路径编码中文及特殊字符', () => {
  expect(configPath({ namespace: '中文', group: 'a b', name: 'a?#.yaml' })).toBe(
    '/api/admin/namespaces/%E4%B8%AD%E6%96%87/groups/a%20b/configs/a%3F%23.yaml',
  )
})
it('登录错误和当前密码错误不触发会话过期，空 401 会触发', async () => {
  const expired = vi.fn()
  window.addEventListener('confhub:unauthorized', expired)
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockImplementation(() =>
        Promise.resolve(new Response('{"error":"invalid credentials"}', { status: 401 })),
      ),
  )
  await expect(api.login('admin', 'bad')).rejects.toThrow('用户名或密码不正确')
  await expect(api.password('bad', 'new')).rejects.toThrow('当前密码不正确')
  expect(expired).not.toHaveBeenCalled()
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 401 })))
  await expect(api.namespaces()).rejects.toThrow('登录已过期')
  expect(expired).toHaveBeenCalledOnce()
  window.removeEventListener('confhub:unauthorized', expired)
})
it('网络错误与取消区别处理，204 不解析 JSON', async () => {
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
  await expect(request('/api/test')).rejects.toThrow('无法连接服务')
  const abort = new DOMException('cancel', 'AbortError')
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(abort))
  await expect(request('/api/test')).rejects.toBe(abort)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })))
  expect(await request('/api/test')).toBeUndefined()
})
it('搜索索引和历史元数据遍历服务端游标', async () => {
  const first = Array.from({ length: 200 }, (_, i) => ({ key: { name: `item-${i}` } }))
  const list = vi
    .spyOn(api, 'list')
    .mockResolvedValueOnce(first as never)
    .mockResolvedValueOnce([])
  expect(await groupIndex('ns', 'group')).toHaveLength(200)
  expect(list).toHaveBeenLastCalledWith('ns', 'group', 'item-199', 200, undefined)
  const history = vi
    .spyOn(api, 'history')
    .mockResolvedValueOnce({ versions: [], next_before: 42 })
    .mockResolvedValueOnce({ versions: [] })
  await allVersions({ namespace: 'ns', group: 'g', name: 'a' })
  expect(history).toHaveBeenLastCalledWith(
    { namespace: 'ns', group: 'g', name: 'a' },
    42,
    100,
    undefined,
  )
})
