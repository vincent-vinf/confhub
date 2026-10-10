import { Select } from '../components/select'
import { useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { ArrowLeft, FilePlus2, Save, WandSparkles } from 'lucide-react'
import { api, configUrl, listUrl } from '../lib/api'
import { bytes, nameError } from '../lib/config'
import { formatContent, validateContent } from '../lib/format'
import { formats, type Draft } from '../lib/types'
import { CodeEditor, type EditorHandle } from '../components/editor'
import { ChangeDialog } from '../components/change-dialog'
import { Button, ErrorNotice } from '../components/ui'
import { useDirty, useToast, useNavigationPermit } from '../components/providers'

export function ConfigCreatePage() {
  const permit = useNavigationPermit()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const toast = useToast()
  const [namespace, setNamespace] = useState(params.get('namespace') ?? 'public'),
    [group, setGroup] = useState(params.get('group') ?? 'DEFAULT_GROUP')
  const [name, setName] = useState('')
  const [draft, setDraft] = useState<Draft>({ content: '', format: 'yaml', description: '' })
  const draftRef = useRef(draft)
  draftRef.current = draft
  const [review, setReview] = useState<Draft>()
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const [formatting, setFormatting] = useState(false)
  const [published, setPublished] = useState(false)
  const editor = useRef<EditorHandle>(null)
  const namespaces = useQuery({
    queryKey: ['namespaces'],
    queryFn: ({ signal }) => api.namespaces(signal),
  })
  const groups = useQuery({
    queryKey: ['groups', namespace],
    queryFn: ({ signal }) => api.groups(namespace, signal),
  })
  useDirty('新建配置的未保存内容', !published && (!!name || !!draft.content || !!draft.description))
  async function prepare() {
    if (busy) return
    const invalidName = nameError(name)
    if (invalidName) {
      setError(invalidName)
      document.getElementById('new-name')?.focus()
      return
    }
    if (!namespaces.data?.includes(namespace) || !groups.data?.includes(group)) {
      setError('请选择有效的命名空间和分组。')
      return
    }
    if (bytes(draft.description) > 4096) {
      setError('版本说明不能超过 4096 字节。')
      return
    }
    const captured = draft
    setBusy(true)
    setError(undefined)
    try {
      const invalid = await validateContent(draft.format, draft.content)
      if (invalid) {
        setError(invalid)
        return
      }
      if (draftRef.current !== captured) {
        setError('内容发生变化，请重新点击保存。')
        return
      }
      setReview({ ...captured })
    } finally {
      setBusy(false)
    }
  }
  async function format() {
    const captured = draft
    setFormatting(true)
    setError(undefined)
    try {
      const value = await formatContent(captured.format, captured.content)
      if (draftRef.current !== captured) {
        setError('内容发生变化，请重新格式化。')
        return
      }
      editor.current?.replace(value)
      toast('已格式化，可使用 Ctrl / ⌘ + Z 撤销。')
    } catch (e) {
      setError(e)
    } finally {
      setFormatting(false)
    }
  }
  return (
    <>
      <div className="detail-breadcrumb">
        <Link to={listUrl(namespace, group)}>
          <ArrowLeft size={15} aria-hidden="true" />
          返回配置列表
        </Link>
      </div>
      <div className="page-heading">
        <div>
          <h1>新建配置</h1>
        </div>
        <span className="heading-icon">
          <FilePlus2 size={30} aria-hidden="true" />
        </span>
      </div>
      <section className="panel">
        <div className="create-fields">
          <label>
            命名空间
            <Select
              aria-label="命名空间"
              value={namespace}
              onValueChange={(value) => {
                setNamespace(value)
                setGroup('')
              }}
            >
              {namespaces.data?.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </Select>
          </label>
          <label>
            分组
            <Select aria-label="分组" value={group} onValueChange={(value) => setGroup(value)}>
              <option value="">选择分组</option>
              {groups.data?.map((g) => (
                <option key={g}>{g}</option>
              ))}
            </Select>
          </label>
          <label>
            配置名称 <span className="required">*</span>
            <input
              id="new-name"
              placeholder="例如 application.yaml"
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                setError(undefined)
              }}
              autoComplete="off"
            />
          </label>
        </div>
        <ErrorNotice error={error || namespaces.error || groups.error} />
        <div className="editor-toolbar">
          <div className="panel-title">配置内容</div>
          <div className="editor-options">
            <label>
              格式
              <Select
                aria-label="配置格式"
                value={draft.format}
                onValueChange={(value) => setDraft({ ...draft, format: value as Draft['format'] })}
              >
                {formats.map((format) => (
                  <option key={format} value={format}>
                    {format === 'text' ? '纯文本' : format.toUpperCase()}
                  </option>
                ))}
              </Select>
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
        <CodeEditor
          ref={editor}
          value={draft.content}
          format={draft.format}
          onChange={(content) => {
            setDraft((current) => ({ ...current, content }))
            setError(undefined)
          }}
        />
        <div className="editor-status">
          <span>首次保存将创建 v1</span>
          <span className="mono">{bytes(draft.content).toLocaleString()} 字节</span>
        </div>
        <div className="editor-description">
          <label htmlFor="new-description">
            版本说明 <span>选填</span>
          </label>
          <input
            id="new-description"
            placeholder="简要说明配置用途或这次变更…"
            value={draft.description}
            onChange={(e) => setDraft({ ...draft, description: e.target.value })}
          />
        </div>
        <div className="editor-footer">
          <Button variant="primary" busy={busy} onClick={prepare}>
            <Save size={16} aria-hidden="true" />
            保存并发布
          </Button>
        </div>
      </section>
      {review && (
        <ChangeDialog
          configKey={{ namespace, group, name }}
          action={{ kind: 'save', draft: review }}
          onClose={() => setReview(undefined)}
          onSuccess={(state) => {
            setPublished(true)
            setReview(undefined)
            permit()
            navigate(configUrl(state.key), { replace: true })
          }}
        />
      )}
    </>
  )
}
