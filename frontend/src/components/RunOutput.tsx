import type { RunState } from '../hooks/useScripts'

interface ProgressBarProps {
  current: number
  total: number
  label?: string
}

// ProgressBar is the shared bar markup used by RunOutput (per-run) and
// AggregateRunPanel (group rollup). One source of truth for the Tailwind
// classes — keep the visuals identical across both surfaces.
export function ProgressBar({ current, total, label }: ProgressBarProps) {
  const pct = total > 0 ? Math.max(0, Math.min(100, (current / total) * 100)) : 0
  return (
    <div>
      <div className="flex justify-between text-sm text-slate-400 mb-1">
        <span>{label ?? ''}</span>
        <span>{current}/{total}</span>
      </div>
      <div className="w-full bg-slate-700 rounded-full h-2">
        <div
          className="bg-blue-500 h-2 rounded-full transition-all duration-300"
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}

interface RunOutputProps {
  run: RunState
}

function RunOutput({ run }: RunOutputProps) {
  return (
    <div className="space-y-3">
      {run.progress && (
        <ProgressBar
          current={run.progress.current}
          total={run.progress.total}
          label={run.progress.label}
        />
      )}

      {/* Status badge */}
      <div className="flex items-center gap-2">
        <span className="text-xs font-medium text-slate-400">Status:</span>
        <span className={`text-xs px-2 py-0.5 rounded ${
          run.status === 'completed' ? 'bg-green-900 text-green-300'
          : run.status === 'failed' ? 'bg-red-900 text-red-300'
          : run.status === 'cancelled' ? 'bg-slate-700 text-slate-300'
          : 'bg-yellow-900 text-yellow-300'
        }`}>
          {run.status}
        </span>
      </div>

      {/* Output lines */}
      {run.output.length > 0 && (
        <pre className="bg-slate-900 rounded p-3 text-sm text-slate-300 font-mono overflow-x-auto max-h-48 overflow-y-auto">
          {run.output.join('\n')}
        </pre>
      )}

      {/* Error display */}
      {run.error && (
        <div className="bg-red-900/30 border border-red-800 rounded p-3">
          <p className="text-sm text-red-300 font-medium">{run.error.message}</p>
          {run.error.traceback && (
            <details className="mt-2">
              <summary className="text-xs text-red-400 cursor-pointer">Traceback</summary>
              <pre className="mt-1 text-xs text-red-300 font-mono whitespace-pre-wrap">
                {run.error.traceback}
              </pre>
            </details>
          )}
        </div>
      )}
    </div>
  )
}

export default RunOutput
