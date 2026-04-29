import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import type { ReactNode } from 'react'

// Mock the bindings BEFORE importing useScripts. RunnerService.StartRun
// always rejects so we can assert the catch path's surfacing policy.
vi.mock('../../bindings/go-python-runner/internal/services', () => ({
  ScriptService: {
    ListScripts: vi.fn().mockResolvedValue([]),
  },
  RunnerService: {
    StartRun: vi.fn().mockRejectedValue(new Error('boom')),
    StartParallelRuns: vi.fn().mockRejectedValue(new Error('boom')),
    CancelRun: vi.fn().mockRejectedValue(new Error('boom')),
  },
  // LogService is intentionally not exported here — if useScripts ever
  // tries to call LogError on a Wails-bound catch, the test will fail
  // with "LogService is undefined", which is exactly what we want.
}))

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: vi.fn().mockReturnValue(() => {}),
  },
}))

import { useScripts } from './useScripts'
import { NotificationsProvider, useNotifications } from './useNotifications'

function wrapper({ children }: { children: ReactNode }) {
  return <NotificationsProvider>{children}</NotificationsProvider>
}

describe('useScripts surfacing policy', () => {
  it('startRun rejection does not push a notification (Go reservoir.Report owns the toast)', async () => {
    const { result } = renderHook(
      () => ({ scripts: useScripts(), notify: useNotifications() }),
      { wrapper },
    )

    // Wait for initial loading to settle.
    await waitFor(() => expect(result.current.scripts.loading).toBe(false))

    // Snapshot the notification queue length before the rejection.
    const before = result.current.notify.notifications.length

    await act(async () => {
      const id = await result.current.scripts.startRun('any', {})
      expect(id).toBeNull()
    })

    // Catch is control flow only — the queue must not have grown. Go's
    // reservoir.Report → notify:toast handles surfacing.
    expect(result.current.notify.notifications).toHaveLength(before)
  })

  it('cancelRun rejection does not push a notification', async () => {
    const { result } = renderHook(
      () => ({ scripts: useScripts(), notify: useNotifications() }),
      { wrapper },
    )

    await waitFor(() => expect(result.current.scripts.loading).toBe(false))
    const before = result.current.notify.notifications.length

    await act(async () => {
      await result.current.scripts.cancelRun('non-existent-run')
    })

    expect(result.current.notify.notifications).toHaveLength(before)
  })

  it('startParallelRuns rejection does not push a notification', async () => {
    const { result } = renderHook(
      () => ({ scripts: useScripts(), notify: useNotifications() }),
      { wrapper },
    )

    await waitFor(() => expect(result.current.scripts.loading).toBe(false))
    const before = result.current.notify.notifications.length

    await act(async () => {
      const ids = await result.current.scripts.startParallelRuns('any', {}, 2)
      expect(ids).toBeNull()
    })

    expect(result.current.notify.notifications).toHaveLength(before)
  })
})
