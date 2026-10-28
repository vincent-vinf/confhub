import type { User } from './types'
export function allows(user: User | undefined, action: string, namespace = '', group = '') {
  if (!user?.enabled) return false
  if (user.read_only && action !== 'config.read' && action !== 'audit.read') return false
  if (user.system_admin) return true
  if (action === 'clients.read') return user.clients_read
  if (action !== 'config.read' && action !== 'config.write') return false
  return user.grants.some(
    (g) =>
      (!g.namespace || g.namespace === namespace) &&
      (!group || !g.group || g.group === group) &&
      (g.access === 'write' || action === 'config.read'),
  )
}
