import { useState, useEffect, useCallback } from 'react'

// Import generated types from Wails bindings
import type { Script, Param } from '../../bindings/go-python-runner/internal/registry/models'

export type { Script, Param }

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

  useEffect(() => {
    async function load() {
      try {
        const bindings = await import('../../bindings/go-python-runner/internal/services')
        if (bindings.ScriptService?.ListScripts) {
          const result = await bindings.ScriptService.ListScripts()
          setScripts(result || [])
        }
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e)
        setLoadError(`Failed to load scripts: ${msg}`)
        try {
          const svc = await import('../../bindings/go-python-runner/internal/services')
          svc.LogService?.LogError?.('frontend', `Failed to load scripts: ${msg}`, {})
        } catch { /* bindings not available */ }
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [])

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
        console.warn('Failed to set up events:', e)
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
  }, [])

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
      console.error('Failed to start run:', e)
      // Report to backend logging
      try {
        const svc = await import('../../bindings/go-python-runner/internal/services')
        svc.LogService?.LogError?.('frontend', `Failed to start run: ${msg}`, { scriptID })
      } catch { /* bindings not available */ }
      // Surface error in run state so UI can display it
      const errorRunID = `error-${crypto.randomUUID()}`
      setRuns(prev => {
        const next = new Map(prev)
        next.set(errorRunID, {
          runID: errorRunID, scriptID, status: 'failed', output: [],
          progress: null, error: { message: `Failed to start: ${msg}`, traceback: '' }, data: null,
        })
        return next
      })
    }
    return null
  }, [])

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
      console.error('Failed to start parallel runs:', e)
      try {
        const svc = await import('../../bindings/go-python-runner/internal/services')
        svc.LogService?.LogError?.('frontend', `Failed to start parallel runs: ${msg}`, { scriptID })
      } catch { /* bindings not available */ }
      const errorRunID = `error-${crypto.randomUUID()}`
      setRuns(prev => {
        const next = new Map(prev)
        next.set(errorRunID, {
          runID: errorRunID, scriptID, status: 'failed', output: [],
          progress: null, error: { message: `Failed to start parallel runs: ${msg}`, traceback: '' }, data: null,
        })
        return next
      })
    }
    return null
  }, [])

  const cancelRun = useCallback(async (runID: string) => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      if (bindings.RunnerService?.CancelRun) {
        await bindings.RunnerService.CancelRun(runID)
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      console.error('Failed to cancel run:', e)
      try {
        const svc = await import('../../bindings/go-python-runner/internal/services')
        svc.LogService?.LogError?.('frontend', `Failed to cancel run: ${msg}`, { runID })
      } catch { /* bindings not available */ }
    }
  }, [])

  return { scripts, runs, loading, loadError, startRun, startParallelRuns, cancelRun }
}
