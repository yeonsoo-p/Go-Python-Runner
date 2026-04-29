import { render, screen } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import TaskCard from './TaskCard'
import type { Script } from '../hooks/useScripts'

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

describe('TaskCard', () => {
  it('renders script name and description', () => {
    render(
      <TaskCard script={mockScript} runs={[]} onStartRun={vi.fn()} onCancelRun={vi.fn()} />
    )
    expect(screen.getByText('Hello World')).toBeInTheDocument()
    expect(screen.getByText('A simple greeting script')).toBeInTheDocument()
  })

  it('shows plugin badge for plugin scripts', () => {
    render(
      <TaskCard script={pluginScript} runs={[]} onStartRun={vi.fn()} onCancelRun={vi.fn()} />
    )
    expect(screen.getByText('plugin')).toBeInTheDocument()
  })

  it('does not show plugin badge for builtin scripts', () => {
    render(
      <TaskCard script={mockScript} runs={[]} onStartRun={vi.fn()} onCancelRun={vi.fn()} />
    )
    expect(screen.queryByText('plugin')).not.toBeInTheDocument()
  })
})
