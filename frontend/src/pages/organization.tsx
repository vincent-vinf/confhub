import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Folder, FolderPlus, Layers3, Plus, Trash2 } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api, listUrl } from '../lib/api'
import { nameError } from '../lib/config'
import { Badge, Button, Empty, ErrorNotice, Loading, Modal } from '../components/ui'
import { useToast, usePending } from '../components/providers'

type OrgAction = {
  kind: 'namespace' | 'group'
  mode: 'create' | 'delete'
  namespace: string
  name?: string
}
export function OrganizationPage() {
  const [selected, setSelected] = useState('public')
  const [action, setAction] = useState<OrgAction>()
  const namespaces = useQuery({
    queryKey: ['namespaces'],
    queryFn: ({ signal }) => api.namespaces(signal),
  })
  const active = namespaces.data?.includes(selected) ? selected : (namespaces.data?.[0] ?? '')
  const groups = useQuery({
    queryKey: ['groups', active],
    queryFn: ({ signal }) => api.groups(active, signal),
    enabled: !!active,
  })
  return (
    <>
      <div className="page-heading">
        <div>
          <span className="eyebrow">WORKSPACE ORGANIZATION</span>
          <h1>命名空间</h1>
        </div>
        <Button
          variant="primary"
          onClick={() => setAction({ kind: 'namespace', mode: 'create', namespace: '' })}
        >
          <Plus size={18} aria-hidden="true" />
          新建命名空间
        </Button>
      </div>
      <ErrorNotice error={namespaces.error} onRetry={() => namespaces.refetch()} />
      <div className="organization-layout">
        <section className="panel namespace-panel">
          <div className="panel-toolbar">
            <div className="panel-title">
              <Layers3 size={18} aria-hidden="true" />
              命名空间 <Badge>{namespaces.data?.length ?? '…'}</Badge>
            </div>
          </div>
          {namespaces.isPending ? (
            <Loading />
          ) : namespaces.data?.length ? (
            <div className="namespace-list">
              {namespaces.data.map((namespace) => (
                <div
                  className={`namespace-row ${active === namespace ? 'selected' : ''}`}
                  key={namespace}
                >
                  <button
                    onClick={() => setSelected(namespace)}
                    aria-pressed={active === namespace}
                  >
                    <Layers3 size={18} aria-hidden="true" />
                    <span className="mono">{namespace}</span>
                    {namespace === 'public' && <small>默认</small>}
                  </button>
                  <Button
                    variant="ghost"
                    className="icon-button"
                    aria-label={`删除命名空间 ${namespace}`}
                    onClick={() =>
                      setAction({ kind: 'namespace', mode: 'delete', namespace, name: namespace })
                    }
                  >
                    <Trash2 size={16} aria-hidden="true" />
                  </Button>
                </div>
              ))}
            </div>
          ) : (
            <Empty title="没有命名空间" description="新建一个命名空间开始组织配置。" />
          )}
        </section>
        <section className="panel group-panel">
          <div className="panel-toolbar">
            <div>
              <div className="panel-title">
                <Folder size={18} aria-hidden="true" />
                分组 {active && <Badge>{active}</Badge>}
              </div>
            </div>
            <Button
              disabled={!active}
              onClick={() => setAction({ kind: 'group', mode: 'create', namespace: active })}
            >
              <FolderPlus size={16} aria-hidden="true" />
              新建分组
            </Button>
          </div>
          <ErrorNotice error={groups.error} onRetry={() => groups.refetch()} />
          {active && groups.isPending ? (
            <Loading />
          ) : groups.data?.length ? (
            <div className="group-list">
              {groups.data.map((group) => (
                <article key={group}>
                  <span className="folder-tile">
                    <Folder size={20} aria-hidden="true" />
                  </span>
                  <div>
                    <h3 className="mono">{group}</h3>
                    <Link to={listUrl(active, group)}>查看配置 →</Link>
                  </div>
                  <Button
                    variant="ghost"
                    className="icon-button destructive-text"
                    aria-label={`删除分组 ${group}`}
                    onClick={() =>
                      setAction({ kind: 'group', mode: 'delete', namespace: active, name: group })
                    }
                  >
                    <Trash2 size={16} aria-hidden="true" />
                  </Button>
                </article>
              ))}
            </div>
          ) : (
            <Empty
              title={active ? '这个命名空间还没有分组' : '先创建一个命名空间'}
              description={active ? '新建一个分组，再添加配置。' : undefined}
            />
          )}
        </section>
      </div>
      {action && (
        <OrganizationDialog
          action={action}
          onClose={() => setAction(undefined)}
          onCreatedNamespace={setSelected}
        />
      )}
    </>
  )
}
function OrganizationDialog({
  action,
  onClose,
  onCreatedNamespace,
}: {
  action: OrgAction
  onClose: () => void
  onCreatedNamespace: (namespace: string) => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  const client = useQueryClient()
  const toast = useToast()
  const kind = action.kind === 'namespace' ? '命名空间' : '分组'
  const removing = action.mode === 'delete'
  usePending('正在更新命名空间或分组', busy)
  async function submit() {
    if (busy) return
    if (removing && name !== action.name) return
    const invalid = !removing && nameError(name)
    if (invalid) {
      setError(invalid)
      return
    }
    setBusy(true)
    setError(undefined)
    try {
      if (action.kind === 'namespace') {
        if (removing) await api.deleteNamespace(action.namespace)
        else await api.createNamespace(name)
      } else {
        if (removing) await api.deleteGroup(action.namespace, action.name!)
        else await api.createGroup(action.namespace, name)
      }
      await Promise.all([
        client.invalidateQueries({ queryKey: ['namespaces'] }),
        client.invalidateQueries({ queryKey: ['groups'] }),
      ])
      if (!removing && action.kind === 'namespace') onCreatedNamespace(name)
      toast(`${kind}已${removing ? '删除' : '创建'}。`)
      onClose()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      title={`${removing ? '删除' : '新建'}${kind}`}
      description={
        removing
          ? `仅允许删除空${kind}。删除不可撤销，请确认其中的内容已移除。`
          : `名称用于组织配置，创建后可在配置管理中使用。`
      }
      onClose={onClose}
      busy={busy}
      footer={
        <>
          <Button disabled={busy} onClick={onClose}>
            取消
          </Button>
          <Button
            variant={removing ? 'danger' : 'primary'}
            busy={busy}
            disabled={removing ? name !== action.name : !name}
            onClick={submit}
          >
            {removing ? '确认删除' : '创建'}
          </Button>
        </>
      }
    >
      <label className="form-field">
        {removing ? `输入名称「${action.name}」以确认` : `${kind}名称`}
        <input
          autoComplete="off"
          value={name}
          onChange={(e) => {
            setName(e.target.value)
            setError(undefined)
          }}
          placeholder={removing ? action.name : '例如 production'}
        />
      </label>
      {action.kind === 'group' && (
        <p className="muted">
          所属命名空间：<span className="mono">{action.namespace}</span>
        </p>
      )}
      <ErrorNotice error={error} />
    </Modal>
  )
}
