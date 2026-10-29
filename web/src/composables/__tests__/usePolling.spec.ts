import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { backoffDelay, usePolling } from '../usePolling'

function setHidden(value: boolean) {
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => value })
  document.dispatchEvent(new Event('visibilitychange'))
}

describe('backoffDelay', () => {
  it('doubles per failure and caps', () => {
    expect(backoffDelay(30_000, 0, 300_000)).toBe(30_000)
    expect(backoffDelay(30_000, 1, 300_000)).toBe(60_000)
    expect(backoffDelay(30_000, 3, 300_000)).toBe(240_000)
    expect(backoffDelay(30_000, 4, 300_000)).toBe(300_000)
    expect(backoffDelay(30_000, 40, 300_000)).toBe(300_000)
  })
})

describe('usePolling', () => {
  let scope: ReturnType<typeof effectScope>

  beforeEach(() => {
    vi.useFakeTimers()
    scope = effectScope()
  })

  afterEach(() => {
    scope.stop()
    setHidden(false)
    vi.useRealTimers()
  })

  function poll(task: () => unknown, immediate = false, maxInterval = 8_000) {
    const polling = scope.run(() => usePolling(task, { interval: 1_000, maxInterval, immediate }))
    if (!polling) throw new Error('scope inactive')
    return polling
  }

  it('runs every interval while healthy', async () => {
    const task = vi.fn()
    poll(task)
    expect(task).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1_000)
    expect(task).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(3_000)
    expect(task).toHaveBeenCalledTimes(4)
  })

  it('runs immediately when asked', async () => {
    const task = vi.fn()
    poll(task, true)
    await vi.advanceTimersByTimeAsync(0)
    expect(task).toHaveBeenCalledTimes(1)
  })

  it('backs off on throw or false, caps, and resets on success', async () => {
    let fail = true
    const task = vi.fn(() => {
      if (fail) throw new Error('down')
    })
    const { failures } = poll(task, false, 4_000)
    await vi.advanceTimersByTimeAsync(1_000) // fail 1 -> next in 2s
    expect(failures.value).toBe(1)
    await vi.advanceTimersByTimeAsync(1_999)
    expect(task).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1) // fail 2 -> next in 4s
    await vi.advanceTimersByTimeAsync(4_000) // fail 3 -> capped 4s
    expect(task).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(4_000)
    expect(task).toHaveBeenCalledTimes(4)
    fail = false
    await vi.advanceTimersByTimeAsync(4_000) // success
    expect(failures.value).toBe(0)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(task).toHaveBeenCalledTimes(6)
  })

  it('treats a false result as failure', async () => {
    const { failures } = poll(() => Promise.resolve(false))
    await vi.advanceTimersByTimeAsync(1_000)
    expect(failures.value).toBe(1)
  })

  it('pauses while hidden and refreshes on becoming visible', async () => {
    const task = vi.fn()
    poll(task)
    setHidden(true)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(task).not.toHaveBeenCalled()
    setHidden(false)
    await vi.advanceTimersByTimeAsync(0)
    expect(task).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(task).toHaveBeenCalledTimes(2)
  })

  it('dedupes overlapping refreshes', async () => {
    let resolve: () => void = () => {}
    const task = vi.fn(() => new Promise<void>((r) => (resolve = r)))
    const { refresh, refreshing } = poll(task)
    const a = refresh()
    const b = refresh()
    expect(a).toBe(b)
    expect(refreshing.value).toBe(true)
    resolve()
    await a
    expect(task).toHaveBeenCalledTimes(1)
    expect(refreshing.value).toBe(false)
  })

  it('resets the backoff after a successful manual refresh', async () => {
    let fail = true
    const task = vi.fn(() => {
      if (fail) throw new Error('down')
    })
    const { failures, refresh } = poll(task, false, 8_000)
    await vi.advanceTimersByTimeAsync(3_000) // fail at 1s and 3s -> next in 4s
    expect(failures.value).toBe(2)
    fail = false
    await refresh()
    expect(failures.value).toBe(0)
    await vi.advanceTimersByTimeAsync(999)
    expect(task).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(1)
    expect(task).toHaveBeenCalledTimes(4)
  })

  it('lets a forced refresh supersede the run in flight', async () => {
    const resolvers: Array<(ok: boolean) => void> = []
    const task = vi.fn(() => new Promise<boolean>((r) => resolvers.push(r)))
    const { refresh, failures, refreshing } = poll(task)
    const a = refresh()
    const b = refresh({ force: true })
    expect(a).not.toBe(b)
    expect(task).toHaveBeenCalledTimes(2)
    resolvers[1]!(true)
    await b
    expect(failures.value).toBe(0)
    expect(refreshing.value).toBe(false)
    resolvers[0]!(false) // stale failure is ignored
    await a
    expect(failures.value).toBe(0)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(task).toHaveBeenCalledTimes(3)
  })

  it('stops with its scope and ignores visibility afterwards', async () => {
    const task = vi.fn()
    poll(task)
    scope.stop()
    await vi.advanceTimersByTimeAsync(10_000)
    setHidden(false)
    await vi.advanceTimersByTimeAsync(0)
    expect(task).not.toHaveBeenCalled()
  })
})
