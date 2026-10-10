import { useEffect, useId, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'

// Suggestions never constrain freeform tags, including future/offline clients.
export function TagInput({
  label,
  value,
  onChange,
  tag,
  placeholder,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  tag?: string
  placeholder?: string
}) {
  const isName = tag === undefined
  const id = useId()
  const popup = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const [prefix, setPrefix] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setPrefix(value), 150)
    return () => clearTimeout(timer)
  }, [value])
  const query = useQuery({
    queryKey: ['client-tags', tag, prefix],
    queryFn: ({ signal }) => api.clientTags(tag, prefix, signal),
    enabled: open && (isName || !!tag),
    staleTime: 5000,
    gcTime: 60000,
  })
  const suggestions = [
    ...new Set([
      ...(isName ? ['sys.ip', 'sys.hostname'] : []),
      ...(isName || tag ? (query.data ?? []) : []),
    ]),
  ]
    .filter((item) => item.startsWith(value))
    .sort()
  const selected = active >= 0 && active < suggestions.length ? active : -1
  useEffect(() => {
    if (!open || selected < 0) return
    const container = popup.current
    const option = document.getElementById(`${id}-${selected}`)
    if (!container || !option || !container.contains(option)) return
    const viewport = container.getBoundingClientRect()
    const row = option.getBoundingClientRect()
    const top = viewport.top + container.clientTop
    const bottom = top + container.clientHeight
    if (row.top < top) container.scrollTop -= top - row.top
    else if (row.bottom > bottom) container.scrollTop += row.bottom - bottom
  }, [open, selected, query.data, id])
  function choose(item: string) {
    onChange(item)
    setOpen(false)
    setActive(-1)
  }
  return (
    <div className="tag-autocomplete">
      <input
        role="combobox"
        aria-label={label}
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={open ? `${id}-options` : undefined}
        aria-activedescendant={open && selected >= 0 ? `${id}-${selected}` : undefined}
        value={value}
        placeholder={placeholder}
        autoComplete="off"
        onFocus={() => setOpen(true)}
        onBlur={() => {
          setOpen(false)
          setActive(-1)
        }}
        onChange={(event) => {
          onChange(event.target.value)
          setOpen(true)
          setActive(-1)
        }}
        onKeyDown={(event) => {
          if (event.key === 'Escape' && open) {
            event.preventDefault()
            event.stopPropagation()
            setOpen(false)
            return
          }
          if ((event.key === 'ArrowDown' || event.key === 'ArrowUp') && suggestions.length) {
            event.preventDefault()
            setOpen(true)
            setActive(
              event.key === 'ArrowDown'
                ? (selected + 1) % suggestions.length
                : (selected - 1 + suggestions.length) % suggestions.length,
            )
          }
          if (event.key === 'Enter' && open && selected >= 0) {
            event.preventDefault()
            choose(suggestions[selected])
          }
        }}
      />
      {open && (
        <div className="tag-suggestion-popup" ref={popup}>
          <div role="listbox" aria-label={`${label}建议`} id={`${id}-options`}>
            {suggestions.map((item, index) => (
              <div
                key={item}
                role="option"
                id={`${id}-${index}`}
                aria-selected={selected === index}
                className={selected === index ? 'selected' : ''}
                onPointerDown={(event) => event.preventDefault()}
                onClick={() => choose(item)}
              >
                {item === '' ? '空字符串' : item}
              </div>
            ))}
          </div>
          {(query.isError ||
            query.isFetching ||
            !suggestions.length ||
            suggestions.length >= 100) && (
            <p className="section-help" role="status">
              {query.isError
                ? '暂无法读取在线标签，仍可手动填写。'
                : query.isFetching
                  ? '正在读取在线标签…'
                  : !suggestions.length
                    ? '暂无匹配的在线标签，仍可手动填写。'
                    : ''}
              {suggestions.length >= 100 && '最多显示 100 项，请输入前缀缩小范围。'}
            </p>
          )}
        </div>
      )}
    </div>
  )
}
