import { getCurrentInstance, getCurrentScope, onBeforeUnmount, onMounted, onScopeDispose, ref } from 'vue'

/**
 * A poll task. It counts as failed when it throws/rejects or resolves to
 * `false`; anything else is a success.
 */
export type PollTask = () => unknown

export interface PollingOptions {
  /** Delay between polls while healthy. */
  interval: number
  /** Ceiling for the error backoff. Default 5 minutes. */
  maxInterval?: number
  /** Run the task as soon as polling starts instead of after one interval. */
  immediate?: boolean
}

export const DEFAULT_MAX_POLL_INTERVAL_MS = 5 * 60_000

/** interval × 2^failures, capped at max. */
export function backoffDelay(interval: number, failures: number, max: number): number {
  return Math.min(interval * 2 ** failures, Math.max(interval, max))
}

/**
 * Re-runs task on a timer that pauses while the tab is hidden, runs at once
 * when it becomes visible again, and backs off exponentially after failures
 * (reset by the next success). Inside a component it starts on mount and
 * stops before unmount; elsewhere it starts now and stops with the effect
 * scope, if any.
 */
export function usePolling(task: PollTask, options: PollingOptions) {
  const maxInterval = options.maxInterval ?? DEFAULT_MAX_POLL_INTERVAL_MS
  const failures = ref(0)
  const refreshing = ref(false)
  let timer: ReturnType<typeof setTimeout> | undefined
  let active = false
  let inFlight: Promise<void> | undefined
  // Bumped per run; only the newest run updates the backoff and reschedules.
  let generation = 0

  function clear() {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
  }

  function hidden(): boolean {
    return typeof document !== 'undefined' && document.hidden
  }

  function schedule() {
    clear()
    if (!active || hidden()) return
    timer = setTimeout(() => void refresh(), backoffDelay(options.interval, failures.value, maxInterval))
  }

  async function execute(gen: number) {
    refreshing.value = true
    let ok: boolean
    try {
      ok = (await task()) !== false
    } catch {
      ok = false
    }
    if (gen !== generation) return
    failures.value = ok ? 0 : failures.value + 1
    refreshing.value = false
  }

  /**
   * Runs the task now and reschedules. Joins a run already in flight unless
   * `force` is set; a forced run supersedes it (use after a mutation, whose
   * result an older run may predate), and the older run's outcome is ignored.
   */
  function refresh(opts: { force?: boolean } = {}): Promise<void> {
    if (inFlight && !opts.force) return inFlight
    clear()
    const gen = ++generation
    const run = execute(gen).finally(() => {
      if (gen !== generation) return
      inFlight = undefined
      schedule()
    })
    inFlight = run
    return run
  }

  function onVisibility() {
    if (!active) return
    if (hidden()) clear()
    else void refresh()
  }

  function start() {
    if (active) return
    active = true
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibility)
    if (options.immediate) void refresh()
    else schedule()
  }

  function stop() {
    active = false
    clear()
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisibility)
  }

  if (getCurrentInstance()) {
    onMounted(start)
    onBeforeUnmount(stop)
  } else {
    start()
    if (getCurrentScope()) onScopeDispose(stop)
  }

  return { refresh, start, stop, failures, refreshing }
}
