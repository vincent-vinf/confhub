import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import {
  ArrowLeft,
  Check,
  ChevronRight,
  Copy,
  FileCode2,
  GitBranch,
  History,
  RotateCcw,
  Save,
  Trash2,
  WandSparkles,
} from 'lucide-react'
import { api, configQueryKey, listUrl } from '../lib/api'
import { bytes, draftFrom, targetVersion } from '../lib/config'
import { formatContent, validateContent } from '../lib/format'
import { formats, type ConfigKey, type ConfigState, type Draft } from '../lib/types'
import { CodeEditor, type EditorHandle } from '../components/editor'
import { Badge, Button, ErrorNotice, Loading, Modal } from '../components/ui'
import { useDirty, useToast } from '../components/providers'
import { ChangeDialog } from '../components/change-dialog'
import { HistoryPanel } from './history'
import { RulesPanel } from './rules'
import { DeleteConfigDialog } from '../components/delete-dialog'

export function ConfigDetailPage() {
  const params = useParams()
  const key = { namespace: params.namespace!, group: params.group!, name: params.name! }
  return <Detail key={`${key.namespace}/${key.group}/${key.name}`} configKey={key} />
}
function Detail({ configKey }: { configKey: ConfigKey }) {
  const query = useQuery({
    queryKey: configQueryKey(configKey),
    queryFn: ({ signal }) => api.state(configKey, signal),
    staleTime: 0,
    refetchOnMount: 'always',
  })
  const client = useQueryClient()
  const toast = useToast()
  const [params, setParams] = useSearchParams()
  const tab = ['content', 'history', 'rules'].includes(params.get('tab') ?? '')
    ? params.get('tab')!
    : 'content'
  const [baseline, setBaseline] = useState<ConfigState>()
  const [draft, setDraft] = useState<Draft>()
  const draftRef = useRef(draft)
  draftRef.current = draft
  const [saving, setSaving] = useState(false)
  const [formatting, setFormatting] = useState(false)
  const [error, setError] = useState<unknown>()
  const [review, setReview] = useState<Draft>()
  const [deleting, setDeleting] = useState(false)
  const [pendingTarget, setPendingTarget] = useState<{ ruleId?: string }>()
  const [reset, setReset] = useState(false)
  const editor = useRef<EditorHandle>(null)
  useEffect(() => {
    if (query.data && !query.isFetching && !query.error && !baseline) {
      setBaseline(query.data)
      setDraft(draftFrom(query.data))
    }
  }, [query.data, query.isFetching, query.error, baseline])
  let target: ReturnType<typeof targetVersion> | undefined
  try {
    if (baseline && draft) target = targetVersion(baseline, draft.ruleId)
  } catch {
    /* Deleted rules keep the user's draft. */
  }
  const dirty =
    !!draft &&
    (!target ||
      draft.content !== target.content ||
      draft.format !== target.format ||
      draft.description !== '')
  useDirty(`配置「${configKey.name}」的未保存编辑`, dirty)
  function applyState(state: ConfigState, preserveDraft = false) {
    client.setQueryData(configQueryKey(configKey), state)
    setBaseline(state)
    if (!preserveDraft || !dirty) {
      try {
        setDraft(draftFrom(state, draft?.ruleId))
      } catch {
        setDraft(draftFrom(state))
      }
    }
  }
  function switchTarget(ruleId?: string) {
    if (!baseline) return
    setDraft(draftFrom(baseline, ruleId))
    setError(undefined)
    setParams({ tab: 'content' })
    setPendingTarget(undefined)
  }
  async function reviewSave() {
    if (!draft || !baseline || saving) return
    const captured = draft
    setSaving(true)
    setError(undefined)
    try {
      const invalid = await validateContent(captured.format, captured.content)
      if (invalid) {
        setError(invalid)
        return
      }
      if (bytes(captured.description) > 4096) {
        setError('版本说明不能超过 4096 字节。')
        return
      }
      if (draftRef.current !== captured) {
        setError('内容发生变化，请重新点击保存。')
        return
      }
      setReview({ ...captured })
    } finally {
      setSaving(false)
    }
  }
  async function format() {
    if (!draft) return
    const captured = draft
    setFormatting(true)
    setError(undefined)
    try {
      const formatted = await formatContent(captured.format, captured.content)
      if (draftRef.current !== captured) {
        setError('内容发生变化，请重新点击格式化。')
        return
      }
      editor.current?.replace(formatted)
      toast('已格式化，可使用 Ctrl / ⌘ + Z 撤销。')
    } catch (e) {
      setError(e)
    } finally {
      setFormatting(false)
    }
  }
  if (!baseline || !draft)
    return query.error ? (
      <ErrorNotice error={query.error} onRetry={() => query.refetch()} />
    ) : (
      <Loading label="正在读取配置…" />
    )
  return (
    <>
      <div className="detail-breadcrumb">
        <Link to={listUrl(configKey.namespace, configKey.group)}>
          <ArrowLeft size={15} aria-hidden="true" />
          配置列表
        </Link>
        <ChevronRight size={14} aria-hidden="true" />
        <span>{configKey.namespace}</span>
        <ChevronRight size={14} aria-hidden="true" />
        <span>{configKey.group}</span>
      </div>
      <div className="page-heading detail-heading">
        <div>
          <div className="title-line">
            <span className="file-icon large">
              <FileCode2 size={24} aria-hidden="true" />
            </span>
            <h1 className="mono">{configKey.name}</h1>
            <Badge tone="success">全量 v{baseline.global_version}</Badge>
          </div>
        </div>
        <Button variant="ghost" className="destructive-text" onClick={() => setDeleting(true)}>
          <Trash2 size={16} aria-hidden="true" />
          删除配置
        </Button>
      </div>
      <div className="tabs" role="tablist" aria-label="配置管理区域">
        {[
          { id: 'content', label: '配置内容', icon: FileCode2 },
          { id: 'history', label: '版本历史', icon: History },
          { id: 'rules', label: '灰度规则', icon: GitBranch },
        ].map((item) => (
          <button
            id={`tab-${item.id}`}
            key={item.id}
            role="tab"
            tabIndex={tab === item.id ? 0 : -1}
            onKeyDown={(event) => {
              const ids = ['content', 'history', 'rules']
              let index = ids.indexOf(item.id)
              if (event.key === 'ArrowRight') index = (index + 1) % 3
              else if (event.key === 'ArrowLeft') index = (index + 2) % 3
              else if (event.key === 'Home') index = 0
              else if (event.key === 'End') index = 2
              else return
              event.preventDefault()
              setParams({ tab: ids[index] })
              document.getElementById(`tab-${ids[index]}`)?.focus()
            }}
            aria-selected={tab === item.id}
            aria-controls={`panel-${item.id}`}
            onClick={() => setParams({ tab: item.id })}
            className={tab === item.id ? 'active' : ''}
          >
            <item.icon size={17} aria-hidden="true" />
            {item.label}
            {item.id === 'rules' && <span className="tab-count">{baseline.rules.length}</span>}
          </button>
        ))}
      </div>
      {dirty && tab !== 'content' && (
        <div className="notice info">
          编辑区仍有未保存内容，已为你保留。
          <Button variant="ghost" onClick={() => setParams({ tab: 'content' })}>
            返回编辑
          </Button>
        </div>
      )}
      <section
        id="panel-content"
        role="tabpanel"
        aria-labelledby="tab-content"
        hidden={tab !== 'content'}
      >
        <div className="panel editor-panel">
          <div className="editor-toolbar">
            <div className="edit-target">
              <label>
                编辑目标
                <select
                  aria-label="编辑目标"
                  value={draft.ruleId ?? 'global'}
                  onChange={(e) => {
                    const next = {
                      ruleId: e.target.value === 'global' ? undefined : e.target.value,
                    }
                    if (dirty) setPendingTarget(next)
                    else switchTarget(next.ruleId)
                  }}
                >
                  <option value="global">全量配置 · v{baseline.global_version}</option>
                  {baseline.rules.map((rule) => (
                    <option key={rule.id} value={rule.id}>
                      {rule.name || rule.id} · v{rule.target_version}
                      {rule.enabled ? '' : '（停用）'}
                    </option>
                  ))}
                  {draft.ruleId && !target && (
                    <option value={draft.ruleId}>已删除的灰度规则 · 草稿保留</option>
                  )}
                </select>
              </label>
              {target && <Badge>{target.format.toUpperCase()}</Badge>}
            </div>
            <div className="editor-options">
              <label>
                格式
                <select
                  aria-label="配置格式"
                  value={draft.format}
                  onChange={(e) => {
                    setDraft({ ...draft, format: e.target.value as Draft['format'] })
                    setError(undefined)
                  }}
                >
                  {formats.map((format) => (
                    <option key={format} value={format}>
                      {format === 'text' ? '纯文本' : format.toUpperCase()}
                    </option>
                  ))}
                </select>
              </label>
              <Button
                variant="ghost"
                disabled={!['json', 'yaml'].includes(draft.format)}
                busy={formatting}
                onClick={format}
              >
                <WandSparkles size={16} aria-hidden="true" />
                格式化
              </Button>
            </div>
          </div>
          <ErrorNotice
            error={
              error ||
              (!target ? '编辑目标已被删除，当前草稿仍保留。请复制内容或选择其他目标。' : undefined)
            }
          />
          <CodeEditor
            ref={editor}
            value={draft.content}
            format={draft.format}
            onChange={(value) => {
              setDraft((previous) => (previous ? { ...previous, content: value } : previous))
              setError(undefined)
            }}
          />
          <div className="editor-status">
            <span>
              {dirty ? (
                <>
                  <span className="status-dot changed" aria-hidden="true" />
                  未保存的更改
                </>
              ) : (
                <>
                  <Check size={14} aria-hidden="true" />
                  与目标版本一致
                </>
              )}
            </span>
            <span className="mono">
              {draft.format.toUpperCase()} · {bytes(draft.content).toLocaleString()} 字节 · r
              {baseline.revision}
            </span>
          </div>
          <div className="editor-description">
            <label htmlFor="version-description">
              版本说明 <span>选填</span>
            </label>
            <input
              id="version-description"
              placeholder="简要说明这次内容变更…"
              value={draft.description}
              onChange={(e) => setDraft({ ...draft, description: e.target.value })}
            />
          </div>
          <div className="editor-footer">
            <div className="editor-footer-help">
              {draft.ruleId
                ? '保存仅更新此灰度规则的目标版本。'
                : '保存即全量发布，固定灰度版本保持不变。'}
            </div>
            <div className="button-row">
              <Button
                variant="ghost"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(draft.content)
                    toast('配置内容已复制。')
                  } catch {
                    setError('浏览器未允许复制，请在编辑器中全选后复制。')
                  }
                }}
              >
                <Copy size={16} aria-hidden="true" />
                复制
              </Button>
              <Button onClick={() => setReset(true)} disabled={!dirty}>
                <RotateCcw size={16} aria-hidden="true" />
                还原编辑
              </Button>
              <Button
                variant="primary"
                busy={saving}
                disabled={!target || draft.content === target.content}
                onClick={reviewSave}
              >
                <Save size={16} aria-hidden="true" />
                保存并发布
              </Button>
            </div>
          </div>
        </div>
      </section>
      {tab === 'history' && (
        <section id="panel-history" role="tabpanel" aria-labelledby="tab-history">
          <HistoryPanel
            configKey={configKey}
            state={baseline}
            onStateChanged={(state) => applyState(state, true)}
            onRebase={setBaseline}
          />
        </section>
      )}
      {tab === 'rules' && (
        <section id="panel-rules" role="tabpanel" aria-labelledby="tab-rules">
          <RulesPanel
            configKey={configKey}
            state={baseline}
            onStateChanged={(state) => applyState(state, true)}
            onEditRule={(ruleId) => (dirty ? setPendingTarget({ ruleId }) : switchTarget(ruleId))}
          />
        </section>
      )}
      {review && (
        <ChangeDialog
          configKey={configKey}
          baseline={baseline}
          action={{ kind: 'save', draft: review }}
          onClose={() => setReview(undefined)}
          onRebase={setBaseline}
          onSuccess={(state) => {
            applyState(state)
            setReview(undefined)
          }}
        />
      )}
      {deleting && (
        <DeleteConfigDialog
          configKey={configKey}
          state={baseline}
          onClose={() => setDeleting(false)}
        />
      )}
      <Modal
        open={!!pendingTarget || reset}
        title="放弃当前编辑？"
        description="切换编辑目标或还原会丢弃当前未保存的内容。版本历史不会受到影响。"
        onClose={() => {
          setPendingTarget(undefined)
          setReset(false)
        }}
        footer={
          <>
            <Button
              onClick={() => {
                setPendingTarget(undefined)
                setReset(false)
              }}
            >
              继续编辑
            </Button>
            <Button
              variant="danger"
              onClick={() => {
                switchTarget(
                  pendingTarget ? pendingTarget.ruleId : target ? draft.ruleId : undefined,
                )
                setReset(false)
              }}
            >
              放弃并继续
            </Button>
          </>
        }
      />
    </>
  )
}
