import { useState } from 'react'
import type { Script, RunState, RunGroupState } from '../hooks/useScripts'
import ParamForm from './ParamForm'
import RunOutput from './RunOutput'
import AggregateRunPanel from './AggregateRunPanel'

interface TaskCardProps {
  script: Script
  runs: RunState[]
  // Groups for this script. The TaskCard renders an AggregateRunPanel for any
  // group whose runIDs are present in `runs`; ungrouped runs render as
  // standalone per-run cards.
  groups?: RunGroupState[]
  onStartRun: (scriptID: string, params: Record<string, string>, workerCount?: number) => void
  onCancelRun: (runID: string) => void
  onCancelGroup?: (groupID: string) => void
}

function TaskCard({ script, runs, groups = [], onStartRun, onCancelRun, onCancelGroup }: TaskCardProps) {
  const [expanded, setExpanded] = useState(false)
  const runsByID = new Map(runs.map(r => [r.runID, r]))
  const groupedRunIDs = new Set<string>()
  for (const g of groups) for (const id of g.runIDs) groupedRunIDs.add(id)

  const ungroupedRuns = runs.filter(r => !groupedRunIDs.has(r.runID))
  const activeRuns = runs.filter((r) => r.status === 'running')
  const latestFinishedUngrouped = [...ungroupedRuns].reverse().find((r) => r.status !== 'running')
  const latestRun = runs[runs.length - 1]

  const statusColor = latestRun
    ? latestRun.status === 'completed' ? 'bg-green-500'
    : latestRun.status === 'failed' ? 'bg-red-500'
    : latestRun.status === 'cancelled' ? 'bg-slate-500'
    : 'bg-yellow-500'
    : 'bg-slate-600'

  // Header chip: if a group is live, surface "{N} workers"; otherwise the
  // pre-existing "{N} running" count for ungrouped parallel runs.
  const headerChip = groups.length > 0
    ? `${groups[0].runIDs.length} workers`
    : (activeRuns.length > 1 ? `${activeRuns.length} running` : null)

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
            {headerChip && (
              <span className="text-xs px-2 py-0.5 rounded bg-blue-600 text-blue-100">
                {headerChip}
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
            parallel={script.parallel}
            onSubmit={(params, workerCount) => onStartRun(script.id, params, workerCount)}
          />

          {groups.map((g) => {
            const groupRuns = g.runIDs
              .map(id => runsByID.get(id))
              .filter((r): r is RunState => r !== undefined)
            if (groupRuns.length === 0) return null
            return (
              <AggregateRunPanel
                key={g.groupID}
                group={g}
                runs={groupRuns}
                onCancelGroup={onCancelGroup ?? (() => {})}
                onCancelRun={onCancelRun}
              />
            )
          })}

          {ungroupedRuns.filter(r => r.status === 'running').map((run) => (
            <div key={run.runID} className="border border-slate-600 rounded-lg p-3 space-y-3">
              <div className="flex items-center justify-between">
                <span className="text-xs text-slate-400 font-mono">{run.runID.slice(0, 8)}</span>
                <button
                  onClick={() => onCancelRun(run.runID)}
                  className="px-3 py-1 text-sm rounded bg-red-600 hover:bg-red-500 transition"
                >
                  Cancel
                </button>
              </div>
              <RunOutput run={run} />
            </div>
          ))}

          {latestFinishedUngrouped && <RunOutput run={latestFinishedUngrouped} />}
        </div>
      )}
    </div>
  )
}

export default TaskCard
