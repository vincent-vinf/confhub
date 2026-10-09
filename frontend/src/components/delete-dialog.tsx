import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Trash2 } from 'lucide-react'
import { api, listUrl } from '../lib/api'
import { baselineOf } from '../lib/config'
import type { ConfigKey, ConfigState } from '../lib/types'
import { useToast, usePending, useNavigationPermit } from './providers'
import { Button, ErrorNotice, Modal } from './ui'

export function DeleteConfigDialog({
  configKey,
  state,
  onClose,
}: {
  configKey: ConfigKey
  state: ConfigState
  onClose: () => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>()
  const permit = useNavigationPermit()
  const client = useQueryClient()
  const navigate = useNavigate()
  const toast = useToast()
  usePending('正在删除配置', busy)
  async function remove() {
    if (name !== configKey.name || busy) return
    setBusy(true)
    setError(undefined)
    try {
      await api.remove(configKey, baselineOf(state))
      await Promise.all([
        client.invalidateQueries({ queryKey: ['configs'] }),
        client.invalidateQueries({ queryKey: ['config-index'] }),
      ])
      toast('配置及全部历史、灰度规则已删除。')
      onClose()
      permit()
      navigate(listUrl(configKey.namespace, configKey.group), { replace: true })
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      title="永久删除配置"
      description="此操作无法撤销。全部历史版本和灰度规则也会一并删除，客户端将收到配置删除通知。"
      onClose={onClose}
      busy={busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button variant="danger" busy={busy} disabled={name !== configKey.name} onClick={remove}>
            <Trash2 size={16} aria-hidden="true" />
            永久删除
          </Button>
        </>
      }
    >
      <div className="notice warning">
        将删除 <strong className="mono">{configKey.name}</strong> 及其 {state.rules.length}{' '}
        条灰度规则。
      </div>
      <label className="form-field">
        输入配置名称以确认
        <input
          autoComplete="off"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={configKey.name}
        />
      </label>
      <ErrorNotice error={error} />
    </Modal>
  )
}
