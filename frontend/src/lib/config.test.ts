import { describe, expect, it } from 'vitest'
import {
  baselineOf,
  draftFrom,
  editPayload,
  nameError,
  publicationImpact,
  ruleError,
} from './config'
import type { ConfigState, GrayRule } from './types'

export const rule: GrayRule = {
  id: 'gray',
  name: '预发布',
  enabled: true,
  conditions: [{ tag: 'env', operator: 'eq', values: ['gray'] }],
}
export const state: ConfigState = {
  id: 'original',
  key: { namespace: 'public', group: 'DEFAULT_GROUP', name: 'app.json' },
  revision: 7,
  last_version: 2,
  global_version: 2,
  sequence: 9,
  rules: [rule],
  beta: { content: '{"v":1}', format: 'json', description: '', updated_at: '2026-10-10T00:00:00Z' },
  versions: {
    1: {
      number: 1,
      content: '{"v":1}',
      format: 'json',
      description: '旧版本',
      action: 'save',
      created_at: '2026-10-09T10:00:00Z',
    },
    2: {
      number: 2,
      content: '{"v":2}',
      format: 'json',
      description: '',
      action: 'save',
      created_at: '2026-10-09T10:01:00Z',
    },
  },
}

describe('发布边界', () => {
  it('全量与灰度分别从自己的内容编辑，全量说明不继承', () => {
    expect(draftFrom(state)).toEqual({
      content: '{"v":2}',
      format: 'json',
      description: '',
      target: undefined,
    })
    expect(draftFrom(state, 'beta').content).toBe('{"v":1}')
    expect(() => draftFrom({ ...state, beta: undefined }, 'beta')).toThrow('beta 配置')
  })
  it('beta 直接读取唯一正文，不依赖主历史，重新编辑保留灰度说明', () => {
    const edited = structuredClone(state)
    edited.beta = {
      content: '{"beta":"updated"}',
      format: 'json',
      description: '灰度说明',
      updated_at: '2026-10-10T00:00:00Z',
    }
    delete edited.versions[1]
    expect(draftFrom(edited, 'beta')).toEqual({
      content: '{"beta":"updated"}',
      format: 'json',
      description: '灰度说明',
      target: 'beta',
    })
  })
  it('乐观锁携带配置身份与修订，而非比较版本', () => {
    expect(editPayload(state, { ...draftFrom(state, 'beta'), content: 'new' })).toEqual({
      expected_id: 'original',
      expected_revision: 7,
      content: 'new',
      format: 'json',
      description: '',
      target: 'beta',
      confirmed: true,
    })
    expect(baselineOf()).toEqual({ expected_id: '', expected_revision: 0 })
  })
  it('发布影响明确说明固定灰度和全量边界', () => {
    expect(publicationImpact(state)).toContain('beta 配置保持不变')
    expect(publicationImpact(state, 'beta')).toContain('全量 v2')
  })
})
describe('输入约束', () => {
  it('名称按 UTF-8 字节限制并拒绝路径分隔', () => {
    expect(nameError('中文配置')).toBeUndefined()
    for (const name of ['', ' spaced', 'a/b', 'a\\b', '中'.repeat(43)])
      expect(nameError(name)).toBeTruthy()
  })
  it('允许空标签值，拒绝重复规则与错误集合', () => {
    expect(
      ruleError([{ ...rule, conditions: [{ tag: 'env', operator: 'in', values: ['', 'gray'] }] }]),
    ).toBeUndefined()
    expect(ruleError([rule, rule])).toContain('唯一')
    expect(
      ruleError([{ ...rule, conditions: [{ tag: 'env', operator: 'eq', values: ['a', 'b'] }] }]),
    ).toContain('只能有一个')
  })
  it.each([
    ['192.168.2.1', '192.168.2.5'],
    ['192.168.2.5', '192.168.2.5'],
    ['2001:db8::1', '2001:db8::ffff'],
    ['::', 'ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff'],
    ['::ffff:192.168.2.1', '192.168.2.5'],
    ['192.168.2.1', '::ffff:c0a8:205'],
  ])('接受完整且按数值排序的 IP 区间 %s → %s', (start, end) => {
    expect(
      ruleError([
        { ...rule, conditions: [{ tag: 'sys.ip', operator: 'ip_range', values: [start, end] }] },
      ]),
    ).toBeUndefined()
  })
  it.each([
    ['192.168.2.10', '192.168.2.5'],
    ['2001:db8::10', '2001:db8::2'],
    ['192.168.2.1', '::1'],
    ['192.168.02.1', '192.168.2.5'],
    ['192.168.2.256', '192.168.2.5'],
    ['127.1', '127.0.0.5'],
    ['fe80::1%eth0', 'fe80::2%eth0'],
    [':::1', '::2'],
    ['2001:db8::zz', '2001:db8::ffff'],
    ['192.168.2.1:80', '192.168.2.5'],
    ['::ffff:192.168.02.1', '192.168.2.5'],
    ['192.168.2.1', ''],
  ])('拦截非法 IP 区间 %s → %s', (start, end) => {
    expect(
      ruleError([
        { ...rule, conditions: [{ tag: 'sys.ip', operator: 'ip_range', values: [start, end] }] },
      ]),
    ).toContain('IP 区间')
  })
  it('规则和条件数量及 UTF-8 字节上限在保存前检查', () => {
    expect(
      ruleError(Array.from({ length: 101 }, (_, i) => ({ ...rule, id: String(i) }))),
    ).toContain('100 条')
    expect(ruleError([{ ...rule, name: '中'.repeat(43) }])).toContain('128 字节')
    expect(ruleError([{ ...rule, conditions: [] }])).toContain('1–32')
    expect(
      ruleError([{ ...rule, conditions: Array.from({ length: 33 }, () => rule.conditions[0]) }]),
    ).toContain('1–32')
    expect(
      ruleError([{ ...rule, conditions: [{ tag: '', operator: 'eq', values: ['gray'] }] }]),
    ).toContain('标签名称')
    expect(
      ruleError([
        { ...rule, conditions: [{ tag: 'env', operator: 'in', values: ['中'.repeat(171)] }] },
      ]),
    ).toContain('512 字节')
    expect(
      ruleError([
        { ...rule, conditions: [{ tag: 'env', operator: 'in', values: Array(101).fill('gray') }] },
      ]),
    ).toContain('100 个')
  })
})
