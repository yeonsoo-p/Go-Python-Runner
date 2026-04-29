import { useCallback, useEffect, useState } from 'react'
import type { EnvInfo, Package } from '../../bindings/go-python-runner/internal/services/models'
import { useNotifications } from './useNotifications'

export type { EnvInfo, Package }

// One line streamed from the running pip/uv subprocess. Keeps stream so the
// pane can colorize stderr differently.
export interface OperationLogLine {
  stream: 'stdout' | 'stderr'
  line: string
}

// loadIndexURL persists the user's preferred index URL across sessions.
// Frontend-owned UI preference; backend doesn't store it (per philosophy
// alignment in the plan).
const INDEX_URL_KEY = 'env.indexURL'

function loadIndexURL(): string {
  try {
    return window.localStorage.getItem(INDEX_URL_KEY) ?? ''
  } catch {
    return ''
  }
}

function saveIndexURL(value: string) {
  try {
    if (value) {
      window.localStorage.setItem(INDEX_URL_KEY, value)
    } else {
      window.localStorage.removeItem(INDEX_URL_KEY)
    }
  } catch {
    // localStorage unavailable (private mode etc) — best effort.
  }
}

export function useEnv() {
  const [info, setInfo] = useState<EnvInfo | null>(null)
  const [packages, setPackages] = useState<Package[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [logLines, setLogLines] = useState<OperationLogLine[]>([])
  const [indexURL, setIndexURLState] = useState<string>(() => loadIndexURL())
  const [available, setAvailable] = useState(true)
  const { addNotification } = useNotifications()

  const setIndexURL = useCallback((value: string) => {
    setIndexURLState(value)
    saveIndexURL(value)
  }, [])

  const loadInfo = useCallback(async () => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      if (!bindings.EnvService?.GetEnvInfo) {
        setAvailable(false)
        return
      }
      const next = await bindings.EnvService.GetEnvInfo()
      setInfo(next ?? null)
    } catch {
      // Go's reservoir.Report already surfaced this via notify:toast.
      // Frontend catch is control flow only.
      setAvailable(false)
    }
  }, [])

  const loadPackages = useCallback(async () => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      if (!bindings.EnvService?.ListPackages) return
      const list = await bindings.EnvService.ListPackages()
      setPackages(list ?? [])
    } catch {
      // Go already surfaced via notify:toast.
    }
  }, [])

  // Initial fetch.
  useEffect(() => {
    void (async () => {
      await loadInfo()
      await loadPackages()
      setLoading(false)
    })()
  }, [loadInfo, loadPackages])

  // Subscribe to install/uninstall events: drive busy state, capture log
  // lines, refresh package list when an operation ends.
  useEffect(() => {
    let cleanup: Array<() => void> = []
    let aborted = false

    void (async () => {
      try {
        const { Events } = await import('@wailsio/runtime')
        if (aborted) return

        const onStart = Events.On('env:operation:start', () => {
          setBusy(true)
          setLogLines([])
        })
        const onLog = Events.On('env:operation:log', (ev) => {
          const data = ev.data as OperationLogLine
          if (data && typeof data.line === 'string') {
            setLogLines(prev => [...prev, data])
          }
        })
        const onEnd = Events.On('env:operation:end', () => {
          setBusy(false)
          // Errors flow through the central reservoir → notify:toast (handled
          // by useNotifyChannel). env:operation:end is now a pure lifecycle
          // signal; we just flip busy state and refresh the package list.
          void loadPackages()
        })

        cleanup = [onStart, onLog, onEnd].map(fn => () => fn())
      } catch {
        // Wails events unavailable; UI will fall back to manual refresh
        // (busy state stuck false). Surface elsewhere; not catastrophic.
      }
    })()

    return () => {
      aborted = true
      cleanup.forEach(fn => fn())
    }
  }, [loadPackages])

  const installPackage = useCallback(async (spec: string) => {
    if (!spec.trim()) {
      // Pure-frontend validation: Go never sees this, so we surface it here.
      addNotification({ severity: 'error', persistence: 'one-shot', source: 'frontend', message: 'Package spec cannot be empty' })
      return
    }
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      await bindings.EnvService.InstallPackage(spec, indexURL)
    } catch {
      // Go's reservoir.Report already surfaced via notify:toast.
    }
  }, [addNotification, indexURL])

  const installRequirements = useCallback(async (absPath: string) => {
    if (!absPath.trim()) {
      addNotification({ severity: 'error', persistence: 'one-shot', source: 'frontend', message: 'Requirements path cannot be empty' })
      return
    }
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      await bindings.EnvService.InstallRequirements(absPath, indexURL)
    } catch {
      // Go already surfaced via notify:toast.
    }
  }, [addNotification, indexURL])

  const uninstallPackage = useCallback(async (name: string) => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      await bindings.EnvService.UninstallPackage(name)
    } catch {
      // Go already surfaced via notify:toast.
    }
  }, [])

  return {
    info,
    packages,
    loading,
    busy,
    logLines,
    indexURL,
    available,
    setIndexURL,
    installPackage,
    installRequirements,
    uninstallPackage,
    refresh: loadPackages,
  }
}
