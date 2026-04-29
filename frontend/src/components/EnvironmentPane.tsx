import { useMemo, useRef, useState } from 'react'
import { useEnv } from '../hooks/useEnv'
import { useNotifications } from '../hooks/useNotifications'

function EnvironmentPane() {
  const {
    info,
    packages,
    loading,
    busy,
    logLines,
    indexURL,
    available,
    setIndexURL,
    installPackage,
    installRequirements,
    uninstallPackage,
  } = useEnv()
  const { addNotification } = useNotifications()
  const [filter, setFilter] = useState('')
  const [installSpec, setInstallSpec] = useState('')
  const [dragHover, setDragHover] = useState(false)
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return packages
    return packages.filter(p => p.name.toLowerCase().includes(q))
  }, [filter, packages])

  if (loading) {
    return <div className="flex items-center justify-center h-64 text-slate-400">Loading environment…</div>
  }
  if (!available || !info) {
    return (
      <div className="rounded border border-yellow-700 bg-yellow-900/40 px-4 py-3 text-sm text-yellow-100">
        Environment management unavailable. The app could not resolve a Python venv.
      </div>
    )
  }

  const handleInstallSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    const spec = installSpec.trim()
    if (!spec) return
    setInstallSpec('')
    await installPackage(spec)
  }

  const onDropZoneClick = () => {
    if (busy) return
    fileInputRef.current?.click()
  }

  const onFileChosen = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = '' // reset so the same file can be picked again
    if (!file) return
    // Web `File` doesn't expose absolute paths; Wails dev/build modes vary.
    // Best-available: use webkitRelativePath if set, else file.name. The
    // backend validates existence and is-file; if that fails we toast.
    type FileWithPath = File & { path?: string }
    const fp = (file as FileWithPath).path
    const path = fp || (file as FileWithPath).webkitRelativePath || file.name
    if (!file.name.toLowerCase().endsWith('.txt')) {
      addNotification({ level: 'error', message: 'Only .txt files are accepted' })
      return
    }
    await installRequirements(path)
  }

  const onDrop = async (e: React.DragEvent) => {
    e.preventDefault()
    setDragHover(false)
    if (busy) return
    const file = e.dataTransfer.files?.[0]
    if (!file) return
    if (!file.name.toLowerCase().endsWith('.txt')) {
      addNotification({ level: 'error', message: 'Only .txt files are accepted' })
      return
    }
    type FileWithPath = File & { path?: string }
    const fp = (file as FileWithPath).path
    const path = fp || file.name
    await installRequirements(path)
  }

  return (
    <div className="space-y-4">
      {/* Env summary */}
      <div className="rounded border border-slate-700 bg-slate-900/40 px-4 py-3 text-sm">
        <div className="grid grid-cols-1 gap-1 md:grid-cols-2">
          <div><span className="text-slate-400">Python:</span> <code className="break-all">{info.pythonPath}</code></div>
          <div><span className="text-slate-400">Version:</span> {info.pythonVersion || 'unknown'}</div>
          <div><span className="text-slate-400">Venv:</span> <code className="break-all">{info.venvPath}</code></div>
          <div><span className="text-slate-400">Tool:</span> {info.toolName}</div>
        </div>
        {!info.editable && (
          <div className="mt-3 rounded border border-yellow-700 bg-yellow-900/40 px-3 py-2 text-yellow-100">
            This environment is read-only. Install/uninstall is disabled.
          </div>
        )}
      </div>

      {/* Index URL setting */}
      <div className="rounded border border-slate-700 bg-slate-900/40 px-4 py-3 text-sm">
        <label className="block">
          <span className="text-slate-400">Index URL (optional)</span>
          <input
            type="text"
            value={indexURL}
            onChange={(e) => setIndexURL(e.target.value)}
            placeholder="https://pypi.org/simple/"
            className="mt-1 w-full rounded border border-slate-600 bg-slate-950 px-2 py-1 text-slate-100"
          />
        </label>
      </div>

      {/* Install single package */}
      {info.editable && (
        <form onSubmit={handleInstallSubmit} className="rounded border border-slate-700 bg-slate-900/40 px-4 py-3 text-sm">
          <label className="block">
            <span className="text-slate-400">Install package</span>
            <div className="mt-1 flex gap-2">
              <input
                type="text"
                value={installSpec}
                onChange={(e) => setInstallSpec(e.target.value)}
                placeholder="pandas or numpy>=2.0 or git+https://…"
                disabled={busy}
                className="flex-1 rounded border border-slate-600 bg-slate-950 px-2 py-1 text-slate-100 disabled:opacity-60"
              />
              <button
                type="submit"
                disabled={busy || !installSpec.trim()}
                className="rounded bg-blue-700 px-3 py-1 hover:bg-blue-600 disabled:opacity-60"
              >
                Install
              </button>
            </div>
          </label>
        </form>
      )}

      {/* requirements.txt drop zone + file picker */}
      {info.editable && (
        <div
          onClick={onDropZoneClick}
          onDragOver={(e) => { e.preventDefault(); setDragHover(true) }}
          onDragLeave={() => setDragHover(false)}
          onDrop={onDrop}
          className={`cursor-pointer rounded border-2 border-dashed px-4 py-6 text-center text-sm ${dragHover ? 'border-blue-400 bg-blue-900/20' : 'border-slate-600 bg-slate-900/40'} ${busy ? 'pointer-events-none opacity-50' : ''}`}
        >
          Install from requirements.txt — drop a file here or click to browse.
          <input
            ref={fileInputRef}
            type="file"
            accept=".txt"
            onChange={onFileChosen}
            className="hidden"
          />
        </div>
      )}

      {/* Streamed pip output */}
      {logLines.length > 0 && (
        <div className="rounded border border-slate-700 bg-black/60 px-4 py-2 font-mono text-xs">
          {logLines.map((l, i) => (
            <div key={i} className={l.stream === 'stderr' ? 'text-red-300' : 'text-slate-200'}>{l.line}</div>
          ))}
        </div>
      )}

      {/* Package table */}
      <div>
        <div className="mb-2 flex items-center gap-2">
          <input
            type="text"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter packages…"
            className="flex-1 rounded border border-slate-600 bg-slate-950 px-2 py-1 text-sm text-slate-100"
          />
          <span className="text-xs text-slate-400">{filtered.length} / {packages.length}</span>
        </div>
        <div className="overflow-hidden rounded border border-slate-700">
          <table className="w-full text-sm">
            <thead className="bg-slate-800/60">
              <tr>
                <th className="px-3 py-2 text-left">Name</th>
                <th className="px-3 py-2 text-left">Version</th>
                {info.editable && <th className="px-3 py-2 text-right">Actions</th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-700">
              {filtered.length === 0 ? (
                <tr><td colSpan={info.editable ? 3 : 2} className="px-3 py-4 text-center text-slate-400">No packages found.</td></tr>
              ) : (
                filtered.map((pkg) => (
                  <tr key={pkg.name}>
                    <td className="px-3 py-1.5"><code>{pkg.name}</code></td>
                    <td className="px-3 py-1.5 text-slate-300">{pkg.version}</td>
                    {info.editable && (
                      <td className="px-3 py-1.5 text-right">
                        <button
                          type="button"
                          disabled={busy}
                          onClick={() => uninstallPackage(pkg.name)}
                          className="rounded bg-slate-700 px-2 py-0.5 text-xs hover:bg-red-700 disabled:opacity-60"
                        >
                          Uninstall
                        </button>
                      </td>
                    )}
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}

export default EnvironmentPane
