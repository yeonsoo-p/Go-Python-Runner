import { useState, useEffect, useCallback } from 'react'

// Import generated types from Wails bindings
import type { Script, Param, LoadIssue } from '../../bindings/go-python-runner/internal/registry/models'
import { useNotifications } from './useNotifications'

export type { Script, Param, LoadIssue }

// Banner key for the "live updates broken" condition. Frontend-originated
// ongoing banner: dispatched when Wails event setup fails so the user knows
// real-time output / progress / status events won't update — even though the
// rest of the app keeps working.
const LIVE_UPDATES_BROKEN_KEY = 'live-updates-broken'

export type RunStatus = 'running' | 'completed' | 'failed'

export interface RunState {
  runID: string
  scriptID: string
  status: RunStatus
  output: string[]
  progress: { current: number; total: number; label: string } | null
  error: { message: string; traceback: string } | null
  data: Record<string, string> | null
}

interface OutputEvent { runID: string; scriptID: string; text: string }
interface ProgressEvent { runID: string; scriptID: string; current: number; total: number; label: string }
interface StatusEvent { runID: string; scriptID: string; state: RunStatus }
interface ErrorEvent { runID: string; scriptID: string; message: string; traceback: string }
interface DataEvent { runID: string; scriptID: string; key: string; value: string }

