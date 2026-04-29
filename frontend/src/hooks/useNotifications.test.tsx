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
      id = result.current.addNotification({ severity: 'error', persistence: 'one-shot', message: 'boom' })
    })
    expect(result.current.notifications).toHaveLength(1)
    expect(result.current.notifications[0].id).toBe(id)
    expect(result.current.notifications[0].severity).toBe('error')
    expect(result.current.notifications[0].persistence).toBe('one-shot')
    expect(result.current.notifications[0].message).toBe('boom')
  })

  it('one-shot notifications auto-dismiss after the default 6s window', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    act(() => {
      result.current.addNotification({ severity: 'info', persistence: 'one-shot', message: 'temp' })
    })
    expect(result.current.notifications).toHaveLength(1)

    act(() => {
      vi.advanceTimersByTime(6000)
    })
    expect(result.current.notifications).toHaveLength(0)
  })

  it('ongoing banners do not auto-dismiss', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    act(() => {
      result.current.addNotification({ severity: 'warn', persistence: 'ongoing', message: 'sticky', key: 'load:foo' })
    })

    act(() => {
      vi.advanceTimersByTime(60_000)
    })
    expect(result.current.notifications).toHaveLength(1)
  })

  it('ongoing banners with the same key dedupe', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    act(() => {
      result.current.addNotification({ severity: 'warn', persistence: 'ongoing', message: 'first', key: 'load:foo' })
      result.current.addNotification({ severity: 'error', persistence: 'ongoing', message: 'second', key: 'load:foo' })
    })

    expect(result.current.notifications).toHaveLength(1)
    expect(result.current.notifications[0].message).toBe('second')
    expect(result.current.notifications[0].severity).toBe('error')
  })

  it('in-flight notifications are dropped from the global stack', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    act(() => {
      result.current.addNotification({ severity: 'error', persistence: 'in-flight', message: 'streamed', runID: 'r1' })
    })
    expect(result.current.notifications).toHaveLength(0)
  })

  it('dismissByKey clears every ongoing entry with the given key', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    act(() => {
      result.current.addNotification({ severity: 'warn', persistence: 'ongoing', message: 'a', key: 'load:foo' })
      result.current.addNotification({ severity: 'warn', persistence: 'ongoing', message: 'b', key: 'load:bar' })
    })
    expect(result.current.notifications).toHaveLength(2)

    act(() => {
      result.current.dismissByKey('load:foo')
    })
    expect(result.current.notifications).toHaveLength(1)
    expect(result.current.notifications[0].key).toBe('load:bar')
  })

  it('replaceOngoingBanners atomically swaps the ongoing set, leaving toasts alone', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    act(() => {
      result.current.addNotification({ severity: 'warn', persistence: 'ongoing', message: 'old1', key: 'load:a' })
      result.current.addNotification({ severity: 'warn', persistence: 'ongoing', message: 'old2', key: 'load:b' })
      result.current.addNotification({ severity: 'error', persistence: 'one-shot', message: 'toast' })
    })
    expect(result.current.notifications).toHaveLength(3)

    act(() => {
      result.current.replaceOngoingBanners([
        { severity: 'warn', persistence: 'ongoing', message: 'new1', key: 'load:c' },
      ])
    })

    // Toast survives; old ongoing entries gone; new ongoing entry present.
    expect(result.current.notifications).toHaveLength(2)
    expect(result.current.notifications.filter(n => n.persistence === 'ongoing')).toHaveLength(1)
    expect(result.current.notifications.find(n => n.persistence === 'ongoing')?.message).toBe('new1')
    expect(result.current.notifications.filter(n => n.persistence === 'one-shot')).toHaveLength(1)
  })

  it('dismissNotification removes a specific notification and clears its timer', () => {
    const { result } = renderHook(() => useNotifications(), { wrapper })

    let firstId = ''
    let secondId = ''
    act(() => {
      firstId = result.current.addNotification({ severity: 'info', persistence: 'one-shot', message: 'first' })
      secondId = result.current.addNotification({ severity: 'info', persistence: 'one-shot', message: 'second' })
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
      vi.advanceTimersByTime(6000)
    })
    expect(result.current.notifications).toHaveLength(0)
  })
})
