import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { FolderInput, Trash2 } from 'lucide-react'
import { api, ApiError } from '../lib/api'
import type { ConfigSummary } from '../lib/types'
import { Select } from './select'
import { usePending, useToast } from './providers'
import { Button, Confirmation, ErrorNotice, Modal } from './ui'

export function BatchConfigDialog({
  action,
  configs,
  onClose,
  onSuccess,
}: {
  action: 'delete' | 'move'
  configs: ConfigSummary[]
  onClose: () => void
  onSuccess: () => void
}) {
  const moving = action === 'move'
  const [namespace, setNamespace] = useState(configs[0].key.namespace)
  const [group, setGroup] = useState('')
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  const client = useQueryClient()
  const toast = useToast()
  const namespaces = useQuery({
    queryKey: ['namespaces'],
    queryFn: ({ signal }) => api.namespaces(signal),
    enabled: moving,
  })
  const groups = useQuery({
    queryKey: ['groups', namespace],
    queryFn: ({ signal }) => api.groups(namespace, signal),
    enabled: moving,
  })
  const canSubmit = moving
    ? namespaces.data?.includes(namespace) &&
      groups.data?.includes(group) &&
      configs.every((item) => item.key.namespace !== namespace || item.key.group !== group)
    : confirmed
  usePending(moving ? '正在移动配置' : '正在删除配置', busy)

  async function submit() {
    if (!canSubmit || busy) return
    setBusy(true)
    setError(undefined)
    try {
      const result = moving
        ? await api.batchMove(configs, namespace, group)
        : await api.batchDelete(configs)
      await Promise.all([
        client.invalidateQueries({ queryKey: ['configs'] }),
        client.invalidateQueries({ queryKey: ['config-index'] }),
        client.invalidateQueries({ queryKey: ['config'] }),
      ])
      toast(
        moving
          ? `已将 ${result.count} 份配置移动到 ${namespace} / ${group}。`
          : `已删除 ${result.count} 份配置。`,
      )
      onSuccess()
    } catch (e) {
      setError(
        e instanceof ApiError &&
          (e.status === 404 || (e.status === 409 && !e.message.includes('目标分组')))
          ? '配置或分组已发生变化，本次操作未执行。请关闭弹窗并刷新列表后重新选择。'
          : e,
      )
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      title={moving ? '移动配置' : '批量删除配置'}
      description={
        moving
          ? '保留配置名称、历史版本、beta 和灰度规则。原路径的客户端将收到删除通知。'
          : '全部历史版本、beta 和灰度规则也会删除，此操作无法撤销。'
      }
      onClose={onClose}
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button
            variant={moving ? 'primary' : 'danger'}
            busy={busy}
            disabled={!canSubmit}
            onClick={submit}
          >
            {moving ? (
              <FolderInput size={16} aria-hidden="true" />
            ) : (
              <Trash2 size={16} aria-hidden="true" />
            )}
            {moving ? '确认移动' : '确认删除'}
          </Button>
        </>
      }
    >
      <div className="batch-summary">
        <strong>已选择 {configs.length} 份配置</strong>
        <ul>
          {configs.map((item) => (
            <li key={item.id} className="mono">
              {item.key.name}
            </li>
          ))}
        </ul>
      </div>
      {moving ? (
        <div className="form-stack">
          <label className="form-field">
            目标命名空间
            <Select
              aria-label="目标命名空间"
              value={namespace}
              disabled={busy || namespaces.isPending}
              onValueChange={(value) => {
                setNamespace(value)
                setGroup('')
                setError(undefined)
              }}
            >
              {namespaces.data?.map((name) => (
                <option key={name}>{name}</option>
              ))}
            </Select>
          </label>
          <ErrorNotice error={namespaces.error} onRetry={() => namespaces.refetch()} />
          <label className="form-field">
            目标分组
            <Select
              aria-label="目标分组"
              value={group}
              disabled={busy || groups.isPending || !!groups.error || !groups.data?.length}
              onValueChange={(value) => {
                setGroup(value)
                setError(undefined)
              }}
            >
              <option value="" disabled>
                {groups.isPending
                  ? '正在读取分组…'
                  : groups.data?.length
                    ? '请选择分组'
                    : '暂无分组'}
              </option>
              {groups.data?.map((name) => (
                <option
                  key={name}
                  disabled={configs.some(
                    (item) => item.key.namespace === namespace && item.key.group === name,
                  )}
                >
                  {name}
                </option>
              ))}
            </Select>
          </label>
          <ErrorNotice error={groups.error} onRetry={() => groups.refetch()} />
        </div>
      ) : (
        <Confirmation
          checked={confirmed}
          onChange={setConfirmed}
          label={`确认删除这 ${configs.length} 份配置及全部历史和灰度规则`}
        />
      )}
      <ErrorNotice error={error} />
    </Modal>
  )
}
