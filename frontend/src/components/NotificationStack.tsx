import { useNotifications } from '../hooks/useNotifications'
import type { Notification, Severity } from '../hooks/useNotifications'

const SEVERITY_STYLE: Record<Severity, string> = {
  error: 'bg-red-900/90 border-red-700 text-red-100',
  critical: 'bg-red-950 border-red-500 text-red-50',
  warn: 'bg-yellow-900/90 border-yellow-700 text-yellow-100',
  info: 'bg-slate-800/90 border-slate-600 text-slate-100',
}

const SEVERITY_LABEL: Record<Severity, string> = {
  error: 'Error',
  critical: 'Critical',
  warn: 'Warning',
  info: 'Info',
}

const ONGOING_STYLE = 'border-2 ring-1 ring-inset ring-current/20'

function Toast({ notification, onDismiss }: { notification: Notification; onDismiss: () => void }) {
  const baseStyle = SEVERITY_STYLE[notification.severity]
  const persistenceStyle = notification.persistence === 'ongoing' ? ONGOING_STYLE : ''
  return (
    <div
      role="alert"
      className={`pointer-events-auto cursor-pointer rounded-md border px-4 py-3 shadow-lg transition ${baseStyle} ${persistenceStyle}`}
      onClick={onDismiss}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-xs font-semibold uppercase tracking-wide opacity-80">
            {notification.title ?? SEVERITY_LABEL[notification.severity]}
            {notification.scriptID && (
              <span className="ml-2 font-mono text-[10px] opacity-70">{notification.scriptID}</span>
            )}
            {notification.runID && (
              <span className="ml-2 font-mono text-[10px] opacity-70">{notification.runID.slice(0, 8)}</span>
            )}
          </div>
          <div className="mt-1 break-words text-sm">{notification.message}</div>
          {notification.traceback && (
            <details className="mt-1 text-xs opacity-80">
              <summary className="cursor-pointer">Traceback</summary>
              <pre className="mt-1 whitespace-pre-wrap break-all font-mono text-[10px] opacity-90">{notification.traceback}</pre>
            </details>
          )}
        </div>
        <button
          type="button"
          aria-label="Dismiss"
          className="text-lg leading-none opacity-70 hover:opacity-100"
          onClick={(e) => {
            e.stopPropagation()
            onDismiss()
          }}
        >
          ×
        </button>
      </div>
    </div>
  )
}

function CriticalPane({ notification, onDismiss }: { notification: Notification; onDismiss: () => void }) {
  return (
    <div role="alertdialog" className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-6">
      <div className="max-w-2xl rounded-md border-2 border-red-500 bg-red-950 p-6 text-red-50 shadow-2xl">
        <div className="text-sm font-semibold uppercase tracking-wide opacity-80">
          {notification.title ?? 'Critical'}
        </div>
        <div className="mt-2 break-words text-base">{notification.message}</div>
        {notification.traceback && (
          <pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-all rounded bg-black/40 p-2 font-mono text-xs">{notification.traceback}</pre>
        )}
        <div className="mt-4 flex justify-end">
          <button
            type="button"
            className="rounded bg-red-700 px-4 py-2 text-sm hover:bg-red-600"
            onClick={onDismiss}
          >
            Dismiss
          </button>
        </div>
      </div>
    </div>
  )
}

function NotificationStack() {
  const { notifications, dismissNotification } = useNotifications()

  // Critical takes the full screen and pre-empts the toast/banner stack.
  const critical = notifications.find(n => n.persistence === 'catastrophic')
  if (critical) {
    return <CriticalPane notification={critical} onDismiss={() => dismissNotification(critical.id)} />
  }

  // Banners (ongoing) render across the top; toasts (one-shot) at the bottom-right.
  const banners = notifications.filter(n => n.persistence === 'ongoing')
  const toasts = notifications.filter(n => n.persistence === 'one-shot')

  if (banners.length === 0 && toasts.length === 0) return null

  return (
    <>
      {banners.length > 0 && (
        <div className="pointer-events-none fixed inset-x-0 top-2 z-40 flex flex-col items-center gap-2 px-4">
          <div className="flex w-full max-w-3xl flex-col gap-2">
            {banners.map((n) => (
              <Toast key={n.id} notification={n} onDismiss={() => dismissNotification(n.id)} />
            ))}
          </div>
        </div>
      )}
      {toasts.length > 0 && (
        <div className="pointer-events-none fixed bottom-4 right-4 z-50 flex w-full max-w-sm flex-col gap-2">
          {toasts.map((n) => (
            <Toast key={n.id} notification={n} onDismiss={() => dismissNotification(n.id)} />
          ))}
        </div>
      )}
    </>
  )
}

export default NotificationStack
