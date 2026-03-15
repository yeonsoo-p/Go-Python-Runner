import { useState } from 'react'
import type { Script, RunState } from '../hooks/useScripts'
import ParamForm from './ParamForm'
import RunOutput from './RunOutput'

interface TaskCardProps {
  script: Script
  runs: RunState[]
  onStartRun: (scriptID: string, params: Record<string, string>) => void
  onCancelRun: (runID: string) => void
}

function TaskCard({ script, runs, onStartRun, onCancelRun }: TaskCardProps) {
  const [expanded, setExpanded] = useState(false)
  const latestRun = runs[runs.length - 1]
  const isRunning = latestRun?.status === 'running'

  const statusColor = latestRun
    ? latestRun.status === 'completed' ? 'bg-green-500'
    : latestRun.status === 'failed' ? 'bg-red-500'
    : 'bg-yellow-500'
    : 'bg-slate-600'

  return (
    <div className="bg-slate-800 rounded-lg border border-slate-700 overflow-hidden">
      <div
        className="p-4 cursor-pointer hover:bg-slate-750 transition"
        onClick={() => setExpanded(!expanded)}
      >
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <h3 className="font-semibold text-lg">{script.name}</h3>
            {script.source === 'plugin' && (
              <span className="text-xs px-2 py-0.5 rounded bg-purple-600 text-purple-100">
                plugin
              </span>
            )}
          </div>
          <div className="flex items-center gap-2">
            {latestRun && (
              <span className={`inline-block w-2 h-2 rounded-full ${statusColor}`} />
            )}
            <span className="text-sm text-slate-400">
              {expanded ? '\u25B2' : '\u25BC'}
            </span>
          </div>
        </div>
        <p className="text-sm text-slate-400 mt-1">{script.description}</p>
      </div>

      {expanded && (
        <div className="border-t border-slate-700 p-4 space-y-4">
          <ParamForm
            params={script.params}
            onSubmit={(params) => onStartRun(script.id, params)}
            disabled={isRunning}
          />

          {isRunning && latestRun && (
            <button
              onClick={() => onCancelRun(latestRun.runID)}
              className="px-3 py-1 text-sm rounded bg-red-600 hover:bg-red-500 transition"
            >
              Cancel
            </button>
          )}

          {latestRun && <RunOutput run={latestRun} />}
        </div>
      )}
    </div>
  )
}

export default TaskCard
