import { useState } from 'react'
import type { Param } from '../hooks/useScripts'
import { useNotifications } from '../hooks/useNotifications'

interface ParallelConfig {
  default_workers: number
  max_workers: number
}

interface ParamFormProps {
  params: Param[]
  parallel?: ParallelConfig | null
  onSubmit: (values: Record<string, string>, workerCount?: number) => void
  disabled?: boolean
}

function ParamForm({ params, parallel, onSubmit, disabled }: ParamFormProps) {
  const { addNotification } = useNotifications()
  const [values, setValues] = useState<Record<string, string>>(() => {
    const initial: Record<string, string> = {}
    for (const p of params) {
      initial[p.name] = p.default || ''
    }
    return initial
  })
  const [workerCount, setWorkerCount] = useState(parallel?.default_workers ?? 1)

  // Don't use HTML5 `required` — WebKitGTK / WebView2 source the validation
  // popover string from the OS locale catalog, which can render empty under
  // some locales. Validate explicitly and surface via the app's notifications.
  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const missing = params.filter(p => p.required && !values[p.name].trim()).map(p => p.name)
    if (missing.length > 0) {
      addNotification({
        severity: 'error',
        persistence: 'one-shot',
        source: 'frontend',
        message: `Required field${missing.length > 1 ? 's' : ''}: ${missing.join(', ')}`,
      })
      return
    }
    if (parallel) {
      onSubmit(values, workerCount)
    } else {
      onSubmit(values)
    }
  }

  if (params.length === 0 && !parallel) {
    return (
      <button
        onClick={() => onSubmit({})}
        disabled={disabled}
        className="px-4 py-2 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-50 disabled:cursor-not-allowed transition font-medium"
      >
        Run
      </button>
    )
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      {parallel && (
        <div>
          <label className="block text-sm font-medium text-slate-300 mb-1">
            Workers
          </label>
          <input
            type="number"
            min={1}
            max={parallel.max_workers}
            value={workerCount}
            onChange={(e) => setWorkerCount(Math.max(1, Math.min(parallel.max_workers, Number(e.target.value) || 1)))}
            disabled={disabled}
            className="w-24 px-3 py-2 rounded bg-slate-700 border border-slate-600 text-slate-200 focus:outline-none focus:border-blue-500 disabled:opacity-50"
          />
        </div>
      )}
      {params.map((param) => (
        <div key={param.name}>
          <label className="block text-sm font-medium text-slate-300 mb-1">
            {param.name}
            {param.required && <span className="text-red-400 ml-1">*</span>}
          </label>
          <input
            type="text"
            value={values[param.name] || ''}
            onChange={(e) =>
              setValues((prev) => ({ ...prev, [param.name]: e.target.value }))
            }
            placeholder={param.description}
            disabled={disabled}
            className="w-full px-3 py-2 rounded bg-slate-700 border border-slate-600 text-slate-200 placeholder-slate-500 focus:outline-none focus:border-blue-500 disabled:opacity-50"
          />
        </div>
      ))}
      <button
        type="submit"
        disabled={disabled}
        className="px-4 py-2 rounded bg-blue-600 hover:bg-blue-500 disabled:opacity-50 disabled:cursor-not-allowed transition font-medium"
      >
        {parallel ? `Launch ${workerCount} Worker${workerCount !== 1 ? 's' : ''}` : 'Run'}
      </button>
    </form>
  )
}

export default ParamForm
