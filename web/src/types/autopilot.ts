/** GET /autopilot and the host `autopilot` block (Go: pkg/autopilot/controller). */

import { nonNegative, oneOf } from './common'
import { OS_OPTIONS, type HostOS } from './host'

export const AUTOPILOT_HOST_STATES = [
  'idle',
  'rolling',
  'gating',
  'retrying',
  'rolled-back',
  'needs-hands'
] as const
export type AutopilotHostState = (typeof AUTOPILOT_HOST_STATES)[number]

export interface HostAutopilot {
  state: AutopilotHostState
  attempt?: number
  class?: string
  since?: string
  release?: string
  target?: string
  pinned?: boolean
}

export const AUTOPILOT_HOST_LABEL: Record<AutopilotHostState, { text: string; badge: string }> = {
  idle: { text: 'Idle', badge: 'text-bg-light border' },
  rolling: { text: 'Rolling', badge: 'text-bg-info' },
  gating: { text: 'Gating', badge: 'text-bg-primary' },
  retrying: { text: 'Retrying', badge: 'text-bg-warning' },
  'rolled-back': { text: 'Rolled back', badge: 'text-bg-warning' },
  'needs-hands': { text: 'Needs hands', badge: 'text-bg-danger' }
}

export function autopilotHostState(raw: HostAutopilot | null | undefined): AutopilotHostState {
  return AUTOPILOT_HOST_STATES.includes(raw?.state as AutopilotHostState)
    ? (raw?.state as AutopilotHostState)
    : 'idle'
}

/**
 * Release states; `skipped` is a release the fleet moved past without ever
 * adopting it (a newer soaked release won the wave) and is history like `good`.
 */
export const RELEASE_STATES = ['rolling', 'timeout', 'quarantined', 'good', 'skipped'] as const
export type ReleaseState = (typeof RELEASE_STATES)[number]

export const RELEASE_STATE_LABEL: Record<ReleaseState, { text: string; badge: string }> = {
  rolling: { text: 'Rolling', badge: 'text-bg-primary' },
  timeout: { text: 'Timeout', badge: 'text-bg-warning' },
  quarantined: { text: 'Quarantined', badge: 'text-bg-danger' },
  good: { text: 'Good', badge: 'text-bg-success' },
  skipped: { text: 'Skipped', badge: 'text-bg-success' }
}

/** States that are history rather than live: folded behind the good-releases toggle. */
export function isHistoryState(state: ReleaseState): boolean {
  return state === 'good' || state === 'skipped'
}

export interface AutopilotRelease {
  os: HostOS
  version: string
  state: ReleaseState
  since: string
  attempts: number
  class: string
  report: string
  healthy: string[]
  failedOn: string
  failing: boolean
  /** False once the release's files were pruned; the record stays as history. */
  cached: boolean
  /** When the first host passed the gate on it (the soak clock); "" until then or from an older server. */
  firstHealthyAt: string
  /** Server-computed: healthy on a canary for the soak, so non-canaries may move; false from an older server. */
  soaked: boolean
}

export const WAVE_OUTCOMES = ['rolling', 'done', 'aborted'] as const
export type WaveOutcome = (typeof WAVE_OUTCOMES)[number]

/** One pass of an OS's non-canary hosts into one release (`OSState.wave`). */
export interface AutopilotWave {
  release: string
  startedAt: string
  /** "" while the wave is open. */
  endedAt: string
  outcome: WaveOutcome
}

export interface AutopilotOS {
  fleetTarget: string
  current: string
  lastGood: string
  held: boolean
  /** The live records plus the newest good pruned ones; `?releases=all` lists every record. */
  releases: AutopilotRelease[]
  /** How many records the controller holds for the OS in all. */
  releaseCount: number
  /** The newest wave; null when there never was one (or the server predates waves). */
  wave: AutopilotWave | null
  /** RFC3339, only while the cooldown blocks the next wave; "" otherwise. */
  nextWaveAt: string
  /** Registered, not-excluded hosts of the OS with `canary: true`. */
  canaries: number
}

export interface AutopilotAttempt {
  attempt: number
  target: string
  outcome: string
  class: string
  ended: string
  note: string
}

