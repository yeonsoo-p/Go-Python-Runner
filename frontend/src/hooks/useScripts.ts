import { useState, useEffect, useCallback } from 'react'

// Import generated types from Wails bindings
import type { Script, Param } from '../../bindings/go-python-runner/internal/registry/models'

export type { Script, Param }

export interface RunState {
  runID: string
  scriptID: string
  status: string
  output: string[]
  progress: { current: number; total: number; label: string } | null
  error: { message: string; traceback: string } | null
}

interface OutputEvent { runID: string; scriptID: string; text: string }
interface ProgressEvent { runID: string; scriptID: string; current: number; total: number; label: string }
interface StatusEvent { runID: string; scriptID: string; state: string }
interface ErrorEvent { runID: string; scriptID: string; message: string; traceback: string }

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
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [])

  useEffect(() => {
    let cleanup: (() => void)[] = []

    async function setupEvents() {
      try {
        const { Events } = await import('@wailsio/runtime')

        const onOutput = Events.On('run:output', (ev) => {
          const data = ev.data as OutputEvent
          setRuns(prev => {
            const next = new Map(prev)
            const run = next.get(data.runID) || {
              runID: data.runID, scriptID: data.scriptID,
              status: 'running', output: [], progress: null, error: null,
            }
            run.output = [...run.output, data.text]
            next.set(data.runID, run)
            return next
          })
        })

        const onProgress = Events.On('run:progress', (ev) => {
          const data = ev.data as ProgressEvent
          setRuns(prev => {
            const next = new Map(prev)
            const run = next.get(data.runID) || {
              runID: data.runID, scriptID: data.scriptID,
              status: 'running', output: [], progress: null, error: null,
            }
            run.progress = { current: data.current, total: data.total, label: data.label }
            next.set(data.runID, run)
            return next
          })
        })

        const onStatus = Events.On('run:status', (ev) => {
          const data = ev.data as StatusEvent
          setRuns(prev => {
            const next = new Map(prev)
            const run = next.get(data.runID) || {
              runID: data.runID, scriptID: data.scriptID,
              status: data.state, output: [], progress: null, error: null,
            }
            run.status = data.state
            next.set(data.runID, run)
            return next
          })
        })

        const onError = Events.On('run:error', (ev) => {
          const data = ev.data as ErrorEvent
          setRuns(prev => {
            const next = new Map(prev)
            const run = next.get(data.runID) || {
              runID: data.runID, scriptID: data.scriptID,
              status: 'failed', output: [], progress: null, error: null,
            }
            run.error = { message: data.message, traceback: data.traceback }
            run.status = 'failed'
            next.set(data.runID, run)
            return next
          })
        })

        cleanup = [onOutput, onProgress, onStatus, onError].map(unsub => () => unsub())
      } catch (e) {
        console.warn('Failed to set up events:', e)
      }
    }

    setupEvents()
    return () => cleanup.forEach(fn => fn())
  }, [])

  const startRun = useCallback(async (scriptID: string, params: Record<string, string>) => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      if (bindings.RunnerService?.StartRun) {
        const runID = await bindings.RunnerService.StartRun(scriptID, params)
        setRuns(prev => {
          const next = new Map(prev)
          next.set(runID, {
            runID, scriptID, status: 'running', output: [], progress: null, error: null,
          })
          return next
        })
        return runID
      }
    } catch (e) {
      console.error('Failed to start run:', e)
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
      console.error('Failed to cancel run:', e)
    }
  }, [])

  return { scripts, runs, loading, loadError, startRun, cancelRun }
}
