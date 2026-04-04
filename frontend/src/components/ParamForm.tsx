import { useState } from 'react'
import type { Param } from '../hooks/useScripts'

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
  const [values, setValues] = useState<Record<string, string>>(() => {
    const initial: Record<string, string> = {}
    for (const p of params) {
      initial[p.name] = p.default || ''
    }
    return initial
  })
  const [workerCount, setWorkerCount] = useState(parallel?.default_workers ?? 1)

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
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
            required={param.required}
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
