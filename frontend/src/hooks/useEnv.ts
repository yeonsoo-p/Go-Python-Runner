import { useCallback, useEffect, useState } from 'react'
import type { EnvInfo, Package } from '../../bindings/go-python-runner/internal/services/models'
import { useNotifications } from './useNotifications'

export type { EnvInfo, Package }

// One line streamed from pip/uv. stream lets the pane colorize stderr.
export interface OperationLogLine {
  stream: 'stdout' | 'stderr'
  line: string
}

// Frontend-owned UI preference; backend doesn't store it.
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

  // Wails-bound action failures are surfaced by Go via reservoir.Report →
  // notify:toast. Frontend catches are control flow only.
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
      setAvailable(false)
    }
  }, [])

  const loadPackages = useCallback(async () => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      if (!bindings.EnvService?.ListPackages) return
      const list = await bindings.EnvService.ListPackages()
      setPackages(list ?? [])
    } catch { /* Go surfaced via notify:toast */ }
  }, [])

  useEffect(() => {
    void (async () => {
      await loadInfo()
      await loadPackages()
      setLoading(false)
    })()
  }, [loadInfo, loadPackages])

  // env:operation:end is a pure lifecycle signal — errors flow through
  // notify:toast via useNotifyChannel.
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
          void loadPackages()
        })

        cleanup = [onStart, onLog, onEnd].map(fn => () => fn())
      } catch { /* Wails events unavailable; manual refresh still works */ }
    })()

    return () => {
      aborted = true
      cleanup.forEach(fn => fn())
    }
  }, [loadPackages])

  // Pure-frontend validation surfaces here because Go never sees these.
  const installPackage = useCallback(async (spec: string) => {
    if (!spec.trim()) {
      addNotification({ severity: 'error', persistence: 'one-shot', source: 'frontend', message: 'Package spec cannot be empty' })
      return
    }
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      await bindings.EnvService.InstallPackage(spec, indexURL)
    } catch { /* Go surfaced via notify:toast */ }
  }, [addNotification, indexURL])

  const installRequirements = useCallback(async (absPath: string) => {
    if (!absPath.trim()) {
      addNotification({ severity: 'error', persistence: 'one-shot', source: 'frontend', message: 'Requirements path cannot be empty' })
      return
    }
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      await bindings.EnvService.InstallRequirements(absPath, indexURL)
    } catch { /* Go surfaced via notify:toast */ }
  }, [addNotification, indexURL])

  const uninstallPackage = useCallback(async (name: string) => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      await bindings.EnvService.UninstallPackage(name)
    } catch { /* Go surfaced via notify:toast */ }
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
