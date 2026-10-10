import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { ChevronLeft, ChevronRight, Monitor, RefreshCw } from 'lucide-react'
import { api, configUrl } from '../lib/api'
import { formatDate } from '../lib/config'
import { Badge, Button, Empty, ErrorNotice, Loading } from '../components/ui'

export function ClientsPage() {
  const [cursors, setCursors] = useState([''])
  const after = cursors[cursors.length - 1]
  const query = useQuery({
    queryKey: ['clients', after],
    queryFn: ({ signal }) => api.clients(after, signal),
    staleTime: 0,
    refetchInterval: 5000,
    refetchOnWindowFocus: true,
  })
  return (
    <>
      <div className="page-heading">
        <div>
          <div className="eyebrow">CLIENTS</div>
          <h1>在线客户端</h1>
          <p className="page-description">
            查看 WebSocket 连接、客户端标签和每个订阅已发送的版本。
          </p>
        </div>
        <Button onClick={() => query.refetch()} busy={query.isFetching}>
          <RefreshCw size={16} aria-hidden="true" />
          刷新
        </Button>
      </div>
      <div className="notice info">
        每 5 秒自动刷新。正常断连约 10 秒内从页面移除；实例异常退出约 20
        秒过期，再于下一次读取时移除。HTTP GET
        不计入在线连接。已发送版本表示服务器成功写入连接，客户端应用状态未确认。
      </div>
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
              : '正在读取连接快照'}
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
                {!Object.keys(client.tags).length && (
                  <p className="section-help">此连接未传入标签。</p>
                )}
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
                          <small>
                            修订 {sub.revision}
                            {sub.rule_id ? ` · 规则 ${sub.rule_id}` : ' · 全量'}
                          </small>
                        )}
                        {sub.id && <small>配置 ID {sub.id}</small>}
                      </div>
                    </li>
                  ))}
                </ul>
                {!client.subscriptions.length && (
                  <p className="section-help">已连接，尚未订阅配置。</p>
                )}
              </article>
            ))}
          </div>
        ) : !query.error ? (
          <Empty
            title={after ? '这一页已无在线连接' : '暂无在线客户端'}
            description={
              after
                ? '连接列表会变化，可以返回第一页重新查看。'
                : '客户端建立 WebSocket 连接后将显示在这里。'
            }
            action={
              after ? <Button onClick={() => setCursors([''])}>返回第一页</Button> : undefined
            }
          />
        ) : null}
        <div className="table-footer">
          <span className="section-help">本页 {query.data?.clients.length ?? 0} 个连接 · 只读</span>
          <div className="pagination">
            <Button
              aria-label="上一页客户端"
              disabled={cursors.length === 1 || query.isPending}
              onClick={() => setCursors((items) => items.slice(0, -1))}
            >
              <ChevronLeft size={16} aria-hidden="true" />
              上一页
            </Button>
            <Button
              aria-label="下一页客户端"
              disabled={!query.data?.next_after || query.isPending}
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
