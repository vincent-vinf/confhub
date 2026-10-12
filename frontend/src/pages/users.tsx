import { useId, useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, RefreshCw, Trash2, Users } from 'lucide-react'
import { api, ApiError } from '../lib/api'
import { bytes } from '../lib/config'
import type { Grant, User, UserInput } from '../lib/types'
import { Select } from '../components/select'
import { Badge, Button, Confirmation, Empty, ErrorNotice, Loading, Modal } from '../components/ui'
import { usePending, useToast } from '../components/providers'

export function UsersPage() {
  const [cursors, setCursors] = useState([''])
  const [editing, setEditing] = useState<User | 'new'>()
  const [action, setAction] = useState<{ user: User; kind: 'password' | 'delete' | 'toggle' }>()
  const query = useQuery({
    queryKey: ['users', cursors.at(-1)],
    queryFn: ({ signal }) => api.users(cursors.at(-1), signal),
  })
  return (
    <>
      <div className="page-heading">
        <h1>用户管理</h1>
        <Button variant="primary" onClick={() => setEditing('new')}>
          <Plus size={18} aria-hidden="true" />
          创建用户
        </Button>
      </div>
      <section className="panel">
        <div className="panel-toolbar">
          <div className="panel-title">
            <Users size={18} aria-hidden="true" />
            用户列表
          </div>
          <Button
            variant="ghost"
            className="icon-button"
            aria-label="刷新用户列表"
            busy={query.isFetching}
            onClick={() => query.refetch()}
          >
            <RefreshCw size={17} aria-hidden="true" />
          </Button>
        </div>
        <ErrorNotice error={query.error} onRetry={() => query.refetch()} />
        {query.isPending ? (
          <Loading />
        ) : query.data?.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>用户名</th>
                  <th>类型</th>
                  <th>状态</th>
                  <th>权限</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {query.data.map((user) => (
                  <tr key={user.id}>
                    <td className="mono">{user.username}</td>
                    <td>{user.system_admin ? '系统管理员' : '普通用户'}</td>
                    <td>
                      <Badge tone={user.enabled ? 'success' : 'neutral'}>
                        {user.enabled ? '启用' : '禁用'}
                      </Badge>
                    </td>
                    <td>
                      {user.system_admin ? (
                        '全部权限'
                      ) : (
                        <div className="user-permissions">
                          {user.grants.length === 0 && !user.clients_read && (
                            <span className="muted">未授权</span>
                          )}
                          {user.grants.map((g) => (
                            <Badge key={`${g.namespace}/${g.group}`}>
                              {g.namespace || '全部空间'} / {g.group || '全部分组'} ·{' '}
                              {g.access === 'write' ? '读写' : '只读'}
                            </Badge>
                          ))}
                          {user.clients_read && <Badge>客户端查看</Badge>}
                        </div>
                      )}
                    </td>
                    <td>
                      <div className="row-actions">
                        <Button variant="ghost" onClick={() => setEditing(user)}>
                          编辑权限
                        </Button>
                        <Button
                          variant="ghost"
                          onClick={() => setAction({ user, kind: 'password' })}
                        >
                          重置密码
                        </Button>
                        {user.username !== 'admin' && (
                          <>
                            <Button
                              variant="ghost"
                              onClick={() => setAction({ user, kind: 'toggle' })}
                            >
                              {user.enabled ? '禁用' : '启用'}
                            </Button>
                            <Button
                              variant="ghost"
                              className="destructive-text"
                              onClick={() => setAction({ user, kind: 'delete' })}
                            >
                              删除
                            </Button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          !query.error && <Empty title="没有用户" />
        )}
        <div className="table-footer">
          <span>第 {cursors.length} 页</span>
          <div className="pagination">
            <Button
              disabled={cursors.length === 1 || query.isFetching}
              onClick={() => setCursors((c) => c.slice(0, -1))}
            >
              上一页
            </Button>
            <Button
              disabled={query.data?.length !== 25 || query.isFetching}
              onClick={() => setCursors((c) => [...c, query.data!.at(-1)!.username])}
            >
              下一页
            </Button>
          </div>
        </div>
      </section>
      {editing && (
        <UserEditor
          user={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(undefined)}
        />
      )}
      {action && (
        <UserAction user={action.user} kind={action.kind} onClose={() => setAction(undefined)} />
      )}
    </>
  )
}
function userError(error: unknown) {
  if (error instanceof ApiError && error.status === 409)
    return '用户信息已变更，或用户名已存在。请关闭弹窗并刷新列表后重试。'
  return error
}
function UserEditor({ user, onClose }: { user?: User; onClose: () => void }) {
  const id = useId()
  const [username, setUsername] = useState(user?.username ?? '')
  const [password, setPassword] = useState('')
  const [input, setInput] = useState<UserInput>(
    user
      ? {
          enabled: user.enabled,
          system_admin: user.system_admin,
          clients_read: user.clients_read,
          grants: structuredClone(user.grants),
        }
      : { enabled: true, system_admin: false, clients_read: false, grants: [] },
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  const client = useQueryClient(),
    toast = useToast()
  usePending('正在保存用户权限', busy)
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    if (!user && (bytes(password) < 8 || bytes(password) > 72)) {
      setError('密码应为 8–72 字节。')
      document.getElementById(`${id}-password`)?.focus()
      return
    }
    setBusy(true)
    setError(undefined)
    try {
      if (user) await api.updateUser(user, input)
      else await api.createUser(username, password, input)
      await client.invalidateQueries({ queryKey: ['users'] })
      await client.invalidateQueries({ queryKey: ['me'] })
      toast(user ? '用户权限已更新。' : '用户已创建。')
      onClose()
    } catch (e) {
      setError(userError(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      title={user ? `编辑用户 · ${user.username}` : '创建用户'}
      onClose={onClose}
      busy={busy}
      wide
      footer={
        <>
          <Button disabled={busy} onClick={onClose}>
            取消
          </Button>
          <Button type="submit" form={id} variant="primary" busy={busy}>
            {user ? '保存权限' : '创建用户'}
          </Button>
        </>
      }
    >
      <form id={id} className="form-stack" onSubmit={submit}>
        {!user && (
          <div className="form-grid">
            <label>
              用户名
              <input
                required
                autoComplete="off"
                pattern="[a-zA-Z0-9][a-zA-Z0-9_.\-]{0,63}"
                maxLength={64}
                value={username}
                placeholder="字母、数字、下划线、点或连字符"
                onChange={(e) => setUsername(e.target.value)}
              />
            </label>
            <label>
              初始密码
              <input
                id={`${id}-password`}
                required
                type="password"
                autoComplete="new-password"
                value={password}
                placeholder="8–72 字节"
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
          </div>
        )}
        <div className="button-row">
          <label className="check-label">
            <input
              type="checkbox"
              checked={input.enabled}
              disabled={user?.username === 'admin'}
              onChange={(e) => setInput({ ...input, enabled: e.target.checked })}
            />
            启用账号
          </label>
          <label className="check-label">
            <input
              type="checkbox"
              checked={input.system_admin}
              disabled={user?.username === 'admin'}
              onChange={(e) => setInput({ ...input, system_admin: e.target.checked })}
            />
            系统管理员（全部权限）
          </label>
        </div>
        {!input.system_admin && (
          <>
            <label className="check-label">
              <input
                type="checkbox"
                checked={input.clients_read}
                onChange={(e) => setInput({ ...input, clients_read: e.target.checked })}
              />
              查看全部在线客户端和标签
            </label>
            <fieldset>
              <legend>配置权限</legend>
              <div className="grant-list">
                {input.grants.map((grant, index) => (
                  <GrantEditor
                    key={index}
                    grant={grant}
                    index={index}
                    onChange={(g) =>
                      setInput({
                        ...input,
                        grants: input.grants.map((old, i) => (i === index ? g : old)),
                      })
                    }
                    onDelete={() =>
                      setInput({ ...input, grants: input.grants.filter((_, i) => i !== index) })
                    }
                  />
                ))}
              </div>
              <Button
                disabled={input.grants.length >= 200}
                onClick={() =>
                  setInput({
                    ...input,
                    grants: [...input.grants, { namespace: '', group: '', access: 'read' }],
                  })
                }
              >
                <Plus size={16} aria-hidden="true" />
                添加授权
              </Button>
            </fieldset>
          </>
        )}
        <ErrorNotice error={error} />
      </form>
    </Modal>
  )
}
function GrantEditor({
  grant,
  index,
  onChange,
  onDelete,
}: {
  grant: Grant
  index: number
  onChange: (grant: Grant) => void
  onDelete: () => void
}) {
  const namespaces = useQuery({
    queryKey: ['namespaces'],
    queryFn: ({ signal }) => api.namespaces(signal),
  })
  const groups = useQuery({
    queryKey: ['groups', grant.namespace],
    queryFn: ({ signal }) => api.groups(grant.namespace, signal),
    enabled: !!grant.namespace,
  })
  return (
    <div className="grant-editor">
      <label>
        命名空间
        <Select
          aria-label={`授权 ${index + 1} 命名空间`}
          value={grant.namespace}
          onValueChange={(n) => onChange({ ...grant, namespace: n, group: '' })}
        >
          <option value="">全部命名空间</option>
          {namespaces.data?.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
          {grant.namespace && !namespaces.data?.includes(grant.namespace) && (
            <option>{grant.namespace}</option>
          )}
        </Select>
      </label>
      <label>
        分组
        <Select
          aria-label={`授权 ${index + 1} 分组`}
          value={grant.group}
          disabled={!grant.namespace}
          onValueChange={(g) => onChange({ ...grant, group: g })}
        >
          <option value="">全部分组</option>
          {groups.data?.map((g) => (
            <option key={g} value={g}>
              {g}
            </option>
          ))}
          {grant.group && !groups.data?.includes(grant.group) && <option>{grant.group}</option>}
        </Select>
      </label>
      <label>
        权限
        <Select
          aria-label={`授权 ${index + 1} 权限`}
          value={grant.access}
          onValueChange={(access) => onChange({ ...grant, access: access as Grant['access'] })}
        >
          <option value="read">只读</option>
          <option value="write">读写</option>
        </Select>
      </label>
      <Button
        variant="ghost"
        className="icon-button destructive-text"
        aria-label={`删除授权 ${index + 1}`}
        onClick={onDelete}
      >
        <Trash2 size={17} aria-hidden="true" />
      </Button>
      <ErrorNotice error={namespaces.error || groups.error} />
    </div>
  )
}
function UserAction({
  user,
  kind,
  onClose,
}: {
  user: User
  kind: 'password' | 'delete' | 'toggle'
  onClose: () => void
}) {
  const [password, setPassword] = useState(''),
    [checked, setChecked] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>()
  const client = useQueryClient(),
    toast = useToast(),
    id = useId()
  const title =
    kind === 'password'
      ? '重置密码'
      : kind === 'delete'
        ? '删除用户'
        : user.enabled
          ? '禁用用户'
          : '启用用户'
  usePending(`正在${title}`, busy)
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!checked || busy) return
    if (kind === 'password' && (bytes(password) < 8 || bytes(password) > 72)) {
      setError('密码应为 8–72 字节。')
      return
    }
    setBusy(true)
    setError(undefined)
    try {
      if (kind === 'password') await api.resetPassword(user, password)
      else if (kind === 'delete') await api.deleteUser(user)
      else
        await api.updateUser(user, {
          enabled: !user.enabled,
          system_admin: user.system_admin,
          clients_read: user.clients_read,
          grants: user.grants,
        })
      await client.invalidateQueries({ queryKey: ['users'] })
      await client.invalidateQueries({ queryKey: ['me'] })
      toast(`${title}成功。`)
      onClose()
    } catch (e) {
      setError(userError(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      title={`${title} · ${user.username}`}
      description={
        kind === 'delete'
          ? '用户和授权将永久删除。'
          : kind === 'password' || user.enabled
            ? '已有登录会话将失效。'
            : undefined
      }
      busy={busy}
      onClose={onClose}
      footer={
        <>
          <Button disabled={busy} onClick={onClose}>
            取消
          </Button>
          <Button
            form={id}
            type="submit"
            variant={kind === 'delete' ? 'danger' : 'primary'}
            disabled={!checked}
            busy={busy}
          >
            确认{title}
          </Button>
        </>
      }
    >
      <form id={id} className="form-stack" onSubmit={submit}>
        {kind === 'password' && (
          <label>
            新密码
            <input
              required
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="8–72 字节"
            />
          </label>
        )}
        <Confirmation checked={checked} onChange={setChecked} label="我已确认此账号及操作影响" />
        <ErrorNotice error={error} />
      </form>
    </Modal>
  )
}
