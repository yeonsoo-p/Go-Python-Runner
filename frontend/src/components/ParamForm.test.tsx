import { render, screen, fireEvent } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import ParamForm from './ParamForm'
import type { Param } from '../hooks/useScripts'
import { NotificationsProvider } from '../hooks/useNotifications'
import NotificationStack from './NotificationStack'

function renderWithNotifications(ui: React.ReactElement) {
  return render(
    <NotificationsProvider>
      {ui}
      <NotificationStack />
    </NotificationsProvider>,
  )
}

describe('ParamForm', () => {
  it('renders a Run button with no params', () => {
    const onSubmit = vi.fn()
    renderWithNotifications(<ParamForm params={[]} onSubmit={onSubmit} />)

    const btn = screen.getByText('Run')
    fireEvent.click(btn)
    expect(onSubmit).toHaveBeenCalledWith({})
  })

  it('renders input fields for params', () => {
    const params: Param[] = [
      { name: 'name', required: true, default: '', description: 'Your name' },
    ]
    renderWithNotifications(<ParamForm params={params} onSubmit={vi.fn()} />)

    expect(screen.getByText('name')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Your name')).toBeInTheDocument()
  })

  it('shows required indicator', () => {
    const params: Param[] = [
      { name: 'input', required: true, default: '', description: 'test' },
    ]
    renderWithNotifications(<ParamForm params={params} onSubmit={vi.fn()} />)
    expect(screen.getByText('*')).toBeInTheDocument()
  })

  it('uses default values', () => {
    const params: Param[] = [
      { name: 'name', required: false, default: 'World', description: 'test' },
    ]
    renderWithNotifications(<ParamForm params={params} onSubmit={vi.fn()} />)
    const input = screen.getByDisplayValue('World')
    expect(input).toBeInTheDocument()
  })

  it('disables form when disabled prop is true', () => {
    const params: Param[] = [
      { name: 'name', required: false, default: '', description: 'test' },
    ]
    renderWithNotifications(<ParamForm params={params} onSubmit={vi.fn()} disabled />)
    expect(screen.getByRole('button')).toBeDisabled()
  })

  // Submitting with an empty required field surfaces a notification (via the
  // app's notification stack — locale-independent) and does NOT call onSubmit.
  // We deliberately don't use HTML5 `required` because WebKitGTK / WebView2
  // pull the validation popover string from the OS locale catalog, which can
  // render empty.
  it('surfaces a notification when a required field is empty', () => {
    const onSubmit = vi.fn()
    const params: Param[] = [
      { name: 'input', required: true, default: '', description: 'Text to analyze' },
    ]
    renderWithNotifications(<ParamForm params={params} onSubmit={onSubmit} />)
    fireEvent.click(screen.getByRole('button', { name: 'Run' }))
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('Required field: input')
  })
})
