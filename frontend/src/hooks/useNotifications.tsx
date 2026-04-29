import { createContext, useCallback, useContext, useRef, useState } from 'react'
import type { ReactNode } from 'react'

export type NotificationLevel = 'error' | 'warn' | 'info'

export interface Notification {
  id: string
  level: NotificationLevel
  message: string
  scriptID?: string
  runID?: string
  createdAt: number
  autoDismissMs?: number
}

export interface NotificationInput {
  level: NotificationLevel
  message: string
  scriptID?: string
  runID?: string
  autoDismissMs?: number
}

interface NotificationsAPI {
  notifications: Notification[]
  addNotification: (n: NotificationInput) => string
  dismissNotification: (id: string) => void
}

const DEFAULT_AUTO_DISMISS_MS = 6000

const NotificationsContext = createContext<NotificationsAPI | null>(null)

export function NotificationsProvider({ children }: { children: ReactNode }) {
  const [notifications, setNotifications] = useState<Notification[]>([])
  const timersRef = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map())

  const dismissNotification = useCallback((id: string) => {
    const timer = timersRef.current.get(id)
    if (timer) {
      clearTimeout(timer)
      timersRef.current.delete(id)
    }
    setNotifications(prev => prev.filter(n => n.id !== id))
  }, [])

  const addNotification = useCallback((input: NotificationInput): string => {
    const id = crypto.randomUUID()
    const autoDismissMs = input.autoDismissMs ?? DEFAULT_AUTO_DISMISS_MS
    const notification: Notification = {
      ...input,
      id,
      createdAt: Date.now(),
      autoDismissMs,
    }
    setNotifications(prev => [...prev, notification])
    if (autoDismissMs > 0) {
      const timer = setTimeout(() => {
        timersRef.current.delete(id)
        setNotifications(prev => prev.filter(n => n.id !== id))
      }, autoDismissMs)
      timersRef.current.set(id, timer)
    }
    return id
  }, [])

  return (
    <NotificationsContext.Provider value={{ notifications, addNotification, dismissNotification }}>
      {children}
    </NotificationsContext.Provider>
  )
}

export function useNotifications(): NotificationsAPI {
  const ctx = useContext(NotificationsContext)
  if (!ctx) {
    throw new Error('useNotifications must be used within NotificationsProvider')
  }
  return ctx
}
