import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import ParamForm from './ParamForm'
import type { Param } from '../hooks/useScripts'

describe('ParamForm', () => {
  it('renders a Run button with no params', () => {
    const onSubmit = vi.fn()
    render(<ParamForm params={[]} onSubmit={onSubmit} />)

    const btn = screen.getByText('Run')
    fireEvent.click(btn)
    expect(onSubmit).toHaveBeenCalledWith({})
  })

  it('renders input fields for params', () => {
    const params: Param[] = [
      { name: 'name', required: true, default: '', description: 'Your name' },
    ]
    render(<ParamForm params={params} onSubmit={vi.fn()} />)

    expect(screen.getByText('name')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Your name')).toBeInTheDocument()
  })

  it('shows required indicator', () => {
    const params: Param[] = [
      { name: 'input', required: true, default: '', description: 'test' },
    ]
    render(<ParamForm params={params} onSubmit={vi.fn()} />)
    expect(screen.getByText('*')).toBeInTheDocument()
  })

  it('uses default values', () => {
    const params: Param[] = [
      { name: 'name', required: false, default: 'World', description: 'test' },
    ]
    render(<ParamForm params={params} onSubmit={vi.fn()} />)
    const input = screen.getByDisplayValue('World')
    expect(input).toBeInTheDocument()
  })

  it('disables form when disabled prop is true', () => {
    const params: Param[] = [
      { name: 'name', required: false, default: '', description: 'test' },
    ]
    render(<ParamForm params={params} onSubmit={vi.fn()} disabled />)
    expect(screen.getByRole('button')).toBeDisabled()
  })
})
