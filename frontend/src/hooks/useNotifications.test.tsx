import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { NotificationsProvider, useNotifications } from './useNotifications'

function wrapper({ children }: { children: ReactNode }) {
  return <NotificationsProvider>{children}</NotificationsProvider>
}

describe('useNotifications', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('throws when used outside provider', () => {
    // Suppress React's console.error for the expected throw.
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => renderHook(() => useNotifications())).toThrow(/must be used within NotificationsProvider/)
    spy.mockRestore()
  })

  it('addNotification appends to the queue and returns its id', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    let id = ''
    act(() => {
      id = result.current.addNotification({ level: 'error', message: 'boom' })
    })
    expect(result.current.notifications).toHaveLength(1)
    expect(result.current.notifications[0].id).toBe(id)
    expect(result.current.notifications[0].level).toBe('error')
    expect(result.current.notifications[0].message).toBe('boom')
  })

  it('auto-dismisses after autoDismissMs elapses', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    act(() => {
      result.current.addNotification({ level: 'info', message: 'temp', autoDismissMs: 1000 })
    })
    expect(result.current.notifications).toHaveLength(1)

    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current.notifications).toHaveLength(0)
  })

  it('does not auto-dismiss when autoDismissMs is 0', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    act(() => {
      result.current.addNotification({ level: 'error', message: 'sticky', autoDismissMs: 0 })
    })

    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    expect(result.current.notifications).toHaveLength(1)
  })

  it('dismissNotification removes a specific notification and clears its timer', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    let firstId = ''
    let secondId = ''
    act(() => {
      firstId = result.current.addNotification({ level: 'info', message: 'first', autoDismissMs: 5000 })
      secondId = result.current.addNotification({ level: 'info', message: 'second', autoDismissMs: 5000 })
    })
    expect(result.current.notifications).toHaveLength(2)

    act(() => {
      result.current.dismissNotification(firstId)
    })
    expect(result.current.notifications).toHaveLength(1)
    expect(result.current.notifications[0].id).toBe(secondId)

    // Advance past the dismissed notification's original autoDismissMs.
    // Should NOT crash trying to dismiss an already-removed entry.
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current.notifications).toHaveLength(0)
  })
})
