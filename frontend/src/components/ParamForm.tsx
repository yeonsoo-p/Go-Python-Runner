import { useState } from 'react'
import type { Param } from '../hooks/useScripts'

interface ParamFormProps {
  params: Param[]
  onSubmit: (values: Record<string, string>) => void
  disabled?: boolean
}

function ParamForm({ params, onSubmit, disabled }: ParamFormProps) {
  const [values, setValues] = useState<Record<string, string>>(() => {
    const initial: Record<string, string> = {}
    for (const p of params) {
      initial[p.name] = p.default || ''
    }
    return initial
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    onSubmit(values)
  }

  if (params.length === 0) {
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
        Run
      </button>
    </form>
  )
}

export default ParamForm
