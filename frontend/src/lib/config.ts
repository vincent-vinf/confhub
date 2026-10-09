import type { ConfigState, Draft, Edit, GrayRule, Version } from './types'

export const bytes = (value: string) => new TextEncoder().encode(value).byteLength
export function nameError(value: string): string | undefined {
  if (!value || bytes(value) > 128 || value.trim() !== value || /[/\\\0\r\n]/.test(value))
    return '请输入 1–128 字节的名称，不含斜杠或首尾空白。'
}
export function targetVersion(state: ConfigState, ruleId?: string): Version {
  const target = ruleId
    ? state.rules.find((rule) => rule.id === ruleId)?.target_version
    : state.global_version
  if (!target || !state.versions[target])
    throw new Error('编辑目标已不存在，请保留草稿并重新打开配置。')
  return state.versions[target]
}
export const baselineOf = (state?: ConfigState) => ({
  expected_id: state?.id ?? '',
  expected_revision: state?.revision ?? 0,
})
export function editPayload(state: ConfigState | undefined, draft: Draft): Edit {
  return {
    ...baselineOf(state),
    content: draft.content,
    format: draft.format,
    description: draft.description,
    rule_id: draft.ruleId,
    confirmed: true,
  }
}
export function publicationImpact(state: ConfigState | undefined, ruleId?: string) {
  if (!state) return '创建第一份配置并全量发布。'
  if (ruleId) {
    const rule = state.rules.find((r) => r.id === ruleId)
    return `仅更新灰度规则「${rule?.name || ruleId}」的目标版本。全量 v${state.global_version} 与其他规则保持原目标。`
  }
  return `全量版本将更新，未命中灰度规则的客户端使用新内容。${state.rules.length ? `${state.rules.length} 条灰度规则继续使用各自固定版本。` : '当前没有灰度规则。'}`
}
export function draftFrom(state: ConfigState, ruleId?: string): Draft {
  const version = targetVersion(state, ruleId)
  return { content: version.content, format: version.format, description: '', ruleId }
}
export function ruleError(rules: GrayRule[]) {
  if (rules.length > 100) return '最多可维护 100 条灰度规则。'
  const ids = new Set<string>()
  for (const rule of rules) {
    if (!rule.id || ids.has(rule.id) || rule.id.length > 36) return '规则 ID 必须唯一。'
    ids.add(rule.id)
    if (bytes(rule.name) > 128) return '规则名称不能超过 128 字节。'
    if (!Number.isSafeInteger(rule.target_version) || rule.target_version < 1)
      return '请选择有效的历史版本。'
    if (!rule.conditions.length || rule.conditions.length > 32)
      return '每条规则应包含 1–32 个条件。'
    for (const condition of rule.conditions) {
      if (!condition.tag || bytes(condition.tag) > 128) return '请填写标签名称，最多 128 字节。'
      if (!condition.values.length || condition.values.some((v) => bytes(v) > 512))
        return '请填写标签值，每个值最多 512 字节。'
      if (condition.values.length > 100) return '每个条件最多 100 个标签值。'
      if (condition.operator === 'eq' && condition.values.length !== 1)
        return '等于条件只能有一个标签值。'
    }
  }
}
export const actionNames: Record<string, string> = {
  save: '全量保存',
  gray_save: '灰度保存',
  rollback: '全量回退',
  gray_rollback: '灰度回退',
  promote: '转为全量',
}
export const formatDate = (iso: string) =>
  new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(iso))
export function ruleId() {
  if (globalThis.crypto.randomUUID) return globalThis.crypto.randomUUID()
  const data = new Uint8Array(12)
  globalThis.crypto.getRandomValues(data)
  return `r-${Array.from(data, (b) => b.toString(16).padStart(2, '0')).join('')}`
}
