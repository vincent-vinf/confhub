import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowRight, GitCompareArrows, History, RotateCcw } from 'lucide-react'
import { api, allVersions } from '../lib/api'
import { actionNames, formatDate } from '../lib/config'
import type { ConfigKey, ConfigState, Version } from '../lib/types'
import { CodeEditor, ConfigDiff } from '../components/editor'
import { ChangeDialog, type ChangeAction } from '../components/change-dialog'
import { Badge, Button, Empty, ErrorNotice, Loading, Modal } from '../components/ui'

export function HistoryPanel({
  configKey,
  state,
  onStateChanged,
  onRebase,
}: {
  configKey: ConfigKey
  state: ConfigState
  onStateChanged: (state: ConfigState) => void
  onRebase: (state: ConfigState) => void
}) {
  const versions = useQuery({
    queryKey: ['versions', configKey],
    queryFn: ({ signal }) => allVersions(configKey, signal),
  })
  const [page, setPage] = useState(0)
  const [viewing, setViewing] = useState<number>()
  const [compare, setCompare] = useState<{ before: number; after: number }>()
  const [action, setAction] = useState<ChangeAction>()
  const [loading, setLoading] = useState<number>()
  const [error, setError] = useState<unknown>()
  const [targetRule, setTargetRule] = useState('global')
  const viewed = useQuery({
    queryKey: ['version', configKey, viewing],
    queryFn: ({ signal }) => api.version(configKey, viewing!, signal),
    enabled: !!viewing,
  })
  async function rollback(version: number) {
    setLoading(version)
    setError(undefined)
    try {
      const source = await api.version(configKey, version)
      setAction({
        kind: 'rollback',
        source,
        ruleId: targetRule === 'global' ? undefined : targetRule,
      })
    } catch (e) {
      setError(e)
    } finally {
      setLoading(undefined)
    }
  }
  const rows = versions.data?.slice(page * 20, (page + 1) * 20)
  return (
    <>
      <section className="panel">
        <div className="panel-toolbar history-toolbar">
          <div>
            <div className="panel-title">
              <History size={18} aria-hidden="true" />
              版本历史 <Badge>{versions.data?.length ?? '…'}</Badge>
            </div>
            <p className="section-help">历史不可修改；回退会复制内容生成新版本。</p>
          </div>
          <label>
            回退目标
            <select
              aria-label="回退目标"
              value={targetRule}
              onChange={(e) => setTargetRule(e.target.value)}
            >
              <option value="global">全量配置</option>
              {state.rules.map((rule) => (
                <option key={rule.id} value={rule.id}>
                  {rule.name || rule.id}
                </option>
              ))}
            </select>
          </label>
        </div>
        <ErrorNotice
          error={error || versions.error}
          onRetry={versions.error ? () => versions.refetch() : undefined}
        />
        {versions.isPending ? (
          <Loading label="正在读取版本历史…" />
        ) : rows?.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>版本</th>
                  <th>说明与操作</th>
                  <th>发布引用</th>
                  <th>创建时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((version) => (
                  <tr key={version.number}>
                    <td>
                      <button
                        className="version-link mono"
                        onClick={() => setViewing(version.number)}
                      >
                        v{version.number}
                      </button>
                    </td>
                    <td>
                      <div className="history-description">
                        {version.description || '未填写说明'}
                      </div>
                      <small className="muted">
                        {actionNames[version.action] ?? version.action}
                        {version.source_version ? ` · 来源 v${version.source_version}` : ''}
                      </small>
                    </td>
                    <td>
                      <div className="reference-tags">
                        {version.references?.length ? (
                          version.references.map((reference) => (
                            <Badge
                              key={reference}
                              tone={reference === 'global' ? 'success' : 'neutral'}
                            >
                              {reference === 'global'
                                ? '全量'
                                : state.rules.find((rule) => rule.id === reference)?.name ||
                                  reference}
                            </Badge>
                          ))
                        ) : (
                          <span className="muted">—</span>
                        )}
                      </div>
                    </td>
                    <td className="date-cell">
                      <time dateTime={version.created_at}>{formatDate(version.created_at)}</time>
                    </td>
                    <td>
                      <div className="row-actions">
                        <Button
                          variant="ghost"
                          aria-label={`查看 v${version.number}`}
                          onClick={() => setViewing(version.number)}
                        >
                          查看
                        </Button>
                        <Button
                          variant="ghost"
                          aria-label={`比较 v${version.number}`}
                          onClick={() =>
                            setCompare({ before: state.global_version, after: version.number })
                          }
                        >
                          <GitCompareArrows size={15} aria-hidden="true" />
                          比较
                        </Button>
                        <Button
                          variant="ghost"
                          aria-label={`回退到 v${version.number}`}
                          busy={loading === version.number}
                          onClick={() => rollback(version.number)}
                        >
                          <RotateCcw size={15} aria-hidden="true" />
                          回退
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty title="没有可用历史版本" />
        )}
        <div className="table-footer">
          <span>按创建版本倒序 · 被引用的旧版本会继续保留</span>
          <div className="pagination">
            <Button disabled={page === 0} onClick={() => setPage((p) => p - 1)}>
              上一页
            </Button>
            <span>{page + 1}</span>
            <Button
              disabled={(versions.data?.length ?? 0) <= (page + 1) * 20}
              onClick={() => setPage((p) => p + 1)}
            >
              下一页
            </Button>
          </div>
        </div>
      </section>
      {viewing && (
        <Modal
          open
          onClose={() => setViewing(undefined)}
          title={`历史版本 v${viewing}`}
          description="此版本是只读快照，可进行比较或回退。"
          wide
          footer={<Button onClick={() => setViewing(undefined)}>关闭</Button>}
        >
          {viewed.isPending ? (
            <Loading />
          ) : viewed.error ? (
            <ErrorNotice error={viewed.error} onRetry={() => viewed.refetch()} />
          ) : (
            viewed.data && (
              <>
                <div className="version-meta">
                  <Badge>{viewed.data.format.toUpperCase()}</Badge>
                  <span>{formatDate(viewed.data.created_at)}</span>
                  <span>{viewed.data.description || '未填写版本说明'}</span>
                </div>
                <CodeEditor
                  readOnly
                  value={viewed.data.content}
                  format={viewed.data.format}
                  label={`历史 v${viewing} 内容`}
                />
              </>
            )
          )}
        </Modal>
      )}
      {compare && (
        <HistoryCompare
          key={`${compare.before}-${compare.after}`}
          configKey={configKey}
          versions={versions.data ?? []}
          initial={compare}
          onClose={() => setCompare(undefined)}
        />
      )}
      {action && (
        <ChangeDialog
          configKey={configKey}
          baseline={state}
          action={action}
          onClose={() => setAction(undefined)}
          onSuccess={(next) => {
            onStateChanged(next)
            setAction(undefined)
          }}
          onRebase={onRebase}
        />
      )}
    </>
  )
}
function HistoryCompare({
  configKey,
  versions,
  initial,
  onClose,
}: {
  configKey: ConfigKey
  versions: Version[]
  initial: { before: number; after: number }
  onClose: () => void
}) {
  const [before, setBefore] = useState(initial.before),
    [after, setAfter] = useState(initial.after)
  const a = useQuery({
    queryKey: ['version', configKey, before],
    queryFn: ({ signal }) => api.version(configKey, before, signal),
  })
  const b = useQuery({
    queryKey: ['version', configKey, after],
    queryFn: ({ signal }) => api.version(configKey, after, signal),
  })
  return (
    <Modal
      open
      title="比较历史版本"
      description="比较只读历史内容，不会改变任何发布目标。"
      wide
      onClose={onClose}
      footer={<Button onClick={onClose}>关闭</Button>}
    >
      <div className="history-compare-controls">
        <label>
          左侧版本
          <select value={before} onChange={(e) => setBefore(Number(e.target.value))}>
            {versions.map((v) => (
              <option key={v.number} value={v.number}>
                v{v.number}
              </option>
            ))}
          </select>
        </label>
        <ArrowRight size={18} aria-hidden="true" />
        <label>
          右侧版本
          <select value={after} onChange={(e) => setAfter(Number(e.target.value))}>
            {versions.map((v) => (
              <option key={v.number} value={v.number}>
                v{v.number}
              </option>
            ))}
          </select>
        </label>
      </div>
      <ErrorNotice
        error={a.error || b.error}
        onRetry={() => {
          a.refetch()
          b.refetch()
        }}
      />
      {a.data && b.data ? (
        <ConfigDiff
          before={a.data.content}
          after={b.data.content}
          beforeFormat={a.data.format}
          afterFormat={b.data.format}
        />
      ) : (
        !a.error && !b.error && <Loading label="正在读取比较内容…" />
      )}
    </Modal>
  )
}
