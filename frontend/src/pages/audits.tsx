import { useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, RefreshCw, ScrollText, Search } from 'lucide-react'
import { api } from '../lib/api'
import { formatDate } from '../lib/config'
import type { Audit, AuditFilter } from '../lib/types'
import { Select } from '../components/select'
import { Badge, Button, Empty, ErrorNotice, Loading, Modal } from '../components/ui'

export const auditActions: Record<string, string> = {
  'auth.login': '登录',
  'auth.logout': '退出登录',
  'auth.password': '修改本人密码',
  'namespace.create': '创建命名空间',
  'namespace.delete': '删除命名空间',
  'group.create': '创建分组',
  'group.delete': '删除分组',
  'config.read': '读取配置',
  'config.version.read': '读取历史版本',
  'config.simulate': '匹配模拟',
  'config.publish': '全量发布',
  'config.beta.save': '编辑 beta',
  'config.delete': '删除配置',
  'config.rules': '修改灰度规则',
  'config.rollback': '全量回退',
  'config.promote': '灰度转全量',
  'config.batch-delete': '批量删除配置',
  'config.batch-move': '批量移动配置',
  'user.create': '创建用户',
  'user.update': '修改用户及权限',
  'user.delete': '删除用户',
  'user.password': '重置用户密码',
}
const empty: AuditFilter = { username: '', ip: '', action: '', result: '', from: '', to: '' }
const resultName = { success: '成功', failure: '失败', pending: '结果未确认' }

