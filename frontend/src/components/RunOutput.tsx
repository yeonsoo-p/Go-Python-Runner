import type { RunState } from '../hooks/useScripts'

interface RunOutputProps {
  run: RunState
}

function RunOutput({ run }: RunOutputProps) {
  return (
    <div className="space-y-3">
      {/* Progress bar */}
      {run.progress && (
        <div>
          <div className="flex justify-between text-sm text-slate-400 mb-1">
            <span>{run.progress.label}</span>
            <span>{run.progress.current}/{run.progress.total}</span>
          </div>
          <div className="w-full bg-slate-700 rounded-full h-2">
            <div
              className="bg-blue-500 h-2 rounded-full transition-all duration-300"
              style={{ width: `${(run.progress.current / run.progress.total) * 100}%` }}
            />
          </div>
        </div>
      )}

      {/* Status badge */}
      <div className="flex items-center gap-2">
        <span className="text-xs font-medium text-slate-400">Status:</span>
        <span className={`text-xs px-2 py-0.5 rounded ${
          run.status === 'completed' ? 'bg-green-900 text-green-300'
          : run.status === 'failed' ? 'bg-red-900 text-red-300'
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

      {/* Data results */}
      {run.data && Object.keys(run.data).length > 0 && (
        <div className="bg-slate-800 border border-slate-600 rounded p-3">
          <p className="text-xs text-slate-400 font-medium mb-2">Data Results</p>
          {Object.entries(run.data).map(([key, value]) => (
            <div key={key} className="flex gap-2 text-sm">
              <span className="text-slate-400">{key}:</span>
              <span className="text-slate-300 font-mono truncate" title={value}>
                {value.length > 64 ? `${value.slice(0, 64)}...` : value}
              </span>
            </div>
          ))}
        </div>
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
