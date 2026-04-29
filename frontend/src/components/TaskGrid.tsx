import { useCallback } from 'react'
import { useScripts } from '../hooks/useScripts'
import TaskCard from './TaskCard'

function TaskGrid() {
  const { scripts, runs, groups, loading, loadError, startRun, startParallelRuns, cancelRun, cancelGroup } = useScripts()

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

  // Plugin LoadIssues and the live-updates-broken condition both render
  // through the central NotificationStack as ongoing banners now — no
  // hand-rolled banner state lives here.
  return (
    <div className="space-y-4">
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
            const scriptGroups = Array.from(groups.values()).filter(
              (g) => g.scriptID === script.id
            )
            return (
              <TaskCard
                key={script.id}
                script={script}
                runs={scriptRuns}
                groups={scriptGroups}
                onStartRun={handleStartRun}
                onCancelRun={cancelRun}
                onCancelGroup={cancelGroup}
              />
            )
          })}
        </div>
      )}
    </div>
  )
}

export default TaskGrid
