import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import TaskCard from './TaskCard'
import type { Script, RunState, RunGroupState } from '../hooks/useScripts'
import { NotificationsProvider } from '../hooks/useNotifications'

function renderWithNotifications(ui: React.ReactElement) {
  return render(<NotificationsProvider>{ui}</NotificationsProvider>)
}

const mockScript: Script = {
  id: 'hello_world',
  name: 'Hello World',
  description: 'A simple greeting script',
  params: [
    { name: 'name', required: false, default: 'World', description: 'Who to greet' }
  ],
  parallel: null,
  source: 'builtin',
}

const pluginScript: Script = {
  ...mockScript,
  id: 'custom',
  name: 'Custom Plugin',
  source: 'plugin',
}

const parallelScript: Script = {
  id: 'parallel_worker',
  name: 'Parallel Worker',
  description: 'Runs in parallel',
  params: [],
  parallel: {
    default_workers: 3,
    max_workers: 8,
    vary_param: 'worker_name',
    chain_param: '',
    names: [],
  },
  source: 'builtin',
}

function makeRun(runID: string, scriptID: string, status: RunState['status'] = 'running'): RunState {
  return { runID, scriptID, status, output: [], progress: null, error: null }
}

describe('TaskCard', () => {
  it('renders script name and description', () => {
    renderWithNotifications(
      <TaskCard script={mockScript} runs={[]} onStartRun={vi.fn()} onCancelRun={vi.fn()} />
    )
    expect(screen.getByText('Hello World')).toBeInTheDocument()
    expect(screen.getByText('A simple greeting script')).toBeInTheDocument()
  })

  it('shows plugin badge for plugin scripts', () => {
    renderWithNotifications(
      <TaskCard script={pluginScript} runs={[]} onStartRun={vi.fn()} onCancelRun={vi.fn()} />
    )
    expect(screen.getByText('plugin')).toBeInTheDocument()
  })

  it('does not show plugin badge for builtin scripts', () => {
    renderWithNotifications(
      <TaskCard script={mockScript} runs={[]} onStartRun={vi.fn()} onCancelRun={vi.fn()} />
    )
    expect(screen.queryByText('plugin')).not.toBeInTheDocument()
  })

  it('renders aggregate panel when runs share a group', () => {
    const group: RunGroupState = {
      groupID: 'g123',
      scriptID: 'parallel_worker',
      runIDs: ['r1', 'r2', 'r3'],
      startedAt: 0,
    }
    const runs = ['r1', 'r2', 'r3'].map((id) => makeRun(id, 'parallel_worker'))

    renderWithNotifications(
      <TaskCard
        script={parallelScript}
        runs={runs}
        groups={[group]}
        onStartRun={vi.fn()}
        onCancelRun={vi.fn()}
        onCancelGroup={vi.fn()}
      />
    )

    // Header chip reads "{N} workers" when a group is live, not "{N} running".
    expect(screen.getByText('3 workers')).toBeInTheDocument()
    expect(screen.queryByText('3 running')).not.toBeInTheDocument()

    // Expand the card to render the panel content.
    fireEvent.click(screen.getByText('Parallel Worker'))
    expect(screen.getByText('Cancel all')).toBeInTheDocument()
    // Per-worker detail is collapsed by default — individual run IDs are not
    // visible until the disclosure is toggled.
    expect(screen.queryByText('r1')).not.toBeInTheDocument()
  })
})