export function AuditsPage() {
  const [draft, setDraft] = useState<AuditFilter>(empty)
  const [filter, setFilter] = useState<AuditFilter>(empty)
  const [cursors, setCursors] = useState([''])
  const [viewing, setViewing] = useState<Audit>()
  const [error, setError] = useState<string>()
  const query = useQuery({
    queryKey: ['audits', filter, cursors.at(-1)],
    queryFn: ({ signal }) => api.audits(filter, cursors.at(-1), signal),
    staleTime: 0,
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (draft.from && draft.to && new Date(draft.from) >= new Date(draft.to)) {
      setError('结束时间应晚于开始时间。')
      return
    }
    setError(undefined)
    const nextFilter = {
      ...draft,
      username: draft.username.trim(),
      ip: draft.ip.trim(),
      from: draft.from ? new Date(draft.from).toISOString() : '',
      to: draft.to ? new Date(draft.to).toISOString() : '',
    }
    if (cursors.length === 1 && JSON.stringify(nextFilter) === JSON.stringify(filter)) {
      void query.refetch()
    }
    setFilter(nextFilter)
    setCursors([''])
  }
  return (
    <>
      <div className="page-heading">
        <h1>操作审计</h1>
        <Button onClick={() => query.refetch()} busy={query.isFetching}>
          <RefreshCw size={16} aria-hidden="true" />
          刷新
        </Button>
      </div>
      <section className="panel">
        <form className="audit-filters" onSubmit={submit}>
          <label>
            用户
            <input
              maxLength={64}
              placeholder="用户名"
              value={draft.username}
              onChange={(e) => setDraft({ ...draft, username: e.target.value })}
            />
          </label>
          <label>
            来源 IP
            <input
              placeholder="IPv4 或 IPv6"
              value={draft.ip}
              onChange={(e) => setDraft({ ...draft, ip: e.target.value })}
            />
          </label>
          <label>
            操作
            <Select
              aria-label="筛选操作"
              value={draft.action}
              onValueChange={(action) => setDraft({ ...draft, action })}
            >
              <option value="">全部操作</option>
              {Object.entries(auditActions).map(([key, name]) => (
                <option key={key} value={key}>
                  {name}
                </option>
              ))}
            </Select>
          </label>
          <label>
            结果
            <Select
              aria-label="筛选结果"
              value={draft.result}
              onValueChange={(result) => setDraft({ ...draft, result })}
            >
              <option value="">全部结果</option>
              {Object.entries(resultName).map(([key, name]) => (
                <option key={key} value={key}>
                  {name}
                </option>
              ))}
            </Select>
          </label>
          <label>
            开始时间
            <input
              type="datetime-local"
              value={draft.from}
              onChange={(e) => setDraft({ ...draft, from: e.target.value })}
            />
          </label>
          <label>
            结束时间
            <input
              type="datetime-local"
              value={draft.to}
              onChange={(e) => setDraft({ ...draft, to: e.target.value })}
            />
          </label>
          <div className="button-row">
            <Button type="submit" variant="primary" busy={query.isFetching}>
              <Search size={16} aria-hidden="true" />
              查询
            </Button>
            <Button
              onClick={() => {
                setDraft(empty)
                setFilter(empty)
                setCursors([''])
                setError(undefined)
              }}
            >
              重置筛选
            </Button>
          </div>
        </form>
        <ErrorNotice
          error={error || query.error}
          onRetry={query.error ? () => query.refetch() : undefined}
        />
      </section>
      <section className="panel">
        <div className="panel-toolbar">
          <div className="panel-title">
            <ScrollText size={18} aria-hidden="true" />
            审计记录 <Badge>第 {cursors.length} 页</Badge>
          </div>
        </div>
        {query.isPending ? (
          <Loading />
        ) : query.data?.audits.length ? (
          <div className="table-scroll">
            <table className="audit-table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>用户</th>
                  <th>来源 IP</th>
                  <th>操作</th>
                  <th>对象</th>
                  <th>结果</th>
                  <th>详情</th>
                </tr>
              </thead>
              <tbody>
                {query.data.audits.map((a) => (
                  <tr key={a.id}>
                    <td className="date-cell">
                      <time dateTime={a.created_at}>{formatDate(a.created_at)}</time>
                    </td>
                    <td>
                      {a.username ||
                        (a.attempted_username ? `${a.attempted_username}（尝试登录）` : '未认证')}
                    </td>
                    <td className="mono">{a.ip || '—'}</td>
                    <td>{auditActions[a.action] || a.action}</td>
                    <td className="audit-subject">{a.subject}</td>
                    <td>
                      <Badge
                        tone={
                          a.result === 'success'
                            ? 'success'
                            : a.result === 'failure'
                              ? 'danger'
                              : 'warning'
                        }
                      >
                        {resultName[a.result]}
                      </Badge>
                    </td>
                    <td>
                      <Button
                        variant="ghost"
                        aria-label={`查看审计 ${a.id}`}
                        onClick={() => setViewing(a)}
                      >
                        详情
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          !query.error && <Empty title="没有符合条件的审计记录" />
        )}
        <div className="table-footer">
          <span>本页 {query.data?.audits.length ?? 0} 条</span>
          <div className="pagination">
            <Button
              aria-label="上一页审计"
              disabled={cursors.length === 1 || query.isFetching}
              onClick={() => setCursors((c) => c.slice(0, -1))}
            >
              <ChevronLeft size={16} aria-hidden="true" />
              上一页
            </Button>
            <Button
              aria-label="下一页审计"
              disabled={!query.data?.next_before || query.isFetching}
              onClick={() => setCursors((c) => [...c, query.data!.next_before!])}
            >
              下一页
              <ChevronRight size={16} aria-hidden="true" />
            </Button>
          </div>
        </div>
      </section>
      {viewing && (
        <Modal
          open
          title="审计详情"
          onClose={() => setViewing(undefined)}
          wide
          footer={<Button onClick={() => setViewing(undefined)}>关闭</Button>}
        >
          <dl className="client-metadata audit-metadata">
            <div>
              <dt>时间</dt>
              <dd>{formatDate(viewing.created_at)}</dd>
            </div>
            <div>
              <dt>操作用户</dt>
              <dd>{viewing.username || '未认证'}</dd>
            </div>
            {viewing.attempted_username && viewing.attempted_username !== viewing.username && (
              <div>
                <dt>尝试登录用户名</dt>
                <dd>{viewing.attempted_username}</dd>
              </div>
            )}
            <div>
              <dt>用户 ID</dt>
              <dd className="mono">{viewing.user_id || '—'}</dd>
            </div>
            <div>
              <dt>来源 IP</dt>
              <dd className="mono">{viewing.ip || '—'}</dd>
            </div>
            <div>
              <dt>操作</dt>
              <dd>{auditActions[viewing.action] || viewing.action}</dd>
            </div>
            <div>
              <dt>对象</dt>
              <dd>{viewing.subject}</dd>
            </div>
            <div>
              <dt>结果</dt>
              <dd>
                {resultName[viewing.result]}
                {viewing.status ? ` · HTTP ${viewing.status}` : ''}
              </dd>
            </div>
            <div>
              <dt>耗时</dt>
              <dd>{viewing.duration_ms} ms</dd>
            </div>
            <div>
              <dt>请求 ID</dt>
              <dd className="mono">{viewing.request_id}</dd>
            </div>
          </dl>
          {Object.keys(viewing.details).length > 0 && (
            <>
              <h3>操作信息</h3>
              <pre className="audit-details">{JSON.stringify(viewing.details, null, 2)}</pre>
            </>
          )}
        </Modal>
      )}
    </>
  )
}
