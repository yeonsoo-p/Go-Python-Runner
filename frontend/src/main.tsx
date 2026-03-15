import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './index.css'

// Forward uncaught errors to Go's LogService
function reportToBackend(source: string, message: string, context: Record<string, string> = {}) {
  import('../bindings/go-python-runner/internal/services').then(({ LogService }) => {
    LogService?.LogError?.(source, message, context)
  }).catch(() => { /* bindings not available */ })
}

window.onerror = (_msg, source, lineno, colno, error) => {
  const message = error?.message ?? String(_msg)
  reportToBackend('frontend', message, {
    source: source ?? '',
    line: String(lineno ?? ''),
    column: String(colno ?? ''),
    stack: error?.stack ?? '',
  })
}

window.addEventListener('unhandledrejection', (event) => {
  const reason = event.reason
  const message = reason instanceof Error ? reason.message : String(reason)
  reportToBackend('frontend', `Unhandled promise rejection: ${message}`, {
    stack: reason instanceof Error ? (reason.stack ?? '') : '',
  })
})

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
