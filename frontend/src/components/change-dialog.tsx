import { Select } from './select'
import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowRight, GitCompareArrows, RefreshCw } from 'lucide-react'
import { api, allVersions, ApiError, configQueryKey } from '../lib/api'
import {
  baselineOf,
  editPayload,
  publicationImpact,
  targetVersion,
  type EditorTarget,
} from '../lib/config'
import type { ConfigKey, ConfigState, Draft, Version } from '../lib/types'
import { ConfigDiff } from './editor'
import { Badge, Button, Confirmation, ErrorNotice, Loading, Modal } from './ui'
import { useToast, usePending } from './providers'

export type ChangeAction =
  | { kind: 'save'; draft: Draft }
  | { kind: 'rollback'; source: Version }
  | { kind: 'promote'; source: EditorTarget }
export function ChangeDialog({
  configKey,
  baseline,
  action,
  onClose,
  onSuccess,
  onRebase,
}: {
  configKey: ConfigKey
  baseline?: ConfigState
  action: ChangeAction
  onClose: () => void
  onSuccess: (state: ConfigState) => void
  onRebase?: (state: ConfigState) => void
}) {
  const client = useQueryClient()
  const toast = useToast()
  const [current, setCurrent] = useState(baseline)
  const originalID = useRef(baseline?.id)
  const graySave = action.kind === 'save' && action.draft.target === 'beta'
  const currentTarget = current ? targetVersion(current, graySave ? 'beta' : undefined) : undefined
  const [comparison, setComparison] = useState(currentTarget?.number ?? 0)
  const [checked, setChecked] = useState(false)
  const [busy, setBusy] = useState(false)
  const [reloading, setReloading] = useState(false)
  const [error, setError] = useState<unknown>()
  const [blocked, setBlocked] = useState(false)
  const [diffReady, setDiffReady] = useState(false)
  usePending('正在发布配置', busy)
  const versions = useQuery({
    queryKey: ['versions', configKey],
    queryFn: ({ signal }) => allVersions(configKey, signal),
    enabled: !!current && !graySave,
  })
  const known = comparison ? current?.versions[comparison] : undefined
  const other = useQuery({
    queryKey: ['version', configKey, comparison],
    queryFn: ({ signal }) => api.version(configKey, comparison, signal),
    enabled: !graySave && comparison > 0 && !known,
  })
  const before = graySave
    ? currentTarget
    : comparison === 0
      ? { content: '', format: action.kind === 'save' ? action.draft.format : action.source.format }
      : (known ?? other.data)
  const promotionSource =
    action.kind === 'promote' && current ? targetVersion(current, 'beta') : undefined
  const after = action.kind === 'save' ? action.draft : (promotionSource ?? action.source)
  const noChange =
    !!currentTarget &&
    currentTarget.content === after.content &&
    currentTarget.format === after.format &&
    (!graySave || currentTarget.description === after.description)
  const isConflict = error instanceof ApiError && error.status === 409
  useEffect(() => {
    setChecked(false)
    setDiffReady(false)
  }, [comparison])
  const title =
    action.kind === 'save'
      ? graySave
        ? '确认灰度发布'
        : '确认保存并发布'
      : action.kind === 'rollback'
        ? '确认全量回退'
        : '确认转为全量'
  const submitLabel =
    action.kind === 'rollback'
      ? '确认回退并发布'
      : action.kind === 'promote'
        ? '确认转为全量'
        : '确认发布'
  async function submit() {
    if (busy || !checked || !before || noChange || blocked || isConflict || !diffReady) return
    setBusy(true)
    setError(undefined)
    try {
      const result =
        action.kind === 'save'
          ? await api.save(configKey, editPayload(current, action.draft))
          : action.kind === 'rollback'
            ? await api.rollback(configKey, baselineOf(current), action.source.number)
            : await api.promote(configKey, baselineOf(current))
      client.setQueryData(configQueryKey(configKey), result.state)
      await Promise.all([
        client.invalidateQueries({ queryKey: ['configs'] }),
        client.invalidateQueries({ queryKey: ['config-index'] }),
        client.invalidateQueries({ queryKey: ['versions', configKey] }),
      ])
      toast(
        result.changed
          ? graySave
            ? 'beta 配置已更新并生效'
            : `已发布 v${result.state.last_version}`
          : '内容未变化。',
      )
      onSuccess(result.state)
    } catch (e) {
      setError(e)
      setChecked(false)
    } finally {
      setBusy(false)
    }
  }
  async function reload() {
    setReloading(true)
    setError(undefined)
    setChecked(false)
    try {
      const latest = await api.state(configKey)
      if (latest.id !== originalID.current) {
        setBlocked(true)
        setError('原配置已被删除后重建。为避免覆盖新配置，请保留草稿并重新打开。')
        return
      }
      if ((graySave || action.kind === 'promote') && !latest.beta) {
        setBlocked(true)
        setError('beta 配置已被删除。草稿已保留，请返回编辑区复制内容或选择其他目标。')
        return
      }
      setCurrent(latest)
      setComparison(targetVersion(latest, graySave ? 'beta' : undefined).number)
      setDiffReady(false)
      onRebase?.(latest)
      client.setQueryData(configQueryKey(configKey), latest)
      await client.invalidateQueries({ queryKey: ['versions', configKey] })
    } catch (e) {
      setError(e)
      if (e instanceof ApiError && e.status === 404) setBlocked(true)
    } finally {
      setReloading(false)
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={title}
      wide
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            返回编辑
          </Button>
          <Button
            variant="primary"
            busy={busy}
            disabled={
              !checked || !before || noChange || blocked || isConflict || !diffReady || reloading
            }
            onClick={submit}
          >
            {submitLabel}
            <ArrowRight size={16} aria-hidden="true" />
          </Button>
        </>
      }
    >
      <div className="publish-summary">
        <div>
          <Badge tone="success">{graySave ? '灰度发布' : '全量发布'}</Badge>
          <span className="mono">{configKey.name}</span>
          {action.kind !== 'save' && (
            <Badge>
              {action.kind === 'promote' ? '来源 beta 配置' : `来源 v${action.source.number}`}
            </Badge>
          )}
          <ArrowRight size={14} aria-hidden="true" />
          <strong>
            {graySave ? '更新 beta 配置（无编号）' : `新版本 v${(current?.last_version ?? 0) + 1}`}
          </strong>
        </div>
        {(graySave || !!current?.rules.length) && (
          <p>{publicationImpact(current, graySave ? 'beta' : undefined)}</p>
        )}
      </div>
      <ErrorNotice error={error} />
      {isConflict && !blocked && current && (
        <Button onClick={reload} busy={reloading}>
          <RefreshCw size={16} aria-hidden="true" />
          读取最新版本并重新对比
        </Button>
      )}
      {isConflict && !current && (
        <p className="field-error">该名称已存在，请返回编辑并更换名称。</p>
      )}
      {noChange && <div className="notice info">内容与当前编辑目标一致，无需发布。</div>}
      {!graySave && (
        <>
          <div className="diff-toolbar">
            <label>
              <GitCompareArrows size={16} aria-hidden="true" />
              对比版本
              <Select
                aria-label="对比版本"
                value={comparison}
                onValueChange={(value) => {
                  setComparison(Number(value))
                  setChecked(false)
                }}
                disabled={busy || reloading}
              >
                {!current && <option value={0}>空内容（首次创建）</option>}
                {current && (
                  <option value={currentTarget!.number}>
                    v{currentTarget!.number} · 当前编辑目标
                  </option>
                )}
                {versions.data
                  ?.filter((v) => v.number !== currentTarget?.number)
                  .map((v) => (
                    <option key={v.number} value={v.number}>
                      v{v.number}
                      {v.description ? ` · ${v.description}` : ''}
                    </option>
                  ))}
              </Select>
            </label>
          </div>
          {versions.error && (
            <ErrorNotice error={versions.error} onRetry={() => versions.refetch()} />
          )}
          {other.error && !known && (
            <ErrorNotice error={other.error} onRetry={() => other.refetch()} />
          )}
        </>
      )}
      {before ? (
        <ConfigDiff
          key={`${current?.revision ?? 0}-${comparison}`}
          beforeTitle={graySave ? '修改前 · 当前灰度内容' : undefined}
          afterTitle={graySave ? '修改后 · 将覆盖临时内容' : undefined}
          before={before.content}
          after={after.content}
          beforeFormat={before.format}
          afterFormat={after.format}
          onReady={setDiffReady}
        />
      ) : (
        <Loading label="正在读取对比版本…" />
      )}
      <div className="confirmation-row">
        <Confirmation checked={checked} onChange={setChecked} />
      </div>
    </Modal>
  )
}
