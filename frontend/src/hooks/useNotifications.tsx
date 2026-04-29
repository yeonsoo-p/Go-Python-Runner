import { createContext, useCallback, useContext, useRef, useState } from 'react'
import type { ReactNode } from 'react'

// Severity mirrors the Go notify.Severity enum and proto runner.Severity:
// 'info' < 'warn' < 'error' < 'critical'. Lowercase string literals match
// what the Go reservoir emits in Wails event payloads.
export type Severity = 'info' | 'warn' | 'error' | 'critical'

// Persistence is the UI-routing axis. Auto-dismiss behaviour is derived from
// it: one-shot toasts auto-dismiss after 6s; ongoing banners stay until Go
// dismisses them (DismissBanner / ReplaceBannersByPrefix); in-flight is
// rendered by RunOutput on a per-run basis, not by this stack, so entries
// with that persistence are dropped here; catastrophic renders as a full-
// screen pane.
export type Persistence = 'one-shot' | 'ongoing' | 'in-flight' | 'catastrophic'

// Source matches the Go notify.Source string ("backend" | "python" | "frontend").
export type NotificationSource = 'backend' | 'python' | 'frontend'

export interface Notification {
  id: string
  severity: Severity
  persistence: Persistence
  source?: NotificationSource
  title?: string
  message: string
  scriptID?: string
  runID?: string
  traceback?: string
  // key dedupes ongoing banners. For ongoing entries with the same key,
  // re-adding replaces the prior entry instead of stacking.
  key?: string
  createdAt: number
  // autoDismissMs reflects what the renderer should use. Derived from
  // persistence by default; can be overridden via NotificationInput.
  autoDismissMs?: number
}

export interface NotificationInput {
  severity: Severity
  persistence: Persistence
  source?: NotificationSource
  title?: string
  message: string
  scriptID?: string
  runID?: string
  traceback?: string
  key?: string
  // Optional override for the persistence-derived auto-dismiss window.
  autoDismissMs?: number
}

interface NotificationsAPI {
  notifications: Notification[]
  addNotification: (n: NotificationInput) => string
  dismissNotification: (id: string) => void
  // dismissByKey clears every ongoing notification with the given key. Used
  // by the central notify channel when Go emits notify:banner:dismiss.
  dismissByKey: (key: string) => void
  // replaceOngoingBanners atomically swaps every ongoing notification for
  // the given snapshot. Used by the central notify channel when Go emits
  // notify:banners:list (full snapshot for reconnect / refresh).
  replaceOngoingBanners: (snapshot: NotificationInput[]) => void
}

const ONE_SHOT_DEFAULT_MS = 6000

// autoDismissForPersistence returns the default auto-dismiss window for
// each persistence value. Entries whose result is 0 stay mounted until
// explicitly dismissed.
function autoDismissForPersistence(p: Persistence): number {
  switch (p) {
    case 'one-shot':
      return ONE_SHOT_DEFAULT_MS
    case 'ongoing':
    case 'in-flight':
    case 'catastrophic':
      return 0
  }
}

const NotificationsContext = createContext<NotificationsAPI | null>(null)

export function NotificationsProvider({ children }: { children: ReactNode }) {
  const [notifications, setNotifications] = useState<Notification[]>([])
  const timersRef = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map())

  const clearTimer = useCallback((id: string) => {
    const timer = timersRef.current.get(id)
    if (timer) {
      clearTimeout(timer)
      timersRef.current.delete(id)
    }
  }, [])

  const dismissNotification = useCallback((id: string) => {
    clearTimer(id)
    setNotifications(prev => prev.filter(n => n.id !== id))
  }, [clearTimer])

  const dismissByKey = useCallback((key: string) => {
    if (!key) return
    setNotifications(prev => {
      const kept: Notification[] = []
      for (const n of prev) {
        if (n.key === key) {
          clearTimer(n.id)
        } else {
          kept.push(n)
        }
      }
      return kept
    })
  }, [clearTimer])

  const addNotification = useCallback((input: NotificationInput): string => {
    const id = crypto.randomUUID()
    const autoDismissMs = input.autoDismissMs ?? autoDismissForPersistence(input.persistence)
    const notification: Notification = {
      ...input,
      id,
      createdAt: Date.now(),
      autoDismissMs,
    }
    // in-flight is owned by RunOutput; never enters the global stack.
    if (input.persistence === 'in-flight') {
      return id
    }
    setNotifications(prev => {
      // Ongoing banners with a key dedupe — same key replaces the prior entry.
      if (input.persistence === 'ongoing' && input.key) {
        const existing = prev.find(n => n.persistence === 'ongoing' && n.key === input.key)
        if (existing) {
          clearTimer(existing.id)
          return prev.map(n => n.id === existing.id ? notification : n)
        }
      }
      return [...prev, notification]
    })
    if (autoDismissMs > 0) {
      const timer = setTimeout(() => {
        timersRef.current.delete(id)
        setNotifications(prev => prev.filter(n => n.id !== id))
      }, autoDismissMs)
      timersRef.current.set(id, timer)
    }
    return id
  }, [clearTimer])

  const replaceOngoingBanners = useCallback((snapshot: NotificationInput[]) => {
    const replacements: Notification[] = snapshot.map(input => ({
      ...input,
      // Snapshots from Go are always ongoing banners — coerce to be defensive.
      persistence: 'ongoing' as const,
      id: crypto.randomUUID(),
      createdAt: Date.now(),
      autoDismissMs: 0,
    }))
    setNotifications(prev => {
      const kept: Notification[] = []
      for (const n of prev) {
        if (n.persistence === 'ongoing') {
          clearTimer(n.id)
        } else {
          kept.push(n)
        }
      }
      return [...kept, ...replacements]
    })
  }, [clearTimer])

  return (
    <NotificationsContext.Provider value={{ notifications, addNotification, dismissNotification, dismissByKey, replaceOngoingBanners }}>
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
