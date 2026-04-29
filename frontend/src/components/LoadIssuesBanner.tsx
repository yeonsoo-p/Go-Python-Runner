import type { LoadIssue } from '../hooks/useScripts'
import { useNotifications } from '../hooks/useNotifications'

// joinPath appends a filename to a directory using whichever separator
// already appears in the directory path (Windows backslash or POSIX slash).
// The dir comes from Go (filepath.Join'd), so it's already correct for the
// host OS — we just need to match.
function joinPath(dir: string, file: string): string {
  const sep = dir.includes('\\') && !dir.includes('/') ? '\\' : '/'
  return dir.endsWith(sep) ? `${dir}${file}` : `${dir}${sep}${file}`
}

interface Props {
  issues: LoadIssue[]
  expanded: boolean
  onToggleExpanded: () => void
}

// Persistent banner rendered above the task grid when one or more plugin
// scripts failed to load. Each row offers two actions: open script.json in
// the OS default editor, or open the containing folder. Errors from those
// actions surface as toasts (transient tier) per the unhappy-path rule.
function LoadIssuesBanner({ issues, expanded, onToggleExpanded }: Props) {
  const { addNotification } = useNotifications()

  const handleOpen = async (absPath: string) => {
    try {
      const bindings = await import('../../bindings/go-python-runner/internal/services')
      await bindings.ScriptService.OpenPath(absPath)
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      addNotification({ level: 'error', message: `Failed to open ${absPath}: ${msg}` })
      // Go already logged via slog; no need for LogService.LogError here.
    }
  }

  const count = issues.length
  const label = count === 1 ? '1 plugin failed to load' : `${count} plugins failed to load`

  return (
    <div className="rounded border border-red-700 bg-red-900/40 text-sm text-red-100">
      <button
        type="button"
        className="flex w-full items-center justify-between px-4 py-2 text-left hover:bg-red-900/60"
        onClick={onToggleExpanded}
        aria-expanded={expanded}
      >
        <span>{label} — click for details</span>
        <span className="text-xs opacity-70">{expanded ? 'hide' : 'show'}</span>
      </button>
      {expanded && (
        <ul className="divide-y divide-red-800/60 border-t border-red-800/60">
          {issues.map((issue, i) => (
            <li key={`${issue.dir}-${i}`} className="flex flex-col gap-1 px-4 py-2">
              <div className="flex items-center justify-between gap-2">
                <code className="break-all text-xs">{issue.dir}</code>
                <div className="flex shrink-0 gap-2 text-xs">
                  <button
                    type="button"
                    className="rounded bg-red-800 px-2 py-1 hover:bg-red-700"
                    onClick={() => handleOpen(joinPath(issue.dir, 'script.json'))}
                  >
                    Edit script.json
                  </button>
                  <button
                    type="button"
                    className="rounded bg-red-800 px-2 py-1 hover:bg-red-700"
                    onClick={() => handleOpen(issue.dir)}
                  >
                    Open folder
                  </button>
                </div>
              </div>
              <div className="text-xs text-red-200">{issue.error}</div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export default LoadIssuesBanner
