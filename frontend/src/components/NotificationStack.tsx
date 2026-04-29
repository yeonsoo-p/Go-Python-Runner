import { useNotifications } from '../hooks/useNotifications'
import type { Notification, NotificationLevel } from '../hooks/useNotifications'

const LEVEL_STYLE: Record<NotificationLevel, string> = {
  error: 'bg-red-900/90 border-red-700 text-red-100',
  warn: 'bg-yellow-900/90 border-yellow-700 text-yellow-100',
  info: 'bg-slate-800/90 border-slate-600 text-slate-100',
}

const LEVEL_LABEL: Record<NotificationLevel, string> = {
  error: 'Error',
  warn: 'Warning',
  info: 'Info',
}

function Toast({ notification, onDismiss }: { notification: Notification; onDismiss: () => void }) {
  const style = LEVEL_STYLE[notification.level]
  return (
    <div
      role="alert"
      className={`pointer-events-auto cursor-pointer rounded-md border px-4 py-3 shadow-lg transition ${style}`}
      onClick={onDismiss}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-xs font-semibold uppercase tracking-wide opacity-80">
            {LEVEL_LABEL[notification.level]}
            {notification.scriptID && (
              <span className="ml-2 font-mono text-[10px] opacity-70">{notification.scriptID}</span>
            )}
          </div>
          <div className="mt-1 break-words text-sm">{notification.message}</div>
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

function NotificationStack() {
  const { notifications, dismissNotification } = useNotifications()
  if (notifications.length === 0) return null
  return (
    <div className="pointer-events-none fixed bottom-4 right-4 z-50 flex w-full max-w-sm flex-col gap-2">
      {notifications.map((n) => (
        <Toast key={n.id} notification={n} onDismiss={() => dismissNotification(n.id)} />
      ))}
    </div>
  )
}

export default NotificationStack
