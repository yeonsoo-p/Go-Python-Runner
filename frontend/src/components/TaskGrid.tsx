import { useCallback } from 'react'
import { useScripts } from '../hooks/useScripts'
import TaskCard from './TaskCard'

function TaskGrid() {
  const { scripts, runs, loading, loadError, startRun, startParallelRuns, cancelRun } = useScripts()

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

  if (loadError) {
    return (
      <div className="flex items-center justify-center h-64 text-red-400">
        {loadError}
      </div>
    )
  }

  if (scripts.length === 0) {
    return (
      <div className="flex items-center justify-center h-64 text-slate-400">
        No scripts found.
      </div>
    )
  }

  return (
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
  )
}

export default TaskGrid
