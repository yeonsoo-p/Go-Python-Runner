import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import AggregateRunPanel from './AggregateRunPanel'
import type { RunGroupState, RunState } from '../hooks/useScripts'

function makeRun(overrides: Partial<RunState>): RunState {
  return {
    runID: 'r',
    scriptID: 'parallel_worker',
    status: 'running',
    output: [],
    progress: null,
    error: null,
    data: null,
    ...overrides,
  }
}

function makeGroup(runIDs: string[]): RunGroupState {
  return {
    groupID: 'g-aaaaaaaa-bbbb',
    scriptID: 'parallel_worker',
    runIDs,
    startedAt: 0,
  }
}

describe('AggregateRunPanel', () => {
  it('renders sum-of-currents/sum-of-totals across workers', () => {
    const runs: RunState[] = [
      makeRun({ runID: 'r1', progress: { current: 50, total: 100, label: 'Worker-1' } }),
      makeRun({ runID: 'r2', progress: { current: 80, total: 200, label: 'Worker-2' } }),
      makeRun({ runID: 'r3', progress: { current: 0, total: 100, label: 'Worker-3' } }),
    ]
    render(
      <AggregateRunPanel
        group={makeGroup(['r1', 'r2', 'r3'])}
        runs={runs}
        onCancelGroup={vi.fn()}
        onCancelRun={vi.fn()}
      />
    )
    expect(screen.getByText('130/400')).toBeInTheDocument()
  })

  it('counts running / done / failed correctly', () => {
    const runs: RunState[] = [
      makeRun({ runID: 'r1', status: 'running' }),
      makeRun({ runID: 'r2', status: 'running' }),
      makeRun({ runID: 'r3', status: 'completed' }),
      makeRun({ runID: 'r4', status: 'failed' }),
    ]
    render(
      <AggregateRunPanel
        group={makeGroup(['r1', 'r2', 'r3', 'r4'])}
        runs={runs}
        onCancelGroup={vi.fn()}
        onCancelRun={vi.fn()}
      />
    )
    expect(screen.getByText('2 running')).toBeInTheDocument()
    expect(screen.getByText('1 done')).toBeInTheDocument()
    expect(screen.getByText('1 failed')).toBeInTheDocument()
  })

  it('Cancel all calls onCancelGroup with the group id', () => {
    const onCancelGroup = vi.fn()
    const runs = [makeRun({ runID: 'r1', status: 'running' })]
    render(
      <AggregateRunPanel
        group={makeGroup(['r1'])}
        runs={runs}
        onCancelGroup={onCancelGroup}
        onCancelRun={vi.fn()}
      />
    )
    fireEvent.click(screen.getByText('Cancel all'))
    expect(onCancelGroup).toHaveBeenCalledWith('g-aaaaaaaa-bbbb')
  })

  it('disables Cancel all when no worker is running', () => {
    const runs: RunState[] = [
      makeRun({ runID: 'r1', status: 'completed' }),
      makeRun({ runID: 'r2', status: 'failed' }),
    ]
    render(
      <AggregateRunPanel
        group={makeGroup(['r1', 'r2'])}
        runs={runs}
        onCancelGroup={vi.fn()}
        onCancelRun={vi.fn()}
      />
    )
    const btn = screen.getByText('Cancel all') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })

  it('disclosure toggle reveals per-worker detail', () => {
    const runs: RunState[] = [
      makeRun({ runID: 'r1aaaa11', status: 'running' }),
      makeRun({ runID: 'r2bbbb22', status: 'running' }),
    ]
    render(
      <AggregateRunPanel
        group={makeGroup(['r1aaaa11', 'r2bbbb22'])}
        runs={runs}
        onCancelGroup={vi.fn()}
        onCancelRun={vi.fn()}
      />
    )
    // Hidden by default.
    expect(screen.queryByText('r1aaaa11')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText(/Show per-worker detail/))
    expect(screen.getByText('r1aaaa11')).toBeInTheDocument()
    expect(screen.getByText('r2bbbb22')).toBeInTheDocument()
  })

  it('per-worker Cancel button calls onCancelRun for that worker only', () => {
    const onCancelRun = vi.fn()
    const runs: RunState[] = [
      makeRun({ runID: 'r1aaaa11', status: 'running' }),
      makeRun({ runID: 'r2bbbb22', status: 'running' }),
    ]
    render(
      <AggregateRunPanel
        group={makeGroup(['r1aaaa11', 'r2bbbb22'])}
        runs={runs}
        onCancelGroup={vi.fn()}
        onCancelRun={onCancelRun}
      />
    )
    fireEvent.click(screen.getByText(/Show per-worker detail/))
    const cancelButtons = screen.getAllByText('Cancel')
    fireEvent.click(cancelButtons[0])
    expect(onCancelRun).toHaveBeenCalledWith('r1aaaa11')
  })
})
