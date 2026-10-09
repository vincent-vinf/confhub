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
  target_version: 1,
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
  it('全量与灰度分别从自己的固定目标编辑，说明不继承', () => {
    expect(draftFrom(state)).toEqual({
      content: '{"v":2}',
      format: 'json',
      description: '',
      ruleId: undefined,
    })
    expect(draftFrom(state, 'gray').content).toBe('{"v":1}')
    expect(() => draftFrom(state, 'removed')).toThrow('编辑目标')
  })
  it('乐观锁携带配置身份与修订，而非比较版本', () => {
    expect(editPayload(state, { ...draftFrom(state, 'gray'), content: 'new' })).toEqual({
      expected_id: 'original',
      expected_revision: 7,
      content: 'new',
      format: 'json',
      description: '',
      rule_id: 'gray',
      confirmed: true,
    })
    expect(baselineOf()).toEqual({ expected_id: '', expected_revision: 0 })
  })
  it('发布影响明确说明固定灰度和全量边界', () => {
    expect(publicationImpact(state)).toContain('1 条灰度规则继续使用各自固定版本')
    expect(publicationImpact(state, 'gray')).toContain('全量 v2')
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
})
