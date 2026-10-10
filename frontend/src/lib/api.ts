import type {
  Baseline,
  ClientPage,
  ConfigKey,
  ConfigState,
  ConfigSummary,
  Edit,
  Effective,
  GrayRule,
  Mutation,
  Version,
  VersionPage,
} from './types'

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}
const friendlyError = (status: number, serverMessage?: string) => {
  if (status === 401) return '登录已过期，请重新登录。你的编辑内容会保留。'
  if (status === 403) return '请求来源校验失败，请使用与服务相同的访问地址。'
  if (status === 409) return '配置已被其他人修改，请读取最新版本后重新比较。'
  if (serverMessage?.includes('namespace or group is not empty'))
    return '这里仍有分组或配置，请先移除其中的内容。'
  if (status === 404) return '内容不存在，可能已被删除。'
  if (status >= 500) return '服务暂时不可用，请稍后重试。'
  return serverMessage || '操作失败，请检查填写的内容。'
}
export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      ...options,
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', ...options.headers },
    })
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    throw new ApiError(0, '无法连接服务，请检查网络后重试。')
  }
  if (!response.ok) {
    let message: string | undefined
    try {
      message = (await response.json()).error
    } catch {
      /* Authentication responses may have no body. */
    }
    const wrongCurrentPassword =
      path.endsWith('/password') && response.status === 401 && message === 'invalid credentials'
    if (response.status === 401 && !path.endsWith('/login') && !wrongCurrentPassword)
      window.dispatchEvent(new Event('confhub:unauthorized'))
    throw new ApiError(
      response.status,
      wrongCurrentPassword
        ? '当前密码不正确。'
        : path.endsWith('/login') && response.status === 401
          ? '用户名或密码不正确。'
          : friendlyError(response.status, message),
    )
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
const body = (method: string, value: unknown): RequestInit => ({
  method,
  body: JSON.stringify(value),
})
const base = '/api/admin'
const part = encodeURIComponent
export const groupPath = (namespace: string, group: string) =>
  `${base}/namespaces/${part(namespace)}/groups/${part(group)}`
export const configPath = (key: ConfigKey) =>
  `${groupPath(key.namespace, key.group)}/configs/${part(key.name)}`
export const configUrl = (key: ConfigKey) =>
  `/configs/${part(key.namespace)}/${part(key.group)}/${part(key.name)}`
export const listUrl = (namespace = 'public', group = 'DEFAULT_GROUP') =>
  `/configs?${new URLSearchParams({ namespace, group })}`
export const configQueryKey = (key: ConfigKey) =>
  ['config', key.namespace, key.group, key.name] as const
export const api = {
  clients: (after = '', signal?: AbortSignal) =>
    request<ClientPage>(`${base}/clients?${new URLSearchParams({ after, limit: '25' })}`, {
      signal,
    }),
  clientTags: (tag = '', prefix = '', signal?: AbortSignal) =>
    request<string[]>(`${base}/client-tags?${new URLSearchParams({ tag, prefix })}`, { signal }),
  login: (username: string, password: string) =>
    request<{ username: string; expires_at: string }>(
      `${base}/login`,
      body('POST', { username, password }),
    ),
  logout: () => request<void>(`${base}/logout`, body('POST', {})),
  password: (old_password: string, new_password: string) =>
    request(`${base}/password`, body('POST', { old_password, new_password })),
  namespaces: (signal?: AbortSignal) => request<string[]>(`${base}/namespaces`, { signal }),
  createNamespace: (name: string) => request(`${base}/namespaces`, body('POST', { name })),
  deleteNamespace: (namespace: string) =>
    request(`${base}/namespaces/${part(namespace)}`, body('DELETE', { confirmed: true })),
  groups: (namespace: string, signal?: AbortSignal) =>
    request<string[]>(`${base}/namespaces/${part(namespace)}/groups`, { signal }),
  createGroup: (namespace: string, name: string) =>
    request(`${base}/namespaces/${part(namespace)}/groups`, body('POST', { name })),
  deleteGroup: (namespace: string, group: string) =>
    request(groupPath(namespace, group), body('DELETE', { confirmed: true })),
  list: (namespace: string, group: string, after = '', limit = 25, signal?: AbortSignal) =>
    request<ConfigSummary[]>(
      `${groupPath(namespace, group)}/configs?${new URLSearchParams({ after, limit: String(limit) })}`,
      { signal },
    ),
  state: (key: ConfigKey, signal?: AbortSignal) =>
    request<ConfigState>(configPath(key), { signal }),
  save: (key: ConfigKey, edit: Edit) => request<Mutation>(configPath(key), body('PUT', edit)),
  remove: (key: ConfigKey, baseline: Baseline) =>
    request<Mutation>(configPath(key), body('DELETE', { ...baseline, confirmed: true })),
  history: (key: ConfigKey, before = 0, limit = 100, signal?: AbortSignal) =>
    request<VersionPage>(
      `${configPath(key)}/versions?${new URLSearchParams({ before: String(before), limit: String(limit) })}`,
      { signal },
    ),
  version: (key: ConfigKey, version: number, signal?: AbortSignal) =>
    request<Version>(`${configPath(key)}/versions/${version}`, { signal }),
  rules: (key: ConfigKey, baseline: Baseline, rules: GrayRule[]) =>
    request<Mutation>(
      `${configPath(key)}/rules`,
      body('PUT', {
        ...baseline,
        rules: rules.map(({ id, name, enabled, conditions }) => ({
          id,
          name,
          enabled,
          conditions,
        })),
        confirmed: true,
      }),
    ),
  rollback: (key: ConfigKey, baseline: Baseline, source_version: number) =>
    request<Mutation>(
      `${configPath(key)}/rollback`,
      body('POST', { ...baseline, source_version, confirmed: true }),
    ),
  promote: (key: ConfigKey, baseline: Baseline, rule_id: string) =>
    request<Mutation>(
      `${configPath(key)}/promote`,
      body('POST', { ...baseline, rule_id, confirmed: true }),
    ),
  simulate: (key: ConfigKey, tags: Record<string, string>) =>
    request<Effective>(`${configPath(key)}/simulate`, body('POST', { tags })),
  ready: async (signal?: AbortSignal) => {
    try {
      const r = await fetch('/health/ready', { signal })
      return r.ok
    } catch (error) {
      if (signal?.aborted) throw error
      return false
    }
  },
}
export async function groupIndex(namespace: string, group: string, signal?: AbortSignal) {
  const result: ConfigSummary[] = []
  let after = ''
  for (;;) {
    const page = await api.list(namespace, group, after, 200, signal)
    result.push(...page)
    if (page.length < 200) return result
    after = page[page.length - 1].key.name
  }
}
export async function allVersions(key: ConfigKey, signal?: AbortSignal) {
  const result: Version[] = []
  let before = 0
  for (;;) {
    const page = await api.history(key, before, 100, signal)
    result.push(...page.versions)
    if (!page.next_before) return result
    before = page.next_before
  }
}
export const messageOf = (error: unknown) =>
  error instanceof Error ? error.message : '操作失败，请重试。'
