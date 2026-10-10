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
export type Condition = { tag: string; operator: 'eq' | 'in' | 'ip_range'; values: string[] }
export type GrayRule = {
  id: string
  name: string
  enabled: boolean
  conditions: Condition[]
}
export type ConfigState = {
  id: string
  key: ConfigKey
  revision: number
  last_version: number
  global_version: number
  sequence: number
  beta?: { content: string; format: Format; description: string; updated_at: string }
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
  target?: 'global' | 'beta'
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
  beta: boolean
  deleted: boolean
}
export type Draft = {
  content: string
  format: Format
  description: string
  target?: 'global' | 'beta'
}

export type ClientSubscription = {
  key: ConfigKey
  id?: string
  version: number
  revision: number
  rule_id?: string
  beta: boolean
  deleted: boolean
  sent: boolean
}
export type OnlineClient = {
  id: string
  instance_id: string
  source_address: string
  connected_at: string
  refreshed_at: string
  tags: Record<string, string>
  subscriptions: ClientSubscription[]
}
export type ClientPage = { clients: OnlineClient[]; next_after?: string }
