import { Select } from '../components/select'
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
import { baselineOf, ruleError, ruleId, targetVersion } from '../lib/config'
import type { Condition, ConfigKey, ConfigState, Effective, GrayRule } from '../lib/types'
import { TagInput } from '../components/tag-input'
import { ChangeDialog, type ChangeAction } from '../components/change-dialog'
import { Badge, Button, Confirmation, Empty, ErrorNotice, Modal } from '../components/ui'
import { useDirty, useToast, usePending } from '../components/providers'

export function RulesPanel({
  configKey,
  state,
  onStateChanged,
  onEditBeta,
}: {
  configKey: ConfigKey
  state: ConfigState
  onStateChanged: (state: ConfigState) => void
  onEditBeta: () => void
}) {
  const toast = useToast()
  const client = useQueryClient()
  const [editing, setEditing] = useState<GrayRule>()
  const [initialEdit, setInitialEdit] = useState('')
  const [sourceVersion, setSourceVersion] = useState(0)
  const [initialSource, setInitialSource] = useState(0)
  const versions = useQuery({
    queryKey: ['versions', configKey],
    queryFn: ({ signal }) => allVersions(configKey, signal),
    enabled: !!editing && !state.beta,
  })
  const [pending, setPending] = useState<{
    title: string
    rules: GrayRule[]
    sourceVersion?: number
  }>()
  const [checked, setChecked] = useState(false)
  const [error, setError] = useState<unknown>()
  const [busy, setBusy] = useState(false)
  const [action, setAction] = useState<ChangeAction>()
  usePending('正在更新灰度规则', busy)
  useDirty(
    '灰度规则的未保存编辑',
    !!pending ||
      (!!editing && (JSON.stringify(editing) !== initialEdit || sourceVersion !== initialSource)),
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
          conditions: [{ tag: '', operator: 'eq' as const, values: [''] }],
        }
    setSourceVersion(state.beta ? 0 : state.global_version)
    setInitialSource(state.beta ? 0 : state.global_version)
    setEditing(value)
    setInitialEdit(JSON.stringify(value))
    setError(undefined)
  }
  function review(title: string, rules: GrayRule[], source?: number) {
    const invalid = ruleError(rules)
    if (invalid) {
      setError(invalid)
      return
    }
    setBlocked(false)
    reviewBaseline.current = state
    setPending({ title, rules, sourceVersion: source })
    setChecked(false)
    setError(undefined)
  }
  function reviewEdit() {
    if (!editing) return
    const existing = state.rules.some((rule) => rule.id === editing.id)
    const next = existing
      ? state.rules.map((rule) => (rule.id === editing.id ? editing : rule))
      : [...state.rules, editing]
    if (
      !state.beta &&
      (!sourceVersion || !versions.data?.some((v) => v.number === sourceVersion))
    ) {
      setError('请选择仍可用的主版本作为 beta 复制来源。')
      return
    }
    review(
      existing ? '确认修改灰度规则' : '确认新增灰度规则',
      next,
      state.beta ? undefined : sourceVersion,
    )
  }
  async function commit() {
    if (!pending || !checked || busy || blocked) return
    setBusy(true)
    setError(undefined)
    try {
      const mutation = await api.rules(
        configKey,
        baselineOf(reviewBaseline.current),
        pending.rules,
        pending.sourceVersion,
      )
      onStateChanged(mutation.state)
      client.setQueryData(configQueryKey(configKey), mutation.state)
      await client.invalidateQueries({ queryKey: ['versions', configKey] })
      toast(mutation.changed ? '灰度规则已更新并立即生效。' : '规则未发生变化。')
      setPending(undefined)
      setEditing(undefined)
    } catch (e) {
      if (e instanceof ApiError && e.status === 404 && pending.sourceVersion && editing) {
        await client.invalidateQueries({ queryKey: ['versions', configKey] })
        setPending(undefined)
        setError('复制来源已不可用，请重新选择仍保留的主版本。')
      } else {
        setError(e)
      }
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
      if (!latest.beta && pending?.rules.length && !pending.sourceVersion) {
        onStateChanged(latest)
        reviewBaseline.current = latest
        setPending(undefined)
        setChecked(false)
        if (editing) {
          setSourceVersion(0)
          setInitialSource(0)
          setError('beta 已被删除，规则草稿仍保留。请重新选择主版本作为复制来源。')
        } else {
          setError('beta 已被删除，原规则操作无法继续。请重新新增规则并选择主版本复制。')
        }
        return
      }
      if (!latest.beta && pending?.sourceVersion) {
        const available = await allVersions(configKey)
        client.setQueryData(['versions', configKey], available)
        if (!available.some((version) => version.number === pending.sourceVersion)) {
          onStateChanged(latest)
          reviewBaseline.current = latest
          setPending(undefined)
          setChecked(false)
          setError('原复制来源已被清理，草稿仍保留。请重新选择主版本并确认。')
          return
        }
      }
      // Preserve the explicitly selected source when only the global changes.
      // If another admin created beta, this list now routes to that shared beta.
      setPending((value) =>
        value ? { ...value, sourceVersion: latest.beta ? undefined : value.sourceVersion } : value,
      )
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
  function promote() {
    setAction({ kind: 'promote', source: targetVersion(state, 'beta') })
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
                  </div>
                  <RuleConditions rule={rule} />
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
                    className={`icon-button ${rule.enabled ? 'destructive-text' : 'success-text'}`}
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
          <Empty title="当前使用全量发布" />
        )}
      </section>
      {state.beta && (
        <section className="panel">
          <div className="panel-toolbar">
            <div>
              <div className="panel-title">
                <Badge>beta</Badge>灰度配置
              </div>
            </div>
            <div className="button-row">
              <Button onClick={onEditBeta}>
                <Pencil size={15} aria-hidden="true" />
                编辑配置
              </Button>
              <Button onClick={promote}>转为全量</Button>
            </div>
          </div>
        </section>
      )}
      <Simulation configKey={configKey} state={state} />
      {editing && !pending && (
        <Modal
          open
          onClose={() => setEditing(undefined)}
          title={state.rules.some((r) => r.id === editing.id) ? '修改灰度规则' : '新增灰度规则'}
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
          {!state.beta && (
            <label>
              beta 复制来源
              <Select
                aria-label="beta 复制来源"
                value={sourceVersion}
                onValueChange={(value) => setSourceVersion(Number(value))}
                disabled={versions.isPending}
              >
                <option value={0}>请选择主版本</option>
                {versions.data?.map((version) => (
                  <option key={version.number} value={version.number}>
                    v{version.number}
                    {version.number === state.global_version ? ' · 当前全量' : ''}
                    {version.description ? ` · ${version.description}` : ''}
                  </option>
                ))}
              </Select>
            </label>
          )}
          {!state.beta && <ErrorNotice error={versions.error} onRetry={() => versions.refetch()} />}
          <RuleForm rule={editing} onChange={setEditing} />
          <ErrorNotice error={error} />
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
            {pending.rules.length === 0 && state.beta
              ? '删除最后一条规则将同时删除 beta 配置。'
              : `全量 v${state.global_version} 保持不变。`}
          </div>
          {pending.sourceVersion && (
            <div className="notice info">从主版本 v{pending.sourceVersion} 复制为 beta 配置。</div>
          )}
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
          <span>
            {condition.operator === 'eq'
              ? '＝'
              : condition.operator === 'ip_range'
                ? 'IP 区间'
                : '属于'}
          </span>
          <strong>
            {condition.values
              .map((v) => (v === '' ? '空字符串' : v))
              .join(condition.operator === 'ip_range' ? ' → ' : ' / ')}
          </strong>
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
            <Badge tone={rule.enabled ? 'success' : 'neutral'}>
              {rule.enabled ? '启用' : '停用'}
            </Badge>
          </div>
          <RuleConditions rule={rule} />
        </li>
      ))}
    </ol>
  ) : (
    <p className="muted">没有灰度规则。</p>
  )
}
function RuleForm({ rule, onChange }: { rule: GrayRule; onChange: (rule: GrayRule) => void }) {
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
                  <TagInput
                    label={`条件 ${index + 1} 标签名称`}
                    value={condition.tag}
                    placeholder="选择在线标签或自由填写"
                    onChange={(tag) => updateCondition(index, { ...condition, tag })}
                  />
                </label>
                <label>
                  匹配方式
                  <Select
                    aria-label={`条件 ${index + 1} 匹配方式`}
                    value={condition.operator}
                    onValueChange={(value) =>
                      updateCondition(index, {
                        ...condition,
                        operator: value as Condition['operator'],
                        values:
                          value === 'eq'
                            ? [condition.values[0] ?? '']
                            : value === 'ip_range'
                              ? [condition.values[0] ?? '', condition.values[1] ?? '']
                              : condition.values,
                        tag: !condition.tag && value === 'ip_range' ? 'sys.ip' : condition.tag,
                      })
                    }
                  >
                    <option value="eq">等于</option>
                    <option value="in">属于指定值集合</option>
                    <option value="ip_range">IP 区间（含起止地址）</option>
                  </Select>
                </label>
              </div>
              <div className="condition-values">
                {condition.values.map((value, valueIndex) => (
                  <div className="tag-value-row" key={valueIndex}>
                    <label>
                      {condition.operator === 'ip_range'
                        ? valueIndex === 0
                          ? '起始 IP'
                          : '结束 IP'
                        : `标签值${condition.values.length > 1 ? ` ${valueIndex + 1}` : ''}`}
                      <TagInput
                        label={
                          condition.operator === 'ip_range'
                            ? `条件 ${index + 1} ${valueIndex === 0 ? '起始 IP' : '结束 IP'}`
                            : `条件 ${index + 1} 标签值 ${valueIndex + 1}`
                        }
                        tag={condition.tag}
                        value={value}
                        placeholder={
                          condition.operator === 'ip_range'
                            ? valueIndex === 0
                              ? '192.168.2.1'
                              : '192.168.2.5'
                            : '选择在线值或自由填写（可为空）'
                        }
                        onChange={(nextValue) =>
                          updateCondition(index, {
                            ...condition,
                            values: condition.values.map((v, i) =>
                              i === valueIndex ? nextValue : v,
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
              <span className="eyebrow">匹配结果</span>
              <strong className="simulation-version">
                {result.beta ? 'beta' : `v${result.version}`}
              </strong>
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
              <p>暂无试算结果</p>
            </>
          )}
        </div>
      </div>
    </section>
  )
}
