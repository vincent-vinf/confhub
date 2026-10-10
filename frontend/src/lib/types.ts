export const formats = ['text', 'json', 'yaml', 'toml', 'xml', 'properties', 'ini'] as const
export type Format = (typeof formats)[number]
export type ConfigKey = { namespace: string; group: string; name: string }
export type Version = {
  number: number
  content: string
  format: Format
  description: string
  action: string
  source_version?: number
  created_at: string
  references?: string[]
}
export type Condition = { tag: string; operator: 'eq' | 'in'; values: string[] }
export type GrayRule = {
  id: string
  name: string
  enabled: boolean
  beta: { base_version: number; content: string; format: Format; description: string }
  conditions: Condition[]
}
export type ConfigState = {
  id: string
  key: ConfigKey
  revision: number
  last_version: number
  global_version: number
  sequence: number
  rules: GrayRule[]
  versions: Record<number, Version>
}
export type ConfigSummary = Pick<
  ConfigState,
  'id' | 'key' | 'revision' | 'last_version' | 'global_version'
>
export type VersionPage = { versions: Version[]; next_before?: number }
export type Baseline = { expected_id: string; expected_revision: number }
export type Edit = Baseline & {
  content: string
  format: Format
  description: string
  rule_id?: string
  confirmed: true
}
export type Mutation = { state: ConfigState; changed: boolean; sequence: number }
export type Effective = {
  id?: string
  key: ConfigKey
  revision: number
  sequence: number
  version: number
  content: string
  format: Format
  rule_id?: string
  deleted: boolean
}
export type Draft = { content: string; format: Format; description: string; ruleId?: string }
