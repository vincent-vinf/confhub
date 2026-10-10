import * as Dialog from '@radix-ui/react-dialog'
import { AlertCircle, Check, FileText, LoaderCircle, X } from 'lucide-react'
import { useId, useRef, type ButtonHTMLAttributes, type ReactNode } from 'react'
import { messageOf } from '../lib/api'

export function Button({
  children,
  variant = 'secondary',
  busy = false,
  className = '',
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  busy?: boolean
}) {
  return (
    <button
      {...props}
      className={`button ${variant} ${className}`}
      disabled={props.disabled || busy}
      aria-busy={busy || undefined}
    >
      {busy && <LoaderCircle className="spin" size={16} aria-hidden="true" />}
      {children}
    </button>
  )
}
export function ErrorNotice({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  if (!error) return null
  return (
    <div className="notice error" role="alert">
      <AlertCircle size={18} aria-hidden="true" />
      <div>{typeof error === 'string' ? error : messageOf(error)}</div>
      {onRetry && <Button onClick={onRetry}>重试</Button>}
    </div>
  )
}
export function Loading({ label = '正在读取…' }: { label?: string }) {
  return (
    <div className="loading" role="status">
      <LoaderCircle size={20} className="spin" aria-hidden="true" />
      {label}
    </div>
  )
}
export function Empty({
  title,
  description,
  action,
}: {
  title: string
  description?: string
  action?: ReactNode
}) {
  return (
    <div className="empty">
      <span className="empty-icon">
        <FileText size={28} aria-hidden="true" />
      </span>
      <h3>{title}</h3>
      {description && <p>{description}</p>}
      {action}
    </div>
  )
}
export function Badge({
  children,
  tone = 'neutral',
}: {
  children: ReactNode
  tone?: 'neutral' | 'success' | 'warning' | 'danger'
}) {
  return <span className={`badge ${tone}`}>{children}</span>
}
export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  wide = false,
  busy = false,
  closable = true,
}: {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  children?: ReactNode
  footer?: ReactNode
  wide?: boolean
  busy?: boolean
  closable?: boolean
}) {
  const descriptionID = useId()
  const returnFocus = useRef<HTMLElement | null>(
    document.activeElement instanceof HTMLElement ? document.activeElement : null,
  )
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(value) => {
        if (!value && !busy && closable) onClose()
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="modal-overlay" />
        <Dialog.Content
          className={`modal ${wide ? 'wide' : ''}`}
          aria-describedby={description ? descriptionID : undefined}
          onOpenAutoFocus={() => {
            if (document.activeElement instanceof HTMLElement) {
              returnFocus.current = document.activeElement
            }
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            if (returnFocus.current?.isConnected && returnFocus.current !== document.body) {
              returnFocus.current.focus()
            } else {
              document.getElementById('main')?.focus()
            }
          }}
          onEscapeKeyDown={(event) => {
            if (busy || !closable) event.preventDefault()
          }}
          onPointerDownOutside={(event) => event.preventDefault()}
        >
          <header className="modal-header">
            <div>
              <Dialog.Title className="modal-title">{title}</Dialog.Title>
              {description && (
                <Dialog.Description id={descriptionID} className="modal-description">
                  {description}
                </Dialog.Description>
              )}
            </div>
            {closable && (
              <Button
                variant="ghost"
                className="icon-button"
                aria-label="关闭对话框"
                onClick={onClose}
                disabled={busy}
              >
                <X size={20} aria-hidden="true" />
              </Button>
            )}
          </header>
          {children && <div className="modal-body">{children}</div>}
          {footer && <footer className="modal-footer">{footer}</footer>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
export function Confirmation({
  checked,
  onChange,
  label = '我已核对差异与影响范围',
}: {
  checked: boolean
  onChange: (checked: boolean) => void
  label?: string
}) {
  return (
    <label className="check-label">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  )
}
export function SuccessIcon() {
  return <Check size={18} aria-hidden="true" />
}
