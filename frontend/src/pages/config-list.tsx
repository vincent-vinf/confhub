import { Select } from '../components/select'
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router-dom'
import {
  ArrowRight,
  ArrowUp,
  ArrowDown,
  ArrowUpDown,
  ChevronLeft,
  ChevronRight,
  FileCode2,
  Download,
  Upload,
  FolderInput,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  X,
} from 'lucide-react'
import { api, configUrl, groupIndex } from '../lib/api'
import { Badge, Button, Empty, ErrorNotice, Loading } from '../components/ui'
import { ImportConfigDialog } from '../components/import-config-dialog'
import { BatchConfigDialog } from '../components/batch-config-dialog'
import { useAuth, usePending, useToast, usePermission } from '../components/providers'
import { allows } from '../lib/permissions'
import { formatDate } from '../lib/config'
import type { ConfigSummary } from '../lib/types'

export function ConfigListPage() {
  const { user } = useAuth()
  const [params, setParams] = useSearchParams()
  const namespace = params.get('namespace') ?? 'public',
    group = params.get('group') ?? ''
  const readable = usePermission('config.read', namespace, group)
  const importable = usePermission('config.write', namespace)
  const [importOpen, setImportOpen] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState<unknown>()
  const toast = useToast()
  usePending('正在导出配置', exporting)
  const writable = usePermission('config.write', namespace, group)
  const manage = usePermission('organization.manage')
  const [search, setSearch] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [sort, setSort] = useState<{
    column: 'name' | 'group' | 'version' | 'published'
    direction: 'asc' | 'desc'
  }>({ column: 'name', direction: 'asc' })
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
  const scopeReady =
    namespaces.data?.includes(namespace) === true &&
    !!groups.data &&
    (!group || groups.data.includes(group))
  const query = useQuery({
    queryKey: ['config-index', namespace, group, group ? undefined : groups.data],
    queryFn: async ({ signal }) => {
      const result: ConfigSummary[] = []
      const scopeGroups = group ? [group] : (groups.data ?? [])
      // Bound concurrency while reusing the existing per-group keyset API.
      for (let offset = 0; offset < scopeGroups.length; offset += 4) {
        const pages = await Promise.all(
          scopeGroups.slice(offset, offset + 4).map((name) => groupIndex(namespace, name, signal)),
        )
        result.push(...pages.flat())
      }
      return result
    },
    enabled: scopeReady,
  })
  useEffect(() => {
    const timer = setTimeout(() => {
      setSearchQuery(search.trim())
      setSearchPage(0)
    }, 250)
    return () => clearTimeout(timer)
  }, [search])
  useEffect(() => {
    setSearch('')
    setSearchQuery('')
    setSearchPage(0)
  }, [namespace, group])
  const filtered = query.data?.filter((config) =>
    config.key.name.toLowerCase().includes(searchQuery.toLowerCase()),
  )
  const ordered = filtered?.slice().sort((a, b) => {
    let order = 0
    switch (sort.column) {
      case 'name':
        order = a.key.name.localeCompare(b.key.name, 'zh-CN', { numeric: true })
        break
      case 'group':
        order = a.key.group.localeCompare(b.key.group, 'zh-CN', { numeric: true })
        break
      case 'version':
        order = a.global_version - b.global_version
        break
      case 'published':
        order = Date.parse(a.published_at ?? '') - Date.parse(b.published_at ?? '')
        break
    }
    const stable = a.key.group.localeCompare(b.key.group) || a.key.name.localeCompare(b.key.name)
    return ((Number.isNaN(order) ? 0 : order) || stable) * (sort.direction === 'asc' ? 1 : -1)
  })
  const rows = ordered?.slice(searchPage * 25, (searchPage + 1) * 25)
  const hasNext = (ordered?.length ?? 0) > (searchPage + 1) * 25
  const pageNumber = searchPage + 1
  const selectedWritable =
    writable &&
    selected.every((item) => allows(user, 'config.write', item.key.namespace, item.key.group))
  const creationGroup =
    group ||
    (groups.data?.includes('DEFAULT_GROUP') &&
    allows(user, 'config.write', namespace, 'DEFAULT_GROUP')
      ? 'DEFAULT_GROUP'
      : groups.data?.find((name) => allows(user, 'config.write', namespace, name))) ||
    ''
  useEffect(() => {
    setSelected([])
  }, [namespace, group, searchQuery, searchPage, sort, writable])
  function sortHeading(column: typeof sort.column, label: string) {
    const active = sort.column === column
    const Icon = active ? (sort.direction === 'asc' ? ArrowUp : ArrowDown) : ArrowUpDown
    return (
      <th
        scope="col"
        aria-sort={active ? (sort.direction === 'asc' ? 'ascending' : 'descending') : 'none'}
      >
        <button
          className="table-sort"
          onClick={() => {
            setSort({ column, direction: active && sort.direction === 'asc' ? 'desc' : 'asc' })
            setSearchPage(0)
          }}
          aria-label={`${label}，点击${active && sort.direction === 'asc' ? '降序' : '升序'}排列`}
        >
          {label}
          <Icon size={14} aria-hidden="true" />
        </button>
      </th>
    )
  }
  const emptyMirror =
    user?.role === 'edge' &&
    ((namespaces.isSuccess && namespaces.data.length === 0) ||
      (namespaces.data?.includes(namespace) && groups.isSuccess && groups.data.length === 0))
  const scopeError = emptyMirror
    ? undefined
    : namespaces.data && !namespaces.data.includes(namespace)
      ? '此命名空间不可访问，请选择有权限的命名空间。'
      : group && groups.data && !groups.data.includes(group)
        ? '此分组不可访问，请选择有权限的分组。'
        : undefined
  useEffect(() => {
    if (namespaces.data?.length && !params.has('namespace'))
      setParams(
        { namespace: namespaces.data.includes('public') ? 'public' : namespaces.data[0] },
        { replace: true },
      )
  }, [namespaces.data, params, setParams])
  function changeNamespace(value: string) {
    setParams({ namespace: value })
  }
  async function refresh() {
    setSelected([])
    const refreshedNamespaces = await namespaces.refetch()
    if (refreshedNamespaces.isError || !refreshedNamespaces.data?.includes(namespace)) return
    const refreshedGroups = await groups.refetch()
    if (refreshedGroups.isError || (group && !refreshedGroups.data?.includes(group))) return
    await query.refetch()
  }
  async function exportConfigs(selection: ConfigSummary[] = []) {
    if (exporting) return
    setExporting(true)
    setExportError(undefined)
    try {
      const blob = await api.exportArchive(
        namespace,
        group,
        selection.length ? '' : searchQuery,
        [],
        selection.map((item) => item.key),
      )
      const url = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = `nacos_config_export_${new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)}.zip`
      document.body.appendChild(link)
      link.click()
      link.remove()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      toast('配置已导出。')
    } catch (e) {
      setExportError(e)
    } finally {
      setExporting(false)
    }
  }
  return (
    <>
      <div className="page-heading">
        <div>
          <h1>配置管理</h1>
        </div>
        <div className="heading-actions">
          {importable && (
            <Button onClick={() => setImportOpen(true)}>
              <Upload size={18} aria-hidden="true" />
              导入配置
            </Button>
          )}
          {writable && (
            <Link
              className="button primary"
              to={`/new?${new URLSearchParams({ namespace, group: creationGroup })}`}
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
            onValueChange={(value) => {
              if (value && sort.column === 'group') setSort({ column: 'name', direction: 'asc' })
              setParams({ namespace, group: value })
            }}
          >
            <option value="">全部分组</option>
            {groups.data?.map((g) => (
              <option key={g}>{g}</option>
            ))}
            {group && !groups.data?.includes(group) && <option>{group}</option>}
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
            {readable && (
              <Button
                title="导出当前命名空间、分组与搜索条件下的全部分页"
                disabled={!!scopeError || !rows?.length}
                busy={exporting}
                onClick={() => exportConfigs()}
              >
                <Download size={16} aria-hidden="true" />
                导出全部
              </Button>
            )}
            <div className="search-field">
              <Search size={17} aria-hidden="true" />
              <input
                aria-label={group ? '搜索本分组配置' : '搜索本命名空间配置'}
                placeholder={group ? '搜索本分组配置名称…' : '搜索本命名空间配置名称…'}
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
              busy={namespaces.isFetching || groups.isFetching || query.isFetching}
              onClick={refresh}
            >
              <RefreshCw size={17} aria-hidden="true" />
            </Button>
          </div>
        </div>
        {readable && selected.length > 0 && (
          <div className="batch-toolbar" role="group" aria-label="批量配置操作">
            <span role="status" aria-live="polite" aria-atomic="true">
              已选择 <strong>{selected.length}</strong> 项
            </span>
            <div className="batch-actions">
              <Button busy={exporting} onClick={() => exportConfigs(selected)}>
                <Download size={16} aria-hidden="true" />
                导出选中
              </Button>
              {selectedWritable && (
                <>
                  <Button onClick={() => setBatchAction('move')}>
                    <FolderInput size={16} aria-hidden="true" />
                    移动配置
                  </Button>
                  <Button variant="danger" onClick={() => setBatchAction('delete')}>
                    <Trash2 size={16} aria-hidden="true" />
                    删除配置
                  </Button>
                </>
              )}
              <Button variant="ghost" onClick={() => setSelected([])}>
                取消选择
              </Button>
            </div>
          </div>
        )}
        <ErrorNotice
          error={exportError || scopeError || namespaces.error || groups.error || query.error}
          onRetry={scopeError ? undefined : refresh}
        />
        {emptyMirror ? (
          <Empty title="暂无已同步配置" />
        ) : !scopeError && query.isPending ? (
          <Loading label={searchQuery ? '正在搜索配置…' : '正在读取配置…'} />
        ) : (
          !scopeError &&
          !query.error &&
          (rows?.length ? (
            <div className="table-scroll">
              <table className="config-table">
                <thead>
                  <tr>
                    {readable && (
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
                    {sortHeading('name', '配置名称')}
                    {!group && sortHeading('group', '分组')}
                    {sortHeading('version', '全量版本')}
                    {sortHeading('published', '全量发布时间')}
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
                      {readable && (
                        <td className="selection-cell">
                          <label className="selection-toggle">
                            <input
                              type="checkbox"
                              aria-label={`选择 ${group ? config.key.name : `${config.key.group} / ${config.key.name}`}`}
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
                      {!group && <td className="config-group mono">{config.key.group}</td>}
                      <td>
                        <Badge tone="success">v{config.global_version}</Badge>
                      </td>
                      <td className="config-published">
                        {config.published_at ? (
                          <time
                            dateTime={config.published_at}
                            title={new Date(config.published_at).toLocaleString('zh-CN', {
                              hour12: false,
                            })}
                          >
                            {formatDate(config.published_at)}
                          </time>
                        ) : (
                          '—'
                        )}
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
              title={
                searchQuery
                  ? '没有找到匹配的配置'
                  : group
                    ? '这个分组还没有配置'
                    : '这个命名空间还没有配置'
              }
              action={
                searchQuery ? (
                  <Button onClick={() => setSearch('')}>清除搜索</Button>
                ) : writable ? (
                  <Link
                    to={`/new?${new URLSearchParams({ namespace, group: creationGroup })}`}
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
              onClick={() => setSearchPage((page) => page - 1)}
            >
              <ChevronLeft size={16} aria-hidden="true" />
            </Button>
            <span>{pageNumber}</span>
            <Button
              aria-label="下一页"
              disabled={!hasNext || query.isPending}
              onClick={() => setSearchPage((page) => page + 1)}
            >
              <ChevronRight size={16} aria-hidden="true" />
            </Button>
          </div>
        </div>
      </section>
      {importOpen && (
        <ImportConfigDialog
          initialNamespace={namespace}
          onClose={() => setImportOpen(false)}
          onSuccess={() => {
            setImportOpen(false)
            setSelected([])
            setSearchPage(0)
          }}
        />
      )}
      {selectedWritable && batchAction && selected.length > 0 && (
        <BatchConfigDialog
          action={batchAction}
          configs={selected}
          onClose={() => setBatchAction(undefined)}
          onSuccess={() => {
            setBatchAction(undefined)
            setSelected([])
            setSearchPage(0)
          }}
        />
      )}
    </>
  )
}