export function useScripts() {
  const [scripts, setScripts] = useState<Script[]>([])
  const [runs, setRuns] = useState<Map<string, RunState>>(new Map())
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const { addNotification } = useNotifications()

  // loadCatalog fetches scripts only. Plugin LoadIssues no longer flow
  // through this polling read — they arrive as ongoing banners on the
  // notify:banners:list channel and render through the central notification
  // stack.
  const loadCatalog = useCallback(async () => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      const nextScripts = await (bindings.ScriptService?.ListScripts?.() ?? Promise.resolve([]))
      setScripts(nextScripts || [])
      setLoadError(null)
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      // Catastrophic: app cannot function without scripts. Surface inline,
      // not as a toast — user needs to see this prominently.
      setLoadError(`Failed to load scripts: ${msg}`)
      try {
        const svc = await import('../../bindings/go-python-runner/internal/services')
        svc.LogService?.LogError?.('frontend', `Failed to load scripts: ${msg}`, {})
      } catch { /* bindings not available */ }
    }
  }, [])

  useEffect(() => {
    void (async () => {
      await loadCatalog()
      setLoading(false)
    })()
  }, [loadCatalog])

  // Hot reload: when Go's filesystem watcher detects a change, re-fetch
  // the catalog. Pure invalidation — the event carries no payload.
  useEffect(() => {
    let unsub: (() => void) | null = null
    let aborted = false
    void (async () => {
      try {
        const { Events } = await import('@wailsio/runtime')
        if (aborted) return
        unsub = Events.On('scripts:changed', () => { void loadCatalog() })
      } catch {
        // If Wails events fail here, the persistent banner is already raised
        // by the run-events useEffect below; no separate signal needed.
      }
    })()
    return () => {
      aborted = true
      if (unsub) unsub()
    }
  }, [loadCatalog])

  useEffect(() => {
    let cleanup: (() => void)[] = []
    let aborted = false

    async function setupEvents() {
      try {
        const { Events } = await import('@wailsio/runtime')

        // If component unmounted during the async import, clean up immediately.
        if (aborted) return

        // Factory to reduce duplication across the four run event handlers.
        function onRunEvent<T extends { runID: string; scriptID: string }>(
          event: string,
          defaultStatus: RunStatus,
          updater: (run: RunState, data: T) => void,
        ) {
          return Events.On(event, (ev) => {
            const data = ev.data as T
            setRuns(prev => {
              const next = new Map(prev)
              const run = next.get(data.runID) || {
                runID: data.runID, scriptID: data.scriptID,
                status: defaultStatus, output: [], progress: null, error: null, data: null,
              }
              updater(run, data)
              next.set(data.runID, run)
              return next
            })
          })
        }

        const onOutput = onRunEvent<OutputEvent>('run:output', 'running', (run, data) => {
          run.output = [...run.output, data.text]
        })
        const onProgress = onRunEvent<ProgressEvent>('run:progress', 'running', (run, data) => {
          run.progress = { current: data.current, total: data.total, label: data.label }
        })
        // Go is the sole emitter of run:status — frontend just renders.
        // Manager guarantees exactly one terminal status event per run.
        const onStatus = onRunEvent<StatusEvent>('run:status', 'running', (run, data) => {
          run.status = data.state
        })
        // Error events carry content (message + traceback). Status is set
        // separately by the run:status event from Manager — onError doesn't
        // touch run.status anymore. (Frontend "shows", Go "manages".)
        const onError = onRunEvent<ErrorEvent>('run:error', 'running', (run, data) => {
          run.error = { message: data.message, traceback: data.traceback }
        })
        const onData = onRunEvent<DataEvent>('run:data', 'running', (run, data) => {
          // Go []byte is JSON-marshaled as a base64 string by Wails.
          run.data = { ...(run.data || {}), [data.key]: data.value }
        })

        cleanup = [onOutput, onProgress, onStatus, onError, onData].map(unsub => () => unsub())
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e)
        // Persistent: live updates broken but the app can still launch scripts.
        // Dispatched as an ongoing banner with a stable key so the central
        // notification stack handles it like any other persistent condition.
        addNotification({
          severity: 'warn',
          persistence: 'ongoing',
          source: 'frontend',
          key: LIVE_UPDATES_BROKEN_KEY,
          title: 'Live updates unavailable',
          message: `Script output may not refresh in real time: ${msg}`,
        })
        try {
          const svc = await import('../../bindings/go-python-runner/internal/services')
          svc.LogService?.LogError?.('frontend', `Failed to set up Wails event listeners: ${msg}`, {})
        } catch { /* bindings not available */ }
      }
    }

    setupEvents()
    return () => {
      aborted = true
      cleanup.forEach(fn => fn())
    }
  }, [addNotification])

  // Transient action failures (StartRun / StartParallelRuns / CancelRun threw)
  // are reported as toasts. The runs Map only ever contains real Manager-issued
  // runIDs — the frontend no longer fabricates pseudo-IDs for failure-only display.
  const reportTransient = useCallback((message: string, ctx: { scriptID?: string; runID?: string }) => {
    addNotification({ severity: 'error', persistence: 'one-shot', source: 'frontend', message, scriptID: ctx.scriptID, runID: ctx.runID })
    import('../../bindings/go-python-runner/internal/services')
      .then(svc => svc.LogService?.LogError?.('frontend', message, {
        ...(ctx.scriptID ? { scriptID: ctx.scriptID } : {}),
        ...(ctx.runID ? { runID: ctx.runID } : {}),
      }))
      .catch(() => { /* bindings not available */ })
  }, [addNotification])

  const startRun = useCallback(async (scriptID: string, params: Record<string, string>) => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      if (bindings.RunnerService?.StartRun) {
        const runID = await bindings.RunnerService.StartRun(scriptID, params)
        setRuns(prev => {
          const next = new Map(prev)
          // Don't overwrite if event handlers already populated this run
          // (fast scripts can emit events before StartRun returns).
          if (!next.has(runID)) {
            next.set(runID, {
              runID, scriptID, status: 'running', output: [], progress: null, error: null, data: null,
            })
          }
          return next
        })
        return runID
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      reportTransient(`Failed to start run: ${msg}`, { scriptID })
    }
    return null
  }, [reportTransient])

  const startParallelRuns = useCallback(async (scriptID: string, params: Record<string, string>, workerCount: number) => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      if (bindings.RunnerService?.StartParallelRuns) {
        const runIDs: string[] = await bindings.RunnerService.StartParallelRuns(scriptID, params, workerCount)
        setRuns(prev => {
          const next = new Map(prev)
          for (const runID of runIDs) {
            if (!next.has(runID)) {
              next.set(runID, {
                runID, scriptID, status: 'running', output: [], progress: null, error: null, data: null,
              })
            }
          }
          return next
        })
        return runIDs
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      reportTransient(`Failed to start parallel runs: ${msg}`, { scriptID })
    }
    return null
  }, [reportTransient])

  const cancelRun = useCallback(async (runID: string) => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      if (bindings.RunnerService?.CancelRun) {
        await bindings.RunnerService.CancelRun(runID)
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      reportTransient(`Failed to cancel run: ${msg}`, { runID })
    }
  }, [reportTransient])

  return { scripts, runs, loading, loadError, startRun, startParallelRuns, cancelRun }
}
