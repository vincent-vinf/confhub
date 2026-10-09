import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowDown,
  ArrowUp,
  FlaskConical,
  GitBranch,
  Pencil,
  Plus,
  Power,
  Trash2,
  X,
} from 'lucide-react'
import { api, allVersions, ApiError, configQueryKey } from '../lib/api'
import { baselineOf, ruleError, ruleId } from '../lib/config'
import type { Condition, ConfigKey, ConfigState, Effective, GrayRule } from '../lib/types'
import { ChangeDialog, type ChangeAction } from '../components/change-dialog'
import { Badge, Button, Confirmation, Empty, ErrorNotice, Loading, Modal } from '../components/ui'
import { useDirty, useToast, usePending } from '../components/providers'

export function RulesPanel({
  configKey,
  state,
  onStateChanged,
  onEditRule,
}: {
  configKey: ConfigKey
  state: ConfigState
  onStateChanged: (state: ConfigState) => void
  onEditRule: (id: string) => void
}) {
  const toast = useToast()
  const client = useQueryClient()
  const versions = useQuery({
    queryKey: ['versions', configKey],
    queryFn: ({ signal }) => allVersions(configKey, signal),
  })
  const [editing, setEditing] = useState<GrayRule>()
  const [initialEdit, setInitialEdit] = useState('')
  const [pending, setPending] = useState<{ title: string; rules: GrayRule[] }>()
  const [checked, setChecked] = useState(false)
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const [action, setAction] = useState<ChangeAction>()
  const [promoting, setPromoting] = useState<string>()
  usePending('正在更新灰度规则', busy)
  useDirty(
    '灰度规则的未保存编辑',
    !!pending || (!!editing && JSON.stringify(editing) !== initialEdit),
  )
  const reviewBaseline = useRef(state)
  const [blocked, setBlocked] = useState(false)
  function startEdit(rule?: GrayRule) {
    const value = rule
      ? structuredClone(rule)
      : {
          id: ruleId(),
          name: '',
          enabled: true,
          target_version: state.global_version,
          conditions: [{ tag: '', operator: 'eq' as const, values: [''] }],
        }
    setEditing(value)
    setInitialEdit(JSON.stringify(value))
    setError(undefined)
  }
  function review(title: string, rules: GrayRule[]) {
    const invalid = ruleError(rules)
    if (invalid) {
      setError(invalid)
      return
    }
    setBlocked(false)
    reviewBaseline.current = state
    setPending({ title, rules })
    setChecked(false)
    setError(undefined)
  }
  function reviewEdit() {
    if (!editing) return
    const existing = state.rules.some((rule) => rule.id === editing.id)
    const next = existing
      ? state.rules.map((rule) => (rule.id === editing.id ? editing : rule))
      : [...state.rules, editing]
    review(existing ? '确认修改灰度规则' : '确认新增灰度规则', next)
  }
  async function commit() {
    if (!pending || !checked || busy || blocked) return
    setBusy(true)
    setError(undefined)
    try {
      const mutation = await api.rules(configKey, baselineOf(reviewBaseline.current), pending.rules)
      onStateChanged(mutation.state)
      client.setQueryData(configQueryKey(configKey), mutation.state)
      await client.invalidateQueries({ queryKey: ['versions', configKey] })
      toast(mutation.changed ? '灰度规则已更新并立即生效。' : '规则未发生变化。')
      setPending(undefined)
      setEditing(undefined)
    } catch (e) {
      setError(e)
      setChecked(false)
    } finally {
      setBusy(false)
    }
  }
  async function conflictReload() {
    setBusy(true)
    try {
      const latest = await api.state(configKey)
      if (latest.id !== reviewBaseline.current.id) {
        setBlocked(true)
        setError('原配置已删除或重建，请保留当前规则草稿并重新打开配置。')
        return
      }
      onStateChanged(latest)
      reviewBaseline.current = latest
      setChecked(false)
      setError('已读取最新规则，草稿仍保留。请核对新旧顺序与条件，再次确认。')
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  async function promote(rule: GrayRule) {
    setPromoting(rule.id)
    setError(undefined)
    try {
      const source = await api.version(configKey, rule.target_version)
      setAction({ kind: 'promote', source })
    } catch (e) {
      setError(e)
    } finally {
      setPromoting(undefined)
    }
  }
  function move(index: number, offset: number) {
    const next = [...state.rules]
    ;[next[index], next[index + offset]] = [next[index + offset], next[index]]
    review('确认调整匹配顺序', next)
  }
  return (
    <>
      <section className="panel">
        <div className="panel-toolbar">
          <div>
            <div className="panel-title">
              <GitBranch size={18} aria-hidden="true" />
              灰度规则 <Badge>{state.rules.length}</Badge>
            </div>
            <p className="section-help">
              从上到下匹配，第一条命中生效；未命中使用全量 v{state.global_version}。
            </p>
          </div>
          <Button
            variant="primary"
            disabled={state.rules.length >= 100}
            onClick={() => startEdit()}
          >
            <Plus size={16} aria-hidden="true" />
            新增规则
          </Button>
        </div>
        <ErrorNotice error={!pending && !editing ? error : undefined} />
        {state.rules.length ? (
          <div className="rule-list">
            {state.rules.map((rule, index) => (
              <article className={`rule-card ${rule.enabled ? '' : 'disabled-rule'}`} key={rule.id}>
                <div className="rule-order">
                  <span>{String(index + 1).padStart(2, '0')}</span>
                  <div>
                    <Button
                      variant="ghost"
                      className="icon-button"
                      aria-label={`上移规则 ${rule.name || rule.id}`}
                      disabled={index === 0}
                      onClick={() => move(index, -1)}
                    >
                      <ArrowUp size={15} aria-hidden="true" />
                    </Button>
                    <Button
                      variant="ghost"
                      className="icon-button"
                      aria-label={`下移规则 ${rule.name || rule.id}`}
                      disabled={index === state.rules.length - 1}
                      onClick={() => move(index, 1)}
                    >
                      <ArrowDown size={15} aria-hidden="true" />
                    </Button>
                  </div>
                </div>
                <div className="rule-main">
                  <div className="rule-title">
                    <h3>{rule.name || '未命名规则'}</h3>
                    <Badge tone={rule.enabled ? 'success' : 'neutral'}>
                      {rule.enabled ? '启用' : '停用'}
                    </Badge>
                    <Badge>固定 v{rule.target_version}</Badge>
                  </div>
                  <RuleConditions rule={rule} />
                  <div className="rule-content-actions">
                    <Button variant="ghost" onClick={() => onEditRule(rule.id)}>
                      <Pencil size={14} aria-hidden="true" />
                      编辑此规则内容
                    </Button>
                    <Button
                      variant="ghost"
                      busy={promoting === rule.id}
                      onClick={() => promote(rule)}
                    >
                      转为全量
                    </Button>
                  </div>
                </div>
                <div className="rule-actions">
                  <Button
                    variant="ghost"
                    className="icon-button"
                    aria-label={`修改规则 ${rule.name || rule.id}`}
                    onClick={() => startEdit(rule)}
                  >
                    <Pencil size={17} aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    className="icon-button"
                    aria-label={`${rule.enabled ? '停用' : '启用'}规则 ${rule.name || rule.id}`}
                    onClick={() =>
                      review(
                        rule.enabled ? '确认停用灰度规则' : '确认启用灰度规则',
                        state.rules.map((r) =>
                          r.id === rule.id ? { ...r, enabled: !r.enabled } : r,
                        ),
                      )
                    }
                  >
                    <Power size={17} aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    className="icon-button destructive-text"
                    aria-label={`删除规则 ${rule.name || rule.id}`}
                    onClick={() =>
                      review(
                        '确认删除灰度规则',
                        state.rules.filter((r) => r.id !== rule.id),
                      )
                    }
                  >
                    <Trash2 size={17} aria-hidden="true" />
                  </Button>
                </div>
              </article>
            ))}
          </div>
        ) : (
          <Empty
            title="当前使用全量发布"
            description="需要定向发布时，添加标签条件并绑定一个固定版本。"
          />
        )}
      </section>
      <Simulation configKey={configKey} state={state} />
      {editing && !pending && (
        <Modal
          open
          onClose={() => setEditing(undefined)}
          title={state.rules.some((r) => r.id === editing.id) ? '修改灰度规则' : '新增灰度规则'}
          description="一条规则内的条件全部满足时匹配；多个规则可表达不同客户端范围。"
          wide
          footer={
            <>
              <Button onClick={() => setEditing(undefined)}>取消</Button>
              <Button variant="primary" onClick={reviewEdit}>
                查看影响并确认
              </Button>
            </>
          }
        >
          <RuleForm
            rule={editing}
            onChange={setEditing}
            versions={versions.data?.map((v) => v.number) ?? [state.global_version]}
          />
          <ErrorNotice
            error={error || versions.error}
            onRetry={versions.error ? () => versions.refetch() : undefined}
          />
        </Modal>
      )}
      {pending && (
        <Modal
          open
          onClose={() => {
            setPending(undefined)
            if (!editing) setError(undefined)
          }}
          title={pending.title}
          description="确认后立即生效。客户端将按新的顺序与标签条件重新匹配有效版本。"
          wide
          busy={busy}
          footer={
            <>
              <Button onClick={() => setPending(undefined)} disabled={busy}>
                {editing ? '返回编辑' : '取消'}
              </Button>
              <Button
                variant="primary"
                onClick={commit}
                busy={busy}
                disabled={
                  !checked || blocked || (error instanceof ApiError && error.status === 409)
                }
              >
                确认生效
              </Button>
            </>
          }
        >
          <div className="notice info">
            全量仍为 v{state.global_version}
            。规则停用或移除后，会继续匹配后面的规则，全部未命中才使用全量。
          </div>
          <div className="rule-comparison">
            <div>
              <h3>原匹配顺序</h3>
              <RuleSummary rules={reviewBaseline.current.rules} />
            </div>
            <div>
              <h3>新匹配顺序</h3>
              <RuleSummary rules={pending.rules} />
            </div>
          </div>
          <ErrorNotice error={error} />
          {error instanceof ApiError && error.status === 409 && (
            <Button onClick={conflictReload} busy={busy}>
              读取最新规则并保留当前编辑
            </Button>
          )}
          <Confirmation
            checked={checked}
            onChange={setChecked}
            label="我已核对标签条件、顺序与影响范围"
          />
        </Modal>
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
          onRebase={onStateChanged}
        />
      )}
    </>
  )
}
function RuleConditions({ rule }: { rule: GrayRule }) {
  return (
    <div className="condition-tags">
      {rule.conditions.map((condition, index) => (
        <span key={index}>
          <code>{condition.tag}</code>
          <span>{condition.operator === 'eq' ? '＝' : '属于'}</span>
          <strong>{condition.values.map((v) => (v === '' ? '空字符串' : v)).join(' / ')}</strong>
          {index < rule.conditions.length - 1 && <small>AND</small>}
        </span>
      ))}
    </div>
  )
}
function RuleSummary({ rules }: { rules: GrayRule[] }) {
  return rules.length ? (
    <ol className="rule-summary">
      {rules.map((rule) => (
        <li key={rule.id}>
          <div>
            <strong>{rule.name || '未命名规则'}</strong>
            <Badge>v{rule.target_version}</Badge>
            <Badge tone={rule.enabled ? 'success' : 'neutral'}>
              {rule.enabled ? '启用' : '停用'}
            </Badge>
          </div>
          <RuleConditions rule={rule} />
        </li>
      ))}
    </ol>
  ) : (
    <p className="muted">没有灰度规则，全部使用全量版本。</p>
  )
}
function RuleForm({
  rule,
  onChange,
  versions,
}: {
  rule: GrayRule
  onChange: (rule: GrayRule) => void
  versions: number[]
}) {
  const updateCondition = (index: number, next: Condition) =>
    onChange({ ...rule, conditions: rule.conditions.map((c, i) => (i === index ? next : c)) })
  return (
    <div className="form-stack">
      <div className="form-grid">
        <label>
          规则名称
          <input
            value={rule.name}
            placeholder="例如 华东预发布"
            onChange={(e) => onChange({ ...rule, name: e.target.value })}
          />
        </label>
        <label>
          固定版本
          <select
            aria-label="固定版本"
            value={rule.target_version}
            onChange={(e) => onChange({ ...rule, target_version: Number(e.target.value) })}
          >
            {[...new Set([rule.target_version, ...versions])]
              .sort((a, b) => b - a)
              .map((v) => (
                <option key={v} value={v}>
                  v{v}
                </option>
              ))}
          </select>
        </label>
      </div>
      <label className="check-label">
        <input
          type="checkbox"
          checked={rule.enabled}
          onChange={(e) => onChange({ ...rule, enabled: e.target.checked })}
        />
        启用这条规则
      </label>
      <fieldset>
        <legend>
          标签条件 <span className="muted">全部满足（AND）</span>
        </legend>
        <div className="rule-form-conditions">
          {rule.conditions.map((condition, index) => (
            <div className="condition-form" key={index}>
              <div className="condition-heading">
                <strong>条件 {index + 1}</strong>
                <Button
                  variant="ghost"
                  className="icon-button"
                  aria-label={`删除条件 ${index + 1}`}
                  disabled={rule.conditions.length === 1}
                  onClick={() =>
                    onChange({ ...rule, conditions: rule.conditions.filter((_, i) => i !== index) })
                  }
                >
                  <X size={16} aria-hidden="true" />
                </Button>
              </div>
              <div className="form-grid">
                <label>
                  标签名称
                  <input
                    aria-label={`条件 ${index + 1} 标签名称`}
                    value={condition.tag}
                    placeholder="例如 sys.hostname 或 env"
                    onChange={(e) => updateCondition(index, { ...condition, tag: e.target.value })}
                  />
                </label>
                <label>
                  匹配方式
                  <select
                    aria-label={`条件 ${index + 1} 匹配方式`}
                    value={condition.operator}
                    onChange={(e) =>
                      updateCondition(index, {
                        ...condition,
                        operator: e.target.value as Condition['operator'],
                        values:
                          e.target.value === 'eq' ? [condition.values[0] ?? ''] : condition.values,
                      })
                    }
                  >
                    <option value="eq">等于</option>
                    <option value="in">属于指定值集合</option>
                  </select>
                </label>
              </div>
              <div className="condition-values">
                {condition.values.map((value, valueIndex) => (
                  <div className="tag-value-row" key={valueIndex}>
                    <label>
                      标签值{condition.values.length > 1 ? ` ${valueIndex + 1}` : ''}
                      <input
                        aria-label={`条件 ${index + 1} 标签值 ${valueIndex + 1}`}
                        value={value}
                        placeholder="例如 gray；空字符串也可作为值"
                        onChange={(e) =>
                          updateCondition(index, {
                            ...condition,
                            values: condition.values.map((v, i) =>
                              i === valueIndex ? e.target.value : v,
                            ),
                          })
                        }
                      />
                    </label>
                    {condition.operator === 'in' && (
                      <Button
                        variant="ghost"
                        className="icon-button"
                        aria-label={`删除条件 ${index + 1} 的标签值 ${valueIndex + 1}`}
                        disabled={condition.values.length === 1}
                        onClick={() =>
                          updateCondition(index, {
                            ...condition,
                            values: condition.values.filter((_, i) => i !== valueIndex),
                          })
                        }
                      >
                        <X size={16} aria-hidden="true" />
                      </Button>
                    )}
                  </div>
                ))}
                {condition.operator === 'in' && (
                  <Button
                    disabled={condition.values.length >= 100}
                    onClick={() =>
                      updateCondition(index, { ...condition, values: [...condition.values, ''] })
                    }
                  >
                    <Plus size={15} aria-hidden="true" />
                    添加标签值
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
        <Button
          disabled={rule.conditions.length >= 32}
          onClick={() =>
            onChange({
              ...rule,
              conditions: [...rule.conditions, { tag: '', operator: 'eq', values: [''] }],
            })
          }
        >
          <Plus size={16} aria-hidden="true" />
          添加 AND 条件
        </Button>
      </fieldset>
    </div>
  )
}
function Simulation({ configKey, state }: { configKey: ConfigKey; state: ConfigState }) {
  const [tags, setTags] = useState([{ name: 'sys.hostname', value: '' }])
  const [result, setResult] = useState<Effective>()
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const generation = useRef(0)
  useEffect(() => {
    generation.current++
    setResult(undefined)
  }, [state.revision])
  function change(next: typeof tags) {
    generation.current++
    setTags(next)
    setResult(undefined)
    setError(undefined)
  }
  async function run() {
    const data: Record<string, string> = {}
    for (const tag of tags) {
      if (!tag.name || Object.hasOwn(data, tag.name)) {
        setError('标签名称不能为空或重复。')
        return
      }
      data[tag.name] = tag.value
    }
    const current = ++generation.current
    setBusy(true)
    setError(undefined)
    try {
      const value = await api.simulate(configKey, data)
      if (current === generation.current) setResult(value)
    } catch (e) {
      if (current === generation.current) setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="panel simulation-panel">
      <div className="panel-toolbar">
        <div>
          <div className="panel-title">
            <FlaskConical size={18} aria-hidden="true" />
            标签试算
          </div>
          <p className="section-help">输入客户端标签，查看它会使用哪个版本。不会发布任何变更。</p>
        </div>
      </div>
      <div className="simulation-body">
        <div className="simulation-inputs">
          {tags.map((tag, index) => (
            <div className="simulation-row" key={index}>
              <label>
                标签名称
                <input
                  aria-label={`试算标签 ${index + 1} 名称`}
                  value={tag.name}
                  onChange={(e) =>
                    change(tags.map((t, i) => (i === index ? { ...t, name: e.target.value } : t)))
                  }
                />
              </label>
              <label>
                标签值
                <input
                  aria-label={`试算标签 ${index + 1} 值`}
                  placeholder="例如 node-a"
                  value={tag.value}
                  onChange={(e) =>
                    change(tags.map((t, i) => (i === index ? { ...t, value: e.target.value } : t)))
                  }
                />
              </label>
              <Button
                variant="ghost"
                className="icon-button"
                aria-label={`删除试算标签 ${index + 1}`}
                disabled={tags.length === 1}
                onClick={() => change(tags.filter((_, i) => i !== index))}
              >
                <X size={16} aria-hidden="true" />
              </Button>
            </div>
          ))}
          <div className="button-row">
            <Button
              disabled={tags.length >= 64}
              onClick={() => change([...tags, { name: '', value: '' }])}
            >
              <Plus size={16} aria-hidden="true" />
              添加标签
            </Button>
            <Button variant="primary" busy={busy} onClick={run}>
              <FlaskConical size={16} aria-hidden="true" />
              开始试算
            </Button>
          </div>
          <ErrorNotice error={error} />
        </div>
        <div className="simulation-result" aria-live="polite">
          {result ? (
            <>
              <span className="eyebrow">EFFECTIVE VERSION</span>
              <strong className="simulation-version">v{result.version}</strong>
              <Badge tone={result.rule_id ? 'warning' : 'success'}>
                {result.rule_id ? '命中灰度规则' : '使用全量配置'}
              </Badge>
              <p>
                {result.rule_id
                  ? state.rules.find((r) => r.id === result.rule_id)?.name || result.rule_id
                  : '未命中任何启用的灰度规则'}
              </p>
            </>
          ) : (
            <>
              <FlaskConical size={28} aria-hidden="true" />
              <p>试算结果将显示在这里</p>
            </>
          )}
        </div>
      </div>
    </section>
  )
}
