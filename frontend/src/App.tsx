import { Component, useState } from 'react'
import type { ErrorInfo, ReactNode } from 'react'
import TaskGrid from './components/TaskGrid'
import LogViewer from './components/LogViewer'

class ErrorBoundary extends Component<
  { children: ReactNode },
  { error: Error | null }
> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    import('../bindings/go-python-runner/internal/services').then(({ LogService }) => {
      LogService?.LogError?.('frontend', error.message, {
        stack: error.stack ?? '',
        componentStack: info.componentStack ?? '',
      })
    }).catch(() => { /* bindings not available */ })
  }

  render() {
    if (this.state.error) {
      return (
        <div className="min-h-screen bg-slate-900 text-slate-200 flex items-center justify-center">
          <div className="max-w-lg p-6 bg-red-900/30 border border-red-700 rounded-lg">
            <h2 className="text-lg font-bold text-red-400 mb-2">Something went wrong</h2>
            <p className="text-sm text-slate-300 mb-4">{this.state.error.message}</p>
            <button
              onClick={() => this.setState({ error: null })}
              className="px-3 py-1 text-sm rounded bg-slate-700 hover:bg-slate-600 transition"
            >
              Try Again
            </button>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}

function App() {
  const [showLogs, setShowLogs] = useState(false)

  return (
    <ErrorBoundary>
      <div className="min-h-screen bg-slate-900 text-slate-200">
        <header className="border-b border-slate-700 px-6 py-4 flex items-center justify-between">
          <h1 className="text-xl font-bold">Go Python Runner</h1>
          <button
            onClick={() => setShowLogs(!showLogs)}
            className="px-3 py-1 text-sm rounded bg-slate-700 hover:bg-slate-600 transition"
          >
            {showLogs ? 'Hide Logs' : 'Show Logs'}
          </button>
        </header>

        <main className="p-6">
          <TaskGrid />
        </main>

        {showLogs && (
          <div className="border-t border-slate-700">
            <LogViewer />
          </div>
        )}
      </div>
    </ErrorBoundary>
  )
}

export default App
