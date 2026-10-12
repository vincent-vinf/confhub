import { expect, it } from 'vitest'
import { allows } from './permissions'
import type { User } from './types'
const user: User = {
  id: 'id',
  username: 'operator',
  enabled: true,
  system_admin: false,
  clients_read: false,
  revision: 1,
  grants: [
    { namespace: 'dev', group: '', access: 'write' },
    { namespace: 'prod', group: 'apps', access: 'read' },
  ],
}
it('开发可写、生产指定分组只读，未授权范围和管理功能拒绝访问', () => {
  expect(allows(user, 'config.write', 'dev', 'future')).toBe(true)
  expect(allows(user, 'config.read', 'prod', 'apps')).toBe(true)
  expect(allows(user, 'config.write', 'prod', 'apps')).toBe(false)
  expect(allows(user, 'config.read', 'prod', 'secret')).toBe(false)
  expect(allows(user, 'config.read', 'unknown', 'apps')).toBe(false)
  expect(allows(user, 'clients.read')).toBe(false)
  expect(allows(user, 'users.manage')).toBe(false)
  expect(allows(undefined, 'config.read', 'dev', 'apps')).toBe(false)
})
it('授权合并、全局范围、客户端权限和禁用管理员', () => {
  expect(
    allows(
      { ...user, grants: [...user.grants, { namespace: 'prod', group: '', access: 'write' }] },
      'config.write',
      'prod',
      'apps',
    ),
  ).toBe(true)
  expect(
    allows(
      { ...user, grants: [{ namespace: '', group: '', access: 'read' }] },
      'config.read',
      'future',
      'future',
    ),
  ).toBe(true)
  expect(
    allows(
      { ...user, grants: [{ namespace: '*', group: '*', access: 'write' }] },
      'config.write',
      'dev',
      'apps',
    ),
  ).toBe(false)
  expect(allows({ ...user, clients_read: true }, 'clients.read')).toBe(true)
  expect(allows({ ...user, system_admin: true }, 'organization.manage')).toBe(true)
  expect(allows({ ...user, enabled: false, system_admin: true }, 'organization.manage')).toBe(false)
})
