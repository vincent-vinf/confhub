import { useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { ChevronLeft, ChevronRight, Monitor, RefreshCw, Search } from 'lucide-react'
import { api, configUrl } from '../lib/api'
import type { ClientFilter } from '../lib/types'
import { Select } from '../components/select'
import { TagInput } from '../components/tag-input'
import { formatDate } from '../lib/config'
import { Badge, Button, Empty, ErrorNotice, Loading } from '../components/ui'

const empty: ClientFilter = {
  search: '',
  namespace: '',
  group: '',
  name: '',
  status: '',
  tag: '',
  tag_value: '',
}

export function ClientsPage() {
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [draft, setDraft] = useState<ClientFilter>(empty)
  const [filter, setFilter] = useState<ClientFilter>(empty)
  const [cursors, setCursors] = useState([''])
  const after = cursors[cursors.length - 1]
  const query = useQuery({
    queryKey: ['clients', filter, after],
    queryFn: ({ signal }) => api.clients(filter, after, signal),
    staleTime: 0,
    refetchInterval: autoRefresh ? 5000 : false,
    refetchOnWindowFocus: autoRefresh,
    refetchOnReconnect: autoRefresh,
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    const next = {
      ...draft,
      search: draft.search.trim(),
      namespace: draft.namespace.trim(),
      group: draft.group.trim(),
      name: draft.name.trim(),
      tag_value: draft.tag ? draft.tag_value : '',
    }
    if (cursors.length === 1 && JSON.stringify(next) === JSON.stringify(filter)) {
      void query.refetch()
    }
    setFilter(next)
    setCursors([''])
  }
  const filtered = Object.values(filter).some(Boolean)
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>在线客户端</h1>
        </div>
        <div className="client-refresh-actions">
          <Button
            variant="ghost"
            role="switch"
            aria-checked={autoRefresh}
            className="client-auto-refresh"
            onClick={() => {
              if (!autoRefresh) void query.refetch()
              setAutoRefresh(!autoRefresh)
            }}
          >
            <span className="client-refresh-track" aria-hidden="true" />
            自动刷新
          </Button>
          <Button onClick={() => query.refetch()} busy={query.isFetching}>
            <RefreshCw size={16} aria-hidden="true" />
            刷新
          </Button>
        </div>
      </div>
      <section className="panel">
        <form className="client-filters" onSubmit={submit} aria-label="筛选在线客户端">
          <label>
            客户端搜索
            <input
              maxLength={512}
              placeholder="主机名、IP、连接或实例 ID"
              value={draft.search}
              onChange={(e) => setDraft({ ...draft, search: e.target.value })}
            />
          </label>
          <label>
            Namespace
            <input
              maxLength={255}
              placeholder="全部 namespace"
              value={draft.namespace}
              onChange={(e) => setDraft({ ...draft, namespace: e.target.value })}
            />
          </label>
          <label>
            Group
            <input
              maxLength={255}
              placeholder="全部分组"
              value={draft.group}
              onChange={(e) => setDraft({ ...draft, group: e.target.value })}
            />
          </label>
          <label>
            配置名称
            <input
              maxLength={255}
              placeholder="配置名称关键字"
              value={draft.name}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            />
          </label>
          <label>
            版本状态
            <Select
              aria-label="筛选版本状态"
              value={draft.status}
              onValueChange={(status) => setDraft({ ...draft, status })}
            >
              <option value="">全部状态</option>
              <option value="main">全量</option>
              <option value="beta">beta</option>
              <option value="waiting">等待发送</option>
              <option value="deleted">配置已删除或不存在</option>
              <option value="unsubscribed">暂无订阅</option>
            </Select>
          </label>
          <label>
            标签名称
            <TagInput
              label="筛选标签名称"
              value={draft.tag}
              placeholder="全部标签"
              onChange={(tag) => setDraft({ ...draft, tag, tag_value: '' })}
            />
          </label>
          <label>
            标签值
            <TagInput
              key={draft.tag}
              disabled={!draft.tag}
              label="筛选标签值"
              tag={draft.tag}
              value={draft.tag_value}
              placeholder="留空匹配任意值"
              onChange={(tag_value) => setDraft({ ...draft, tag_value })}
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
              }}
            >
              重置筛选
            </Button>
          </div>
        </form>
      </section>
      <ErrorNotice error={query.error} onRetry={() => query.refetch()} />
      <section className="panel">
        <div className="panel-toolbar">
          <div className="panel-title">
            <Monitor size={18} aria-hidden="true" />
            连接列表 <Badge>第 {cursors.length} 页</Badge>
          </div>
          <span className="section-help">
            {query.dataUpdatedAt > 0
              ? `最近读取 ${new Date(query.dataUpdatedAt).toLocaleTimeString('zh-CN', { hour12: false })}`
              : '正在读取…'}
          </span>
        </div>
        {query.isPending ? (
          <Loading label="正在读取在线客户端…" />
        ) : query.data?.clients.length ? (
          <div className="client-list">
            {query.data.clients.map((client) => (
              <article key={client.id} className="client-card" data-testid="client-card">
                <div className="client-heading">
                  <h2>{client.tags['sys.hostname'] || client.source_address || '未提供主机名'}</h2>
                  <Badge tone="success">连接中</Badge>
                </div>
                <dl className="client-metadata">
                  <div>
                    <dt>连接 ID</dt>
                    <dd className="mono">{client.id}</dd>
                  </div>
                  <div>
                    <dt>连接实例</dt>
                    <dd className="mono">{client.instance_id}</dd>
                  </div>
                  <div>
                    <dt>远端地址</dt>
                    <dd className="mono">{client.source_address}</dd>
                  </div>
                  <div>
                    <dt>连接时间</dt>
                    <dd>{formatDate(client.connected_at)}</dd>
                  </div>
                  <div>
                    <dt>快照时间</dt>
                    <dd>{formatDate(client.refreshed_at)}</dd>
                  </div>
                </dl>
                <h3>客户端标签</h3>
                <dl className="client-tags">
                  {Object.entries(client.tags)
                    .sort(([a], [b]) => a.localeCompare(b))
                    .map(([tag, value]) => (
                      <div key={tag}>
                        <dt>{tag}</dt>
                        <dd>{value === '' ? '空字符串' : value}</dd>
                      </div>
                    ))}
                </dl>
                {!Object.keys(client.tags).length && <p className="section-help">暂无标签</p>}
                <h3>订阅与已发送版本</h3>
                <ul className="client-subscriptions">
                  {client.subscriptions.map((sub) => (
                    <li key={JSON.stringify(sub.key)}>
                      <div>
                        <Link to={configUrl(sub.key)}>{sub.key.name}</Link>
                        <small>
                          {sub.key.namespace} / {sub.key.group}
                        </small>
                      </div>
                      <div className="client-version">
                        <Badge tone={sub.deleted ? 'warning' : 'neutral'}>
                          {!sub.sent
                            ? '等待发送'
                            : sub.deleted
                              ? '配置已删除或不存在'
                              : sub.beta
                                ? 'beta'
                                : `v${sub.version}`}
                        </Badge>
                        {sub.sent && !sub.deleted && (
                          <small>{sub.rule_id ? `规则 ${sub.rule_id}` : '全量'}</small>
                        )}
                        {sub.id && <small>配置 ID {sub.id}</small>}
                      </div>
                    </li>
                  ))}
                </ul>
                {!client.subscriptions.length && <p className="section-help">暂无订阅</p>}
              </article>
            ))}
          </div>
        ) : !query.error ? (
          <Empty
            title={
              after
                ? '这一页已无在线连接'
                : filtered
                  ? '没有符合条件的在线客户端'
                  : '暂无在线客户端'
            }
            action={
              after ? <Button onClick={() => setCursors([''])}>返回第一页</Button> : undefined
            }
          />
        ) : null}
        <div className="table-footer">
          <span className="section-help">本页 {query.data?.clients.length ?? 0} 个连接</span>
          <div className="pagination">
            <Button
              aria-label="上一页客户端"
              disabled={cursors.length === 1 || query.isFetching}
              onClick={() => setCursors((items) => items.slice(0, -1))}
            >
              <ChevronLeft size={16} aria-hidden="true" />
              上一页
            </Button>
            <Button
              aria-label="下一页客户端"
              disabled={!query.data?.next_after || query.isFetching}
              onClick={() => setCursors((items) => [...items, query.data!.next_after!])}
            >
              下一页
              <ChevronRight size={16} aria-hidden="true" />
            </Button>
          </div>
        </div>
      </section>
    </>
  )
}
