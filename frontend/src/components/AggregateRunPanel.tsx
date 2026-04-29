import { useMemo, useState } from 'react'
import type { RunGroupState, RunState } from '../hooks/useScripts'
import RunOutput, { ProgressBar } from './RunOutput'

interface AggregateRunPanelProps {
  group: RunGroupState
  runs: RunState[]
  onCancelGroup: (groupID: string) => void
  onCancelRun: (runID: string) => void
}

function AggregateRunPanel({ group, runs, onCancelGroup, onCancelRun }: AggregateRunPanelProps) {
  const [expanded, setExpanded] = useState(false)

  const { current, total, label } = useMemo(() => {
    let current = 0
    let total = 0
    let label = ''
    for (const r of runs) {
      if (r.progress) {
        current += r.progress.current
        total += r.progress.total
        if (!label && r.progress.label) label = r.progress.label
      }
    }
    return { current, total, label }
  }, [runs])

  const counts = useMemo(() => {
    let running = 0, done = 0, failed = 0
    for (const r of runs) {
      if (r.status === 'running') running++
      else if (r.status === 'completed') done++
      else if (r.status === 'failed') failed++
    }
    return { running, done, failed }
  }, [runs])

  const hasRunning = counts.running > 0

  return (
    <div className="border border-slate-600 rounded-lg p-3 space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-xs text-slate-400 font-mono">
          group {group.groupID.slice(0, 8)} · {group.runIDs.length} workers
        </span>
        <button
          onClick={() => onCancelGroup(group.groupID)}
          disabled={!hasRunning}
          className="px-3 py-1 text-sm rounded bg-red-600 hover:bg-red-500 transition disabled:bg-slate-700 disabled:text-slate-500 disabled:cursor-not-allowed"
        >
          Cancel all
        </button>
      </div>

      <ProgressBar current={current} total={total} label={label || 'Aggregate'} />

      <div className="flex items-center gap-2 text-xs">
        <span className="px-2 py-0.5 rounded bg-yellow-900 text-yellow-300">
          {counts.running} running
        </span>
        <span className="px-2 py-0.5 rounded bg-green-900 text-green-300">
          {counts.done} done
        </span>
        <span className="px-2 py-0.5 rounded bg-red-900 text-red-300">
          {counts.failed} failed
        </span>
      </div>

      <button
        onClick={() => setExpanded(!expanded)}
        className="text-xs text-slate-400 hover:text-slate-200 transition"
      >
        {expanded ? '▾' : '▸'} {expanded ? 'Hide' : 'Show'} per-worker detail
      </button>

      {expanded && (
        <div className="space-y-3 pl-3 border-l-2 border-slate-700">
          {runs.map((run) => (
            <div key={run.runID} className="space-y-2">
              <div className="flex items-center justify-between">
                <span className="text-xs text-slate-400 font-mono">{run.runID.slice(0, 8)}</span>
                {run.status === 'running' && (
                  <button
                    onClick={() => onCancelRun(run.runID)}
                    className="px-2 py-0.5 text-xs rounded bg-red-600 hover:bg-red-500 transition"
                  >
                    Cancel
                  </button>
                )}
              </div>
              <RunOutput run={run} />
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export default AggregateRunPanel
