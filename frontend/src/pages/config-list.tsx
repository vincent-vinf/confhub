import { Select } from '../components/select'
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router-dom'
import {
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  FileCode2,
  FolderInput,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  X,
} from 'lucide-react'
import { api, configUrl, groupIndex } from '../lib/api'
import { Badge, Button, Empty, ErrorNotice, Loading } from '../components/ui'
import { BatchConfigDialog } from '../components/batch-config-dialog'
import { usePermission } from '../components/providers'
import type { ConfigSummary } from '../lib/types'

export function ConfigListPage() {
  const [params, setParams] = useSearchParams()
  const namespace = params.get('namespace') ?? 'public',
    group = params.get('group') ?? 'DEFAULT_GROUP'
  const writable = usePermission('config.write', namespace, group)
  const manage = usePermission('organization.manage')
  const [search, setSearch] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [cursors, setCursors] = useState([''])
  const [searchPage, setSearchPage] = useState(0)
  const [selected, setSelected] = useState<ConfigSummary[]>([])
  const [batchAction, setBatchAction] = useState<'delete' | 'move'>()
  const namespaces = useQuery({
    queryKey: ['namespaces'],
    queryFn: ({ signal }) => api.namespaces(signal),
  })
  const groups = useQuery({
    queryKey: ['groups', namespace],
    queryFn: ({ signal }) => api.groups(namespace, signal),
    enabled: namespaces.data?.includes(namespace) === true,
  })
  const listing = useQuery({
    queryKey: ['configs', namespace, group, cursors.at(-1)],
    queryFn: ({ signal }) => api.list(namespace, group, cursors.at(-1), 25, signal),
    enabled: groups.data?.includes(group) === true && !searchQuery,
  })
  const index = useQuery({
    queryKey: ['config-index', namespace, group],
    queryFn: ({ signal }) => groupIndex(namespace, group, signal),
    enabled: !!searchQuery && groups.data?.includes(group) === true,
  })
  useEffect(() => {
    const timer = setTimeout(() => {
      setSearchQuery(search.trim())
      setSearchPage(0)
    }, 250)
    return () => clearTimeout(timer)
  }, [search])
  useEffect(() => {
    setCursors([''])
    setSearch('')
    setSearchQuery('')
    setSearchPage(0)
  }, [namespace, group])
  const filtered = searchQuery
    ? index.data?.filter((config) =>
        config.key.name.toLowerCase().includes(searchQuery.toLowerCase()),
      )
    : undefined
  const query = searchQuery ? index : listing
  const rows = searchQuery ? filtered?.slice(searchPage * 25, (searchPage + 1) * 25) : listing.data
  const hasNext = searchQuery
    ? (filtered?.length ?? 0) > (searchPage + 1) * 25
    : rows?.length === 25
  const pageNumber = searchQuery ? searchPage + 1 : cursors.length
  useEffect(() => {
    setSelected([])
  }, [namespace, group, searchQuery, searchPage, pageNumber, writable])
  const scopeError =
    namespaces.data && !namespaces.data.includes(namespace)
      ? '此命名空间不可访问，请选择有权限的命名空间。'
      : groups.data && !groups.data.includes(group)
        ? '此分组不可访问，请选择有权限的分组。'
        : undefined
  useEffect(() => {
    if (namespaces.data?.length && !params.has('namespace'))
      setParams(
        { namespace: namespaces.data.includes('public') ? 'public' : namespaces.data[0] },
        { replace: true },
      )
  }, [namespaces.data, params, setParams])
  useEffect(() => {
    if (groups.data?.length && !params.has('group'))
      setParams(
        {
          namespace,
          group: groups.data.includes('DEFAULT_GROUP') ? 'DEFAULT_GROUP' : groups.data[0],
        },
        { replace: true },
      )
  }, [groups.data, namespace, params, setParams])
  function changeNamespace(value: string) {
    setParams({ namespace: value })
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>配置管理</h1>
        </div>
        {writable && (
          <Link
            className="button primary"
            to={`/new?${new URLSearchParams({ namespace, group })}`}
            aria-disabled={!!scopeError}
            onClick={(event) => {
              if (scopeError) event.preventDefault()
            }}
          >
            <Plus size={18} aria-hidden="true" />
            新建配置
          </Link>
        )}
      </div>
      <div className="scope-bar">
        <label>
          命名空间
          <Select
            aria-label="命名空间"
            value={namespace}
            onValueChange={(value) => changeNamespace(value)}
          >
            {namespaces.data?.map((n) => (
              <option key={n}>{n}</option>
            ))}
            {!namespaces.data?.includes(namespace) && <option>{namespace}</option>}
          </Select>
        </label>
        <span className="scope-divider" />
        <label>
          分组
          <Select
            aria-label="分组"
            value={group}
            onValueChange={(value) => setParams({ namespace, group: value })}
          >
            {groups.data?.map((g) => (
              <option key={g}>{g}</option>
            ))}
            {!groups.data?.includes(group) && <option>{group}</option>}
          </Select>
        </label>
        {manage && (
          <Link className="scope-manage" to="/organization">
            管理命名空间 <ArrowRight size={14} aria-hidden="true" />
          </Link>
        )}
      </div>
      <section className="panel">
        <div className="panel-toolbar">
          <div className="panel-title">
            配置列表{' '}
            <Badge>
              {searchQuery && filtered ? `${filtered.length} 个匹配` : `第 ${pageNumber} 页`}
            </Badge>
          </div>
          <div className="list-tools">
            <div className="search-field">
              <Search size={17} aria-hidden="true" />
              <input
                aria-label="搜索本分组配置"
                placeholder="搜索本分组配置名称…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              {search && (
                <button
                  className="search-clear"
                  aria-label="清除搜索"
                  onClick={() => setSearch('')}
                >
                  <X size={14} aria-hidden="true" />
                </button>
              )}
            </div>
            <Button
              variant="ghost"
              className="icon-button"
              aria-label="刷新配置列表"
              busy={query.isFetching}
              onClick={() => {
                setSelected([])
                query.refetch()
              }}
            >
              <RefreshCw size={17} aria-hidden="true" />
            </Button>
          </div>
        </div>
        {writable && selected.length > 0 && (
          <div className="batch-toolbar" role="group" aria-label="批量配置操作">
            <span role="status" aria-live="polite" aria-atomic="true">
              已选择 <strong>{selected.length}</strong> 项
            </span>
            <div className="batch-actions">
              <Button onClick={() => setBatchAction('move')}>
                <FolderInput size={16} aria-hidden="true" />
                移动配置
              </Button>
              <Button variant="danger" onClick={() => setBatchAction('delete')}>
                <Trash2 size={16} aria-hidden="true" />
                删除配置
              </Button>
              <Button variant="ghost" onClick={() => setSelected([])}>
                取消选择
              </Button>
            </div>
          </div>
        )}
        <ErrorNotice
          error={scopeError || namespaces.error || groups.error || query.error}
          onRetry={scopeError ? undefined : () => query.refetch()}
        />
        {!scopeError && query.isPending ? (
          <Loading label={searchQuery ? '正在搜索本分组…' : '正在读取配置…'} />
        ) : (
          !scopeError &&
          !query.error &&
          (rows?.length ? (
            <div className="table-scroll">
              <table className="config-table">
                <thead>
                  <tr>
                    {writable && (
                      <th className="selection-cell" scope="col">
                        <label className="selection-toggle">
                          <input
                            type="checkbox"
                            aria-label="全选当前页配置"
                            checked={rows.every((item) =>
                              selected.some((selection) => selection.id === item.id),
                            )}
                            ref={(input) => {
                              if (input)
                                input.indeterminate =
                                  selected.length > 0 &&
                                  !rows.every((item) =>
                                    selected.some((selection) => selection.id === item.id),
                                  )
                            }}
                            onChange={(event) => setSelected(event.target.checked ? [...rows] : [])}
                          />
                        </label>
                      </th>
                    )}
                    <th>配置名称</th>
                    <th>全量版本</th>
                    <th>
                      <span className="sr-only">操作</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((config) => (
                    <tr
                      key={config.id}
                      data-selected={selected.some((item) => item.id === config.id) || undefined}
                    >
                      {writable && (
                        <td className="selection-cell">
                          <label className="selection-toggle">
                            <input
                              type="checkbox"
                              aria-label={`选择 ${config.key.name}`}
                              checked={selected.some((item) => item.id === config.id)}
                              onChange={(event) =>
                                setSelected((previous) =>
                                  event.target.checked
                                    ? [...previous, config]
                                    : previous.filter((item) => item.id !== config.id),
                                )
                              }
                            />
                          </label>
                        </td>
                      )}
                      <td>
                        <Link to={configUrl(config.key)} className="config-name">
                          <span className="file-icon">
                            <FileCode2 size={19} aria-hidden="true" />
                          </span>
                          <span className="mono">{config.key.name}</span>
                        </Link>
                      </td>
                      <td>
                        <Badge tone="success">v{config.global_version}</Badge>
                      </td>
                      <td>
                        <Link
                          to={configUrl(config.key)}
                          className="table-open"
                          aria-label={`打开 ${config.key.name}`}
                        >
                          <span>打开</span>
                          <ArrowRight size={16} aria-hidden="true" />
                        </Link>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty
              title={searchQuery ? '没有找到匹配的配置' : '这个分组还没有配置'}
              action={
                searchQuery ? (
                  <Button onClick={() => setSearch('')}>清除搜索</Button>
                ) : writable ? (
                  <Link
                    to={`/new?${new URLSearchParams({ namespace, group })}`}
                    className="button secondary"
                  >
                    创建第一份配置
                  </Link>
                ) : undefined
              }
            />
          ))
        )}
        <div className="table-footer">
          <span>{rows?.length ?? 0} 项</span>
          <div className="pagination">
            <Button
              aria-label="上一页"
              disabled={pageNumber === 1 || query.isPending}
              onClick={() =>
                searchQuery
                  ? setSearchPage((page) => page - 1)
                  : setCursors((previous) => previous.slice(0, -1))
              }
            >
              <ChevronLeft size={16} aria-hidden="true" />
            </Button>
            <span>{pageNumber}</span>
            <Button
              aria-label="下一页"
              disabled={!hasNext || query.isPending}
              onClick={() =>
                searchQuery
                  ? setSearchPage((page) => page + 1)
                  : setCursors((previous) => [...previous, rows!.at(-1)!.key.name])
              }
            >
              <ChevronRight size={16} aria-hidden="true" />
            </Button>
          </div>
        </div>
      </section>
      {writable && batchAction && selected.length > 0 && (
        <BatchConfigDialog
          action={batchAction}
          configs={selected}
          onClose={() => setBatchAction(undefined)}
          onSuccess={() => {
            setBatchAction(undefined)
            setSelected([])
            setCursors([''])
            setSearchPage(0)
          }}
        />
      )}
    </>
  )
}
