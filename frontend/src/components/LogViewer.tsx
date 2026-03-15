import { useState, useEffect, useRef } from 'react'

import type { LogEntry } from '../../bindings/go-python-runner/internal/logging/models'

function LogViewer() {
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [sourceFilter, setSourceFilter] = useState('')
  const [levelFilter, setLevelFilter] = useState('')
  const cleanupRef = useRef<(() => void) | null>(null)

  useEffect(() => {
    async function loadLogs() {
      try {
        const bindings = await import('../../bindings/go-python-runner/internal/services')
        if (bindings.LogService?.GetLogs) {
          // Fetch all logs; client-side filtering handles source/level
          // (streamed log:entry events bypass the backend, so filtering
          // must happen here anyway).
          const result = await bindings.LogService.GetLogs({
            Source: '',
            Level: '',
            RunID: '',
            ScriptID: '',
          })
          setLogs(result || [])
        }
      } catch (e) {
        console.warn('Failed to load logs:', e)
      }
    }
    loadLogs()
  }, [])

  useEffect(() => {
    async function setupEvents() {
      try {
        const { Events } = await import('@wailsio/runtime')
        const unsub = Events.On('log:entry', (ev) => {
          const data = ev.data as LogEntry
          setLogs(prev => [...prev.slice(-999), data])
        })
        cleanupRef.current = unsub
      } catch (e) {
        console.warn('Failed to set up log events:', e)
      }
    }
    setupEvents()

    return () => {
      cleanupRef.current?.()
    }
  }, [])

  const filteredLogs = logs.filter(log => {
    if (sourceFilter && log.Source !== sourceFilter) return false
    if (levelFilter && log.Level !== levelFilter) return false
    return true
  })

  const levelColor = (level: string) => {
    switch (level) {
      case 'ERROR': return 'text-red-400'
      case 'WARN': return 'text-yellow-400'
      case 'INFO': return 'text-blue-400'
      case 'DEBUG': return 'text-slate-500'
      default: return 'text-slate-400'
    }
  }

  return (
    <div className="p-4">
      <div className="flex items-center gap-4 mb-3">
        <h2 className="text-lg font-semibold">Logs</h2>
        <select
          value={sourceFilter}
          onChange={e => setSourceFilter(e.target.value)}
          className="text-sm px-2 py-1 rounded bg-slate-700 border border-slate-600"
        >
          <option value="">All Sources</option>
          <option value="frontend">Frontend</option>
          <option value="backend">Backend</option>
          <option value="python">Python</option>
        </select>
        <select
          value={levelFilter}
          onChange={e => setLevelFilter(e.target.value)}
          className="text-sm px-2 py-1 rounded bg-slate-700 border border-slate-600"
        >
          <option value="">All Levels</option>
          <option value="ERROR">Error</option>
          <option value="WARN">Warn</option>
          <option value="INFO">Info</option>
          <option value="DEBUG">Debug</option>
        </select>
      </div>

      <div className="bg-slate-900 rounded max-h-64 overflow-y-auto font-mono text-xs">
        {filteredLogs.length === 0 ? (
          <div className="p-4 text-slate-500 text-center">No logs</div>
        ) : (
          filteredLogs.map((log, i) => (
            <div key={i} className="px-3 py-1 border-b border-slate-800 flex gap-3">
              <span className={`font-bold ${levelColor(log.Level)}`}>{log.Level}</span>
              <span className="text-slate-500">[{log.Source}]</span>
              <span className="text-slate-300 flex-1">{log.Message}</span>
              {log.Traceback && (
                <details className="mt-1">
                  <summary className="text-red-400 cursor-pointer">traceback</summary>
                  <pre className="text-red-300 whitespace-pre-wrap mt-1">{log.Traceback}</pre>
                </details>
              )}
            </div>
          ))
        )}
      </div>
    </div>
  )
}

export default LogViewer
