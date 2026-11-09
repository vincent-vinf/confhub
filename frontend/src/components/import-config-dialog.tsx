import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../lib/api'
import { allows } from '../lib/permissions'
import type { ImportPlan, ImportPolicy } from '../lib/types'
import { useAuth, usePending, useToast } from './providers'
import { Select } from './select'
import { Badge, Button, Confirmation, ErrorNotice, Modal } from './ui'

const actions = {
  create: '新建',
  conflict: '同名冲突',
  skip: '跳过',
  overwrite: '覆盖',
  unchanged: '不变',
  error: '错误',
}
export function ImportConfigDialog({
  initialNamespace,
  onClose,
  onSuccess,
}: {
  initialNamespace: string
  onClose: () => void
  onSuccess: () => void
}) {
  const { user } = useAuth()
  const [namespace, setNamespace] = useState(initialNamespace)
  const [policy, setPolicy] = useState<ImportPolicy>('ABORT')
  const [file, setFile] = useState<File>()
  const [plan, setPlan] = useState<ImportPlan>()
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  const [page, setPage] = useState(0)
  const errorRef = useRef<HTMLDivElement>(null)
  const controller = useRef<AbortController | undefined>(undefined)
  useEffect(() => () => controller.current?.abort(), [])
  useEffect(() => {
    if (error) errorRef.current?.focus()
  }, [error])
  const queryClient = useQueryClient()
  const toast = useToast()
  const namespaces = useQuery({
    queryKey: ['namespaces'],
    queryFn: ({ signal }) => api.namespaces(signal),
  })
  const targets = namespaces.data?.filter((n) => allows(user, 'config.write', n))
  usePending('正在处理配置导入', busy)
  function reset() {
    setPlan(undefined)
    setConfirmed(false)
    setError(undefined)
    setPage(0)
  }
  async function preview() {
    if (!file || busy) return
    reset()
    setBusy(true)
    controller.current = new AbortController()
    try {
      setPlan(await api.previewImport(file, namespace, policy, controller.current.signal))
    } catch (e) {
      if (!(e instanceof DOMException && e.name === 'AbortError')) setError(e)
    } finally {
      setBusy(false)
    }
  }
  async function submit() {
    if (!file || !plan?.can_import || !confirmed || busy) return
    setBusy(true)
    setError(undefined)
    try {
      const result = await api.importArchive(file, plan)
      await Promise.all(
        ['configs', 'config-index', 'config', 'groups'].map((key) =>
          queryClient.invalidateQueries({ queryKey: [key] }),
        ),
      )
      toast(
        `导入完成：新建 ${result.created}，覆盖 ${result.updated}，跳过 ${result.skipped}，不变 ${result.unchanged}。`,
      )
      onSuccess()
    } catch (e) {
      setPlan(undefined)
      setConfirmed(false)
      setError(
        e instanceof ApiError && e.status === 409
          ? '配置已发生变化，整包未提交。请重新预览并确认。'
          : e,
      )
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      title="导入配置"
      onClose={onClose}
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button
            onClick={preview}
            busy={busy && !plan}
            disabled={busy || !file || !targets?.includes(namespace)}
          >
            {plan ? '重新预览' : '预览导入'}
          </Button>
          {plan && (
            <Button
              variant="primary"
              onClick={submit}
              busy={busy}
              disabled={!plan.can_import || !confirmed}
            >
              导入并发布
            </Button>
          )}
        </>
      }
    >
      <div className="form-stack">
        <div ref={errorRef} tabIndex={-1}>
          <ErrorNotice error={error || namespaces.error} />
        </div>
        <label className="form-field">
          ZIP 文件
          <input
            type="file"
            accept=".zip,application/zip"
            disabled={busy}
            onChange={(e) => {
              reset()
              const next = e.target.files?.[0]
              setFile(next)
              if (next && next.size > 8 * 1024 * 1024) {
                setFile(undefined)
                setError('ZIP 文件不能超过 8 MiB。')
              }
            }}
          />
        </label>
        <label className="form-field">
          目标命名空间
          <Select
            aria-label="目标命名空间"
            value={namespace}
            disabled={busy || namespaces.isPending}
            onValueChange={(n) => {
              setNamespace(n)
              reset()
            }}
          >
            {targets?.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </Select>
        </label>
        <label className="form-field">
          同名配置
          <Select
            aria-label="同名配置"
            value={policy}
            disabled={busy}
            onValueChange={(p) => {
              setPolicy(p as ImportPolicy)
              reset()
            }}
          >
            <option value="ABORT">中止导入</option>
            <option value="SKIP">跳过已有配置</option>
            <option value="OVERWRITE">覆盖并发布</option>
          </Select>
        </label>
        {plan && (
          <>
            <div className="archive-summary" role="status">
              共 {plan.entries.length} 份配置
              {Object.entries(actions).map(([action, label]) => {
                const count = plan.entries.filter((e) => e.action === action).length
                return count > 0 ? (
                  <Badge
                    key={action}
                    tone={action === 'conflict' || action === 'error' ? 'danger' : 'neutral'}
                  >
                    {label} {count}
                  </Badge>
                ) : null
              })}
            </div>
            {plan.missing_groups.length > 0 && (
              <div className="notice">将创建分组：{plan.missing_groups.join('、')}</div>
            )}
            {!plan.can_import && (
              <ErrorNotice error="存在冲突或错误，整包不会提交。请修正文件、分组或冲突策略后重新预览。" />
            )}
            <div className="table-scroll">
              <table className="archive-table">
                <thead>
                  <tr>
                    <th>分组</th>
                    <th>配置名称</th>
                    <th>处理</th>
                  </tr>
                </thead>
                <tbody>
                  {plan.entries.slice(page * 25, (page + 1) * 25).map((e) => (
                    <tr key={`${e.group}/${e.name}`}>
                      <td className="mono">{e.group}</td>
                      <td className="mono">{e.name}</td>
                      <td>
                        {actions[e.action]}
                        {e.error && <div className="archive-item-error">{e.error}</div>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {plan.entries.length > 25 && (
              <div className="pagination">
                <Button disabled={busy || page === 0} onClick={() => setPage((p) => p - 1)}>
                  上一页
                </Button>
                <span>
                  {page + 1} / {Math.ceil(plan.entries.length / 25)}
                </span>
                <Button
                  disabled={busy || (page + 1) * 25 >= plan.entries.length}
                  onClick={() => setPage((p) => p + 1)}
                >
                  下一页
                </Button>
              </div>
            )}
            {plan.can_import && (
              <Confirmation
                checked={confirmed}
                onChange={setConfirmed}
                label="确认导入并立即发布这些配置"
              />
            )}
          </>
        )}
      </div>
    </Modal>
  )
}
