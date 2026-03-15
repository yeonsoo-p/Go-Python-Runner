import { useScripts } from '../hooks/useScripts'
import TaskCard from './TaskCard'

function TaskGrid() {
  const { scripts, runs, loading, loadError, startRun, cancelRun } = useScripts()

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
            onStartRun={startRun}
            onCancelRun={cancelRun}
          />
        )
      })}
    </div>
  )
}

export default TaskGrid
