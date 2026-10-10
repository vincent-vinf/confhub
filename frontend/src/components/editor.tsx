import { useEffect, useState, useImperativeHandle, useLayoutEffect, useRef, type Ref } from 'react'
import { Compartment, EditorState, Transaction, type Extension } from '@codemirror/state'
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  drawSelection,
  highlightSpecialChars,
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from '@codemirror/language'
import { tags } from '@lezer/highlight'
import { MergeView } from '@codemirror/merge'
import type { Format } from '../lib/types'
import { useTheme } from './providers'
import { Button, ErrorNotice, Loading } from './ui'

const highlight = syntaxHighlighting(
  HighlightStyle.define([
    { tag: [tags.keyword, tags.bool, tags.null], color: 'var(--syntax-keyword)' },
    { tag: [tags.string, tags.attributeValue, tags.quote], color: 'var(--syntax-string)' },
    { tag: [tags.number, tags.atom], color: 'var(--syntax-number)' },
    {
      tag: [tags.propertyName, tags.attributeName, tags.tagName, tags.variableName, tags.heading],
      color: 'var(--syntax-property)',
    },
    { tag: tags.comment, color: 'var(--muted)', fontStyle: 'italic' },
  ]),
)
const appearance = (dark: boolean) =>
  EditorView.theme(
    {
      '&': { color: 'var(--text)', backgroundColor: 'var(--surface)' },
      '.cm-content': { caretColor: 'var(--text)' },
    },
    { dark },
  )
async function language(format: Format): Promise<Extension> {
  switch (format) {
    case 'json':
      return (await import('@codemirror/lang-json')).json()
    case 'yaml':
      return (await import('@codemirror/lang-yaml')).yaml()
    case 'xml':
      return (await import('@codemirror/lang-xml')).xml()
    case 'toml':
      return StreamLanguage.define((await import('@codemirror/legacy-modes/mode/toml')).toml)
    case 'properties':
    case 'ini':
      return StreamLanguage.define(
        (await import('@codemirror/legacy-modes/mode/properties')).properties,
      )
    default:
      return []
  }
}
export type EditorHandle = { replace: (value: string) => void; focus: () => void }
export function CodeEditor({
  value,
  format,
  onChange,
  label = '配置内容',
  readOnly = false,
  ref,
}: {
  value: string
  format: Format
  onChange?: (value: string) => void
  label?: string
  readOnly?: boolean
  ref?: Ref<EditorHandle>
}) {
  const container = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const languageSlot = useRef(new Compartment())
  const themeSlot = useRef(new Compartment())
  const { theme } = useTheme()
  useLayoutEffect(() => {
    const editor = new EditorView({
      parent: container.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightSpecialChars(),
          drawSelection(),
          history(),
          highlightActiveLine(),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          EditorView.lineWrapping,
          highlight,
          languageSlot.current.of([]),
          themeSlot.current.of(appearance(theme === 'dark')),
          EditorState.readOnly.of(readOnly),
          EditorView.editable.of(!readOnly),
          EditorView.contentAttributes.of({
            'aria-label': label,
            'aria-multiline': 'true',
            'aria-readonly': String(readOnly),
            role: 'textbox',
          }),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) onChangeRef.current?.(update.state.doc.toString())
          }),
        ],
      }),
    })
    view.current = editor
    return () => {
      view.current = null
      editor.destroy()
    }
    // Construction is once per editor; document/language/theme changes use transactions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readOnly, label])
  useEffect(() => {
    const editor = view.current
    if (editor && editor.state.doc.toString() !== value)
      editor.dispatch({
        changes: { from: 0, to: editor.state.doc.length, insert: value },
        annotations: Transaction.addToHistory.of(false),
      })
  }, [value])
  useEffect(() => {
    let active = true
    language(format)
      .then((extension) => {
        if (active) view.current?.dispatch({ effects: languageSlot.current.reconfigure(extension) })
      })
      .catch(() => {
        if (active) view.current?.dispatch({ effects: languageSlot.current.reconfigure([]) })
      })
    return () => {
      active = false
    }
  }, [format])
  useEffect(() => {
    view.current?.dispatch({ effects: themeSlot.current.reconfigure(appearance(theme === 'dark')) })
  }, [theme])
  useImperativeHandle(
    ref,
    () => ({
      replace: (next) => {
        const editor = view.current
        if (editor)
          editor.dispatch({
            changes: { from: 0, to: editor.state.doc.length, insert: next },
            annotations: Transaction.userEvent.of('input.format'),
          })
      },
      focus: () => view.current?.focus(),
    }),
    [],
  )
  return <div ref={container} className={`code-editor ${readOnly ? 'readonly' : ''}`} />
}
export function ConfigDiff({
  before,
  after,
  beforeFormat,
  afterFormat,
  onReady,
  beforeTitle = '对比版本',
  afterTitle = '待发布 / 选中版本',
}: {
  before: string
  after: string
  beforeFormat: Format
  afterFormat: Format
  onReady?: (ready: boolean) => void
  beforeTitle?: string
  afterTitle?: string
}) {
  const container = useRef<HTMLDivElement>(null)
  const { theme } = useTheme()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>()
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    let active = true
    let merge: MergeView | undefined
    setLoading(true)
    setError(undefined)
    onReady?.(false)
    Promise.all([language(beforeFormat), language(afterFormat)])
      .then(([a, b]) => {
        if (!active) return
        const extensions = (label: string, lang: Extension) => [
          lineNumbers(),
          highlight,
          lang,
          appearance(theme === 'dark'),
          EditorState.readOnly.of(true),
          EditorView.editable.of(false),
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({
            role: 'textbox',
            'aria-readonly': 'true',
            'aria-label': label,
            tabindex: '0',
          }),
        ]
        merge = new MergeView({
          parent: container.current!,
          a: { doc: before, extensions: extensions('对比版本内容', a) },
          b: { doc: after, extensions: extensions('待发布内容', b) },
          highlightChanges: true,
          gutter: true,
          collapseUnchanged: { margin: 3, minSize: 8 },
        })
        merge.dom.querySelector('.cm-merge-a')?.setAttribute('data-diff-title', beforeTitle)
        merge.dom.querySelector('.cm-merge-b')?.setAttribute('data-diff-title', afterTitle)
        setLoading(false)
        onReady?.(true)
      })
      .catch((error) => {
        if (active) {
          setLoading(false)
          setError(error)
          onReady?.(false)
        }
      })
    return () => {
      active = false
      merge?.destroy()
    }
  }, [before, after, beforeFormat, afterFormat, theme, attempt, onReady, beforeTitle, afterTitle])
  return (
    <div className="diff-frame">
      {loading && <Loading label="正在准备版本差异…" />}
      {!!error && (
        <div>
          <ErrorNotice error="差异视图加载失败，请重试。" />
          <Button onClick={() => setAttempt((a) => a + 1)}>重试加载</Button>
        </div>
      )}
      <div className="diff-head">
        <span>{beforeTitle}</span>
        <span>{afterTitle}</span>
      </div>
      <div ref={container} className="diff-content" data-testid="config-diff" />
    </div>
  )
}
