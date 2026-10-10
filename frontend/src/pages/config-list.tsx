import { Select } from '../components/select'
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router-dom'
import {
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  FileCode2,
  Plus,
  RefreshCw,
  Search,
  X,
} from 'lucide-react'
import { api, configUrl, groupIndex } from '../lib/api'
import { Badge, Button, Empty, ErrorNotice, Loading } from '../components/ui'

export function ConfigListPage() {
  const [params, setParams] = useSearchParams()
  const namespace = params.get('namespace') ?? 'public',
    group = params.get('group') ?? 'DEFAULT_GROUP'
  const [search, setSearch] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [cursors, setCursors] = useState([''])
  const [searchPage, setSearchPage] = useState(0)
  const namespaces = useQuery({
    queryKey: ['namespaces'],
    queryFn: ({ signal }) => api.namespaces(signal),
  })
  const groups = useQuery({
    queryKey: ['groups', namespace],
    queryFn: ({ signal }) => api.groups(namespace, signal),
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
  const scopeError =
    namespaces.data && !namespaces.data.includes(namespace)
      ? '命名空间不存在，请选择其他命名空间。'
      : groups.data && !groups.data.includes(group)
        ? '此命名空间没有选中的分组，请选择或创建分组。'
        : undefined
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
        <Link className="scope-manage" to="/organization">
          管理命名空间 <ArrowRight size={14} aria-hidden="true" />
        </Link>
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
              onClick={() => query.refetch()}
            >
              <RefreshCw size={17} aria-hidden="true" />
            </Button>
          </div>
        </div>
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
                    <th>配置名称</th>
                    <th>全量版本</th>
                    <th>
                      <span className="sr-only">操作</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((config) => (
                    <tr key={config.id}>
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
                ) : (
                  <Link
                    to={`/new?${new URLSearchParams({ namespace, group })}`}
                    className="button secondary"
                  >
                    创建第一份配置
                  </Link>
                )
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
    </>
  )
}