export interface AutopilotEpisode {
  os: HostOS
  release: string
  target: string
  attempt: number
  state: AutopilotHostState
  class: string
  since: string
  started: string
  t0: string
  actuator: string
  rollingBack: boolean
  retry: boolean
  done: boolean
  note: string
  timeline: string[]
  attempts: AutopilotAttempt[]
}

export interface AutopilotHost {
  mac: string
  os: HostOS | ''
  healthyOn: string
  pinned: boolean
  episode: AutopilotEpisode | null
}

export interface AutopilotEvent {
  at: string
  kind: string
  os: string
  release: string
  mac: string
  text: string
}

export interface AutopilotReport {
  key: string
  os: string
  version: string
  lastGood: string
  draft: boolean
  createdAt: string
  updatedAt: string
  class: string
  attempts: number
  rollbackResult: string
  /** URL of the rendered Markdown (`/autopilot/reports/<os>-<version>.md`); empty when not written. */
  path: string
  postedURL: string
  postedAt: string
  postAction: string
  postError: string
}

/** GET /autopilot: the P2 cluster/actuator view plus the controller's state. */
export interface AutopilotStatus {
  mode: string
  actuator: string
  dryRun: boolean
  healthWindow: string
  retryAfter: string
  /** `--autopilotSoak` as a Go duration string; "" when 0 or from an older server. */
  soak: string
  /** `--autopilotCooldown` as a Go duration string; "" when 0 or from an older server. */
  cooldown: string
  cluster: { reachable: boolean; kured: boolean; nodes: number; apiServer: string; error: string }
  /** Changes whenever the controller's state did; 0 from a server that does not send it. */
  revision: number
  os: Record<HostOS, AutopilotOS>
  hosts: AutopilotHost[]
  /** The newest 100 of the ring; `?events=all` returns the whole ring. */
  events: AutopilotEvent[]
  /** How many events the ring holds in all. */
  eventCount: number
  reports: AutopilotReport[]
  held: string[]
  quarantined: number
  needsHands: number
}

export type RawAutopilotStatus = Partial<
  Omit<AutopilotStatus, 'cluster' | 'os' | 'hosts' | 'events' | 'reports' | 'held'>
> & {
  cluster?: Partial<AutopilotStatus['cluster']>
  os?: Partial<
    Record<
      HostOS,
      Partial<Omit<AutopilotOS, 'releases' | 'wave'>> & {
        releases?: Partial<AutopilotRelease>[] | null
        wave?: (Partial<Omit<AutopilotWave, 'outcome'>> & { outcome?: string }) | null
      }
    >
  >
  hosts?: (Partial<Omit<AutopilotHost, 'episode'>> & {
    episode?:
      | (Partial<Omit<AutopilotEpisode, 'attempts' | 'signals'>> & {
          signals?: { timeline?: string[] }
          attempts?: Partial<AutopilotAttempt>[]
        })
      | null
  })[]
  events?: Partial<AutopilotEvent>[]
  reports?: Partial<AutopilotReport>[]
  held?: (string | null)[]
}

function emptyOS(): AutopilotOS {
  return {
    fleetTarget: '',
    current: '',
    lastGood: '',
    held: false,
    releases: [],
    releaseCount: 0,
    wave: null,
    nextWaveAt: '',
    canaries: 0
  }
}

