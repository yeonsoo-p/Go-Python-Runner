import { Component, useState } from 'react'
import type { ErrorInfo, ReactNode } from 'react'
import TaskGrid from './components/TaskGrid'
import LogViewer from './components/LogViewer'
import NotificationStack from './components/NotificationStack'
import { NotificationsProvider } from './hooks/useNotifications'

class ErrorBoundary extends Component<
  { children: ReactNode },
  { error: Error | null; errorKey: number }
> {
  state: { error: Error | null; errorKey: number } = { error: null, errorKey: 0 }

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
              onClick={() => this.setState({ error: null, errorKey: this.state.errorKey + 1 })}
              className="px-3 py-1 text-sm rounded bg-slate-700 hover:bg-slate-600 transition"
            >
              Try Again
            </button>
          </div>
        </div>
      )
    }
    return <div key={this.state.errorKey}>{this.props.children}</div>
  }
}

function App() {
  const [showLogs, setShowLogs] = useState(false)

  // Tier guide (Frontend "shows", Go "manages"):
  //   - Transient (action failed, app fine)        → toast via NotificationStack
  //   - Persistent (feature broken, app usable)    → inline banner in the affected pane
  //   - Catastrophic (app cannot function)         → ErrorBoundary fallback or loadError pane in TaskGrid
  return (
    <ErrorBoundary>
      <NotificationsProvider>
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
        <NotificationStack />
      </NotificationsProvider>
    </ErrorBoundary>
  )
}

export default App
