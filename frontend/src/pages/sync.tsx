import { useQuery } from '@tanstack/react-query'
import { CloudDownload, RefreshCw } from 'lucide-react'
import { api } from '../lib/api'
import { formatDate } from '../lib/config'
import { Badge, Button, ErrorNotice, Loading } from '../components/ui'

const errors: Record<string, string> = {
  upstream_unavailable: '云端连接中断，正在重连。',
  local_storage_unavailable: '本地数据库暂不可用。',
  source_changed_rebuild_required: '云端来源已变化，需要重建边缘镜像。',
}
const date = (value?: string) => (!value || value.startsWith('0001-') ? '—' : formatDate(value))

export function SyncPage() {
  const query = useQuery({
    queryKey: ['edge-status'],
    queryFn: ({ signal }) => api.edgeStatus(signal),
    refetchInterval: 5000,
    refetchOnWindowFocus: true,
  })
  const mirror = query.data?.mirror
  const connection = query.data?.connection
  return (
    <>
      <div className="page-heading">
        <h1>同步状态</h1>
        <Button onClick={() => query.refetch()} busy={query.isFetching}>
          <RefreshCw size={16} aria-hidden="true" />
          刷新
        </Button>
      </div>
      <ErrorNotice error={query.error} onRetry={() => query.refetch()} />
      {query.isPending ? (
        <Loading />
      ) : (
        mirror &&
        connection && (
          <>
            <section className="panel">
              <div className="panel-toolbar">
                <div className="panel-title">
                  <CloudDownload size={18} aria-hidden="true" />
                  本地镜像
                </div>
                <Badge tone={mirror.ready ? 'success' : 'warning'}>
                  {mirror.ready ? '可提供服务' : '等待首次同步'}
                </Badge>
              </div>
              <dl className="sync-metadata">
                <div>
                  <dt>云端来源</dt>
                  <dd className="mono">{mirror.source || '—'}</dd>
                </div>
                <div>
                  <dt>已应用水位</dt>
                  <dd className="mono">{mirror.sequence}</dd>
                </div>
                <div>
                  <dt>最近应用时间</dt>
                  <dd>{date(mirror.last_success)}</dd>
                </div>
                <div>
                  <dt>同步任务</dt>
                  <dd>{connection.active_sync_worker ? '当前实例同步' : '备用实例'}</dd>
                </div>
                <div>
                  <dt>当前实例云端连接</dt>
                  <dd>
                    {!connection.active_sync_worker
                      ? '备用实例无需连接'
                      : connection.connected
                        ? '已连接'
                        : '未连接'}
                  </dd>
                </div>
                <div>
                  <dt>当前实例观察水位</dt>
                  <dd className="mono">
                    {connection.active_sync_worker &&
                    connection.observed_cloud_cursor.source === mirror.source
                      ? connection.observed_cloud_cursor.sequence
                      : '—'}
                  </dd>
                </div>
                <div>
                  <dt>同步实例 ID</dt>
                  <dd className="mono">{mirror.sync_owner || '—'}</dd>
                </div>
                <div>
                  <dt>租约到期</dt>
                  <dd>{date(mirror.lease_expires)}</dd>
                </div>
              </dl>
            </section>
            {connection.error && (
              <ErrorNotice error={errors[connection.error] || '同步暂时异常，正在重试。'} />
            )}
          </>
        )
      )}
    </>
  )
}
