import { useEffect } from 'react'
import { useNotifications } from './useNotifications'
import type { NotificationInput, NotificationSource, Severity } from './useNotifications'

// useNotifyChannel is the only place in the frontend that subscribes to the
// notify:* Wails events. Translates each Go reservoir emission into a call
// on useNotifications. Mount this once near the root (App.tsx).
//
// Channel summary:
//   notify:toast         → addNotification(persistence='one-shot')
//   notify:banner        → addNotification(persistence='ongoing'); deduped by key
//   notify:banner:dismiss → dismissByKey
//   notify:critical      → addNotification(persistence='catastrophic')
//   notify:banners:list  → replaceOngoingBanners (atomic snapshot from Go)
//
// run:error and log:entry are NOT consumed here — they pre-date the reservoir
// and have their own consumers (RunOutput per-run pane and LogViewer
// respectively).

interface ToastPayload {
  id?: string
  severity?: Severity
  source?: NotificationSource
  title?: string
  message?: string
  runID?: string
  scriptID?: string
  traceback?: string
  key?: string
}

interface BannersListPayload {
  banners?: ToastPayload[]
}

interface DismissPayload {
  key?: string
}

function payloadToInput(p: ToastPayload, persistence: NotificationInput['persistence']): NotificationInput {
  return {
    severity: p.severity ?? 'error',
    persistence,
    source: p.source,
    title: p.title,
    message: p.message ?? '',
    runID: p.runID,
    scriptID: p.scriptID,
    traceback: p.traceback,
    key: p.key,
  }
}

export function useNotifyChannel() {
  const { addNotification, dismissByKey, replaceOngoingBanners } = useNotifications()

  useEffect(() => {
    let aborted = false
    let cleanups: Array<() => void> = []

    ;(async () => {
      try {
        const { Events } = await import('@wailsio/runtime')
        if (aborted) return

        const onToast = Events.On('notify:toast', (ev) => {
          addNotification(payloadToInput((ev.data as ToastPayload) ?? {}, 'one-shot'))
        })
        const onBanner = Events.On('notify:banner', (ev) => {
          addNotification(payloadToInput((ev.data as ToastPayload) ?? {}, 'ongoing'))
        })
        const onBannerDismiss = Events.On('notify:banner:dismiss', (ev) => {
          const data = (ev.data as DismissPayload) ?? {}
          if (data.key) dismissByKey(data.key)
        })
        const onCritical = Events.On('notify:critical', (ev) => {
          addNotification(payloadToInput((ev.data as ToastPayload) ?? {}, 'catastrophic'))
        })
        const onBannersList = Events.On('notify:banners:list', (ev) => {
          const data = (ev.data as BannersListPayload) ?? {}
          const inputs = (data.banners ?? []).map(p => payloadToInput(p, 'ongoing'))
          replaceOngoingBanners(inputs)
        })

        cleanups = [onToast, onBanner, onBannerDismiss, onCritical, onBannersList].map(fn => () => fn())
      } catch {
        // Wails events unavailable (e.g. browser dev mode without runtime);
        // notifications still work for direct addNotification callers.
      }
    })()

    return () => {
      aborted = true
      cleanups.forEach(fn => fn())
    }
  }, [addNotification, dismissByKey, replaceOngoingBanners])
}
