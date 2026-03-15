import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'

// Mock the bindings and runtime before importing the component
vi.mock('../../bindings/go-python-runner/internal/services', () => ({
  LogService: {
    GetLogs: vi.fn().mockResolvedValue([]),
  },
}))

vi.mock('../../bindings/go-python-runner/internal/logging/models', () => ({
  // Type-only import, no runtime value needed
}))

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: vi.fn().mockReturnValue(() => {}),
  },
}))

import LogViewer from './LogViewer'

describe('LogViewer', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders with empty state', () => {
    render(<LogViewer />)
    expect(screen.getByText('Logs')).toBeInTheDocument()
    expect(screen.getByText('No logs')).toBeInTheDocument()
  })

  it('renders source filter dropdown', () => {
    render(<LogViewer />)
    expect(screen.getByText('All Sources')).toBeInTheDocument()
    expect(screen.getByText('Frontend')).toBeInTheDocument()
    expect(screen.getByText('Backend')).toBeInTheDocument()
    expect(screen.getByText('Python')).toBeInTheDocument()
  })

  it('renders level filter dropdown', () => {
    render(<LogViewer />)
    expect(screen.getByText('All Levels')).toBeInTheDocument()
    expect(screen.getByText('Error')).toBeInTheDocument()
    expect(screen.getByText('Warn')).toBeInTheDocument()
    expect(screen.getByText('Info')).toBeInTheDocument()
    expect(screen.getByText('Debug')).toBeInTheDocument()
  })

  it('allows changing source filter', () => {
    render(<LogViewer />)
    const sourceSelect = screen.getAllByRole('combobox')[0]
    fireEvent.change(sourceSelect, { target: { value: 'python' } })
    expect(sourceSelect).toHaveValue('python')
  })

  it('allows changing level filter', () => {
    render(<LogViewer />)
    const levelSelect = screen.getAllByRole('combobox')[1]
    fireEvent.change(levelSelect, { target: { value: 'ERROR' } })
    expect(levelSelect).toHaveValue('ERROR')
  })
})
