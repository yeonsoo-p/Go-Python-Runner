import { useCallback, useState } from 'react'
import { useScripts } from '../hooks/useScripts'
import TaskCard from './TaskCard'
import LoadIssuesBanner from './LoadIssuesBanner'

function TaskGrid() {
  const { scripts, issues, runs, loading, loadError, liveUpdatesAvailable, startRun, startParallelRuns, cancelRun } = useScripts()
  const [issuesExpanded, setIssuesExpanded] = useState(false)

  const handleStartRun = useCallback(async (scriptID: string, params: Record<string, string>, workerCount?: number) => {
    if (workerCount && workerCount > 1) {
      await startParallelRuns(scriptID, params, workerCount)
    } else {
      await startRun(scriptID, params)
    }
  }, [startRun, startParallelRuns])

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64 text-slate-400">
        Loading scripts...
      </div>
    )
  }

  // Catastrophic tier: scripts couldn't be loaded — app cannot function.
  // Inline pane (not modal) so the user can read it without dismissing.
  if (loadError) {
    return (
      <div className="flex items-center justify-center h-64 text-red-400">
        {loadError}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {/* Persistent tier: live updates broken, app still usable. */}
      {!liveUpdatesAvailable && (
        <div className="rounded border border-yellow-700 bg-yellow-900/40 px-4 py-2 text-sm text-yellow-100">
          Live updates unavailable. Script output may not refresh in real time — reload the app to retry.
        </div>
      )}
      {/* Persistent tier: one or more plugin scripts failed to load. */}
      {issues.length > 0 && (
        <LoadIssuesBanner
          issues={issues}
          expanded={issuesExpanded}
          onToggleExpanded={() => setIssuesExpanded(v => !v)}
        />
      )}
      {scripts.length === 0 ? (
        <div className="flex items-center justify-center h-64 text-slate-400">
          No scripts found.
        </div>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {scripts.map((script) => {
            const scriptRuns = Array.from(runs.values()).filter(
              (r) => r.scriptID === script.id
            )
            return (
              <TaskCard
                key={script.id}
                script={script}
                runs={scriptRuns}
                onStartRun={handleStartRun}
                onCancelRun={cancelRun}
              />
            )
          })}
        </div>
      )}
    </div>
  )
}

export default TaskGrid
