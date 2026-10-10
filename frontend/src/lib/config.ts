import type { ConfigState, Draft, Edit, GrayRule, Version } from './types'

export const bytes = (value: string) => new TextEncoder().encode(value).byteLength
export function nameError(value: string): string | undefined {
  if (!value || bytes(value) > 128 || value.trim() !== value || /[/\\\0\r\n]/.test(value))
    return '请输入 1–128 字节的名称，不含斜杠或首尾空白。'
}
export type EditorTarget = Pick<Version, 'number' | 'content' | 'format' | 'description'>
export function targetVersion(state: ConfigState, target?: 'global' | 'beta'): EditorTarget {
  if (target === 'beta') {
    if (!state.beta) throw new Error('beta 配置已不存在，请保留草稿并重新打开配置。')
    return { ...state.beta, number: 0 }
  }
  const version = state.versions[state.global_version]
  if (!version) throw new Error('编辑目标已不存在，请保留草稿并重新打开配置。')
  return version
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
    target: draft.target,
    confirmed: true,
  }
}
export function publicationImpact(state: ConfigState | undefined, target?: 'global' | 'beta') {
  if (!state) return '创建第一份配置并全量发布。'
  if (target === 'beta')
    return `更新唯一 beta 配置，所有命中灰度规则的客户端立即使用新内容。全量 v${state.global_version} 保持不变，不产生主版本或 beta 历史。`
  return `全量版本将更新，未命中灰度规则的客户端使用新内容。${state.beta ? 'beta 配置保持不变。' : '当前没有 beta 配置。'}`
}
export function draftFrom(state: ConfigState, target?: 'global' | 'beta'): Draft {
  const version = targetVersion(state, target)
  return {
    content: version.content,
    format: version.format,
    description: target === 'beta' ? version.description : '',
    target,
  }
}

export function ruleError(rules: GrayRule[]) {
  if (rules.length > 100) return '最多可维护 100 条灰度规则。'
  const ids = new Set<string>()
  for (const rule of rules) {
    if (!rule.id || ids.has(rule.id) || rule.id.length > 36) return '规则 ID 必须唯一。'
    ids.add(rule.id)
    if (bytes(rule.name) > 128) return '规则名称不能超过 128 字节。'
    if (!rule.conditions.length || rule.conditions.length > 32)
      return '每条规则应包含 1–32 个条件。'
    for (const condition of rule.conditions) {
      if (!condition.tag || bytes(condition.tag) > 128) return '请填写标签名称，最多 128 字节。'
      if (!condition.values.length || condition.values.some((v) => bytes(v) > 512))
        return '请填写标签值，每个值最多 512 字节。'
      if (condition.values.length > 100) return '每个条件最多 100 个标签值。'
      if (condition.operator === 'ip_range') {
        const start = ipAddress(condition.values[0] ?? '')
        const end = ipAddress(condition.values[1] ?? '')
        if (
          condition.values.length !== 2 ||
          !start ||
          !end ||
          start.family !== end.family ||
          start.number > end.number
        )
          return 'IP 区间必须填写两个完整的同族地址，起始 IP 不得大于结束 IP。'
      }
      if (condition.operator === 'eq' && condition.values.length !== 1)
        return '等于条件只能有一个标签值。'
    }
  }
}
export const actionNames: Record<string, string> = {
  save: '全量保存',
  rollback: '全量回退',
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

// Convert strict IPv4 or browser-canonicalized IPv6 to numeric address order.
function ipAddress(value: string): { family: number; number: bigint } | undefined {
  if (!value.includes(':')) {
    const parts = value.split('.')
    if (
      parts.length !== 4 ||
      parts.some((part) => !/^(0|[1-9]\d{0,2})$/.test(part) || Number(part) > 255)
    )
      return
    return { family: 4, number: parts.reduce((number, part) => (number << 8n) | BigInt(part), 0n) }
  }
  if (!/^[a-fA-F0-9:.]+$/.test(value)) return
  if (value.includes('.') && !ipAddress(value.slice(value.lastIndexOf(':') + 1))) return
  try {
    const canonical = new URL(`http://[${value}]/`).hostname.slice(1, -1)
    const [left, right] = canonical.split('::')
    const a = left ? left.split(':') : []
    const b = right ? right.split(':') : []
    const groups = canonical.includes('::')
      ? [...a, ...Array(8 - a.length - b.length).fill('0'), ...b]
      : a
    const number = groups.reduce((number, group) => (number << 16n) | BigInt(`0x${group}`), 0n)
    return number >> 32n === 0xffffn
      ? { family: 4, number: number & 0xffffffffn }
      : { family: 6, number }
  } catch {
    return
  }
}