export function normalizeAutopilotStatus(
  raw: RawAutopilotStatus | null | undefined
): AutopilotStatus {
  const os = {} as Record<HostOS, AutopilotOS>
  for (const name of OS_OPTIONS) {
    const block = raw?.os?.[name]
    const releases = (block?.releases ?? []).map((r) => ({
      os: name,
      version: r.version ?? '',
      state: oneOf(r.state, RELEASE_STATES, 'rolling'),
      since: r.since ?? '',
      attempts: r.attempts ?? 0,
      class: r.class ?? '',
      report: r.report ?? '',
      healthy: r.healthy ?? [],
      failedOn: r.failedOn ?? '',
      failing: r.failing ?? false,
      cached: r.cached ?? true,
      firstHealthyAt: r.firstHealthyAt ?? '',
      soaked: r.soaked ?? false
    }))
    const wave = block?.wave
    os[name] = {
      ...emptyOS(),
      fleetTarget: block?.fleetTarget ?? '',
      current: block?.current ?? '',
      lastGood: block?.lastGood ?? '',
      held: block?.held ?? false,
      releases,
      releaseCount: Math.max(releases.length, nonNegative(block?.releaseCount)),
      wave:
        wave && wave.release
          ? {
              release: wave.release,
              startedAt: wave.startedAt ?? '',
              endedAt: wave.endedAt ?? '',
              outcome: oneOf(wave.outcome, WAVE_OUTCOMES, wave.endedAt ? 'done' : 'rolling')
            }
          : null,
      nextWaveAt: block?.nextWaveAt ?? '',
      canaries: nonNegative(block?.canaries)
    }
  }
  const events = (raw?.events ?? []).map((e) => ({
    at: e.at ?? '',
    kind: e.kind ?? '',
    os: e.os ?? '',
    release: e.release ?? '',
    mac: e.mac ?? '',
    text: e.text ?? ''
  }))
  return {
    mode: raw?.mode ?? 'off',
    actuator: raw?.actuator ?? 'none',
    dryRun: raw?.dryRun ?? true,
    healthWindow: raw?.healthWindow ?? '',
    retryAfter: raw?.retryAfter ?? '',
    soak: raw?.soak ?? '',
    cooldown: raw?.cooldown ?? '',
    cluster: {
      reachable: raw?.cluster?.reachable ?? false,
      kured: raw?.cluster?.kured ?? false,
      nodes: raw?.cluster?.nodes ?? 0,
      apiServer: raw?.cluster?.apiServer ?? '',
      error: raw?.cluster?.error ?? ''
    },
    revision: nonNegative(raw?.revision),
    os,
    hosts: (raw?.hosts ?? []).map((h) => ({
      mac: h.mac ?? '',
      os: h.os ?? '',
      healthyOn: h.healthyOn ?? '',
      pinned: h.pinned ?? false,
      episode: h.episode
        ? {
            os: oneOf(h.episode.os || h.os || undefined, OS_OPTIONS, 'flatcar'),
            release: h.episode.release ?? '',
            target: h.episode.target ?? '',
            attempt: h.episode.attempt ?? 0,
            state: autopilotHostState(h.episode as HostAutopilot),
            class: h.episode.class ?? '',
            since: h.episode.since ?? '',
            started: h.episode.started ?? '',
            t0: h.episode.t0 ?? '',
            actuator: h.episode.actuator ?? '',
            rollingBack: h.episode.rollingBack ?? false,
            retry: h.episode.retry ?? false,
            done: h.episode.done ?? false,
            note: h.episode.note ?? '',
            timeline: h.episode.signals?.timeline ?? [],
            attempts: (h.episode.attempts ?? []).map((a) => ({
              attempt: a.attempt ?? 0,
              target: a.target ?? '',
              outcome: a.outcome ?? '',
              class: a.class ?? '',
              ended: a.ended ?? '',
              note: a.note ?? ''
            }))
          }
        : null
    })),
    events,
    eventCount: Math.max(events.length, nonNegative(raw?.eventCount)),
    reports: (raw?.reports ?? []).map((r) => ({
      key: r.key ?? '',
      os: r.os ?? '',
      version: r.version ?? '',
      lastGood: r.lastGood ?? '',
      draft: r.draft ?? true,
      createdAt: r.createdAt ?? '',
      updatedAt: r.updatedAt ?? '',
      class: r.class ?? '',
      attempts: r.attempts ?? 0,
      rollbackResult: r.rollbackResult ?? '',
      path: r.path ?? '',
      postedURL: r.postedURL ?? '',
      postedAt: r.postedAt ?? '',
      postAction: r.postAction ?? '',
      postError: r.postError ?? ''
    })),
    held: (raw?.held ?? []).filter((h): h is string => typeof h === 'string' && h !== ''),
    quarantined: raw?.quarantined ?? 0,
    needsHands: raw?.needsHands ?? 0
  }
}
