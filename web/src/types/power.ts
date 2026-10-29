/** Power tracker state: the host `power` block and GET /power (Go: pkg/power, pkg/hardware.HostPower). */

export const POWER_STATES = [
  'unknown',
  'off',
  'powering-on',
  'booting',
  'up',
  'draining',
  'rebooting',
  'shutting-down',
  'unreachable'
] as const
export type PowerState = (typeof POWER_STATES)[number]

export const POWER_REQUESTS = ['on', 'reboot', 'shutdown'] as const
export type PowerRequest = (typeof POWER_REQUESTS)[number]

/** Server-owned power block on a host (`power` field, GET /power `hosts`). */
export interface HostPower {
  state: PowerState
  since: string
  reason: string
  request: PowerRequest | ''
  requestedBy: string
  requestedAt: string
  lastSeen: string
  cordoned: boolean
  probe: { ok: boolean; at: string; method: string }
}

export type RawHostPower = Partial<Omit<HostPower, 'probe' | 'state' | 'request'>> & {
  state?: string
  request?: string
  probe?: Partial<HostPower['probe']> | null
}

export function powerState(raw: string | null | undefined): PowerState {
  return POWER_STATES.includes(raw as PowerState) ? (raw as PowerState) : 'unknown'
}

export function normalizeHostPower(raw: RawHostPower | null | undefined): HostPower {
  return {
    state: powerState(raw?.state),
    since: raw?.since ?? '',
    reason: raw?.reason ?? '',
    request: POWER_REQUESTS.includes(raw?.request as PowerRequest)
      ? (raw?.request as PowerRequest)
      : '',
    requestedBy: raw?.requestedBy ?? '',
    requestedAt: raw?.requestedAt ?? '',
    lastSeen: raw?.lastSeen ?? '',
    cordoned: raw?.cordoned ?? false,
    probe: {
      ok: raw?.probe?.ok ?? false,
      at: raw?.probe?.at ?? '',
      method: raw?.probe?.method ?? ''
    }
  }
}

/** Badge colour and label per power state; `dot` is the status-dot modifier. */
export const POWER_STATE_LABEL: Record<PowerState, { text: string; badge: string; dot: string }> = {
  unknown: { text: 'Unknown', badge: 'text-bg-secondary', dot: 'power-dot--unknown' },
  off: { text: 'Off', badge: 'text-bg-dark', dot: 'power-dot--off' },
  'powering-on': { text: 'Powering on', badge: 'text-bg-info', dot: 'power-dot--busy' },
  booting: { text: 'Booting', badge: 'text-bg-info', dot: 'power-dot--busy' },
  up: { text: 'Up', badge: 'text-bg-success', dot: 'power-dot--up' },
  draining: { text: 'Draining', badge: 'text-bg-warning', dot: 'power-dot--busy' },
  rebooting: { text: 'Rebooting', badge: 'text-bg-warning', dot: 'power-dot--busy' },
  'shutting-down': { text: 'Shutting down', badge: 'text-bg-warning', dot: 'power-dot--busy' },
  unreachable: { text: 'Unreachable', badge: 'text-bg-danger', dot: 'power-dot--down' }
}

export const POWER_ACTIONS = ['on', 'reboot', 'shutdown'] as const
export type PowerAction = (typeof POWER_ACTIONS)[number]

export const POWER_ACTION_LABEL: Record<PowerAction, string> = {
  on: 'Power on',
  reboot: 'Reboot',
  shutdown: 'Shutdown'
}

/** States a power action is allowed from (mirrors the server's guards). */
export const POWER_ACTION_FROM: Record<PowerAction, readonly PowerState[]> = {
  on: ['off', 'unreachable', 'unknown'],
  reboot: ['up'],
  shutdown: ['up']
}

export interface PowerCapabilities {
  wol: boolean
  /** Operator actuator: `ssh`, `api` or `none` (reboot/shutdown disabled). */
  actuator: string
}

export interface PowerSummary {
  up: number
  off: number
  unreachable: number
  inFlight: number
}

export interface PowerEvent {
  at: string
  kind: string
  mac: string
  text: string
}

/** GET /power. */
export interface PowerStatus {
  hosts: Record<string, HostPower>
  events: PowerEvent[]
  capabilities: PowerCapabilities
  summary: PowerSummary
}

export type RawPowerStatus = Partial<
  Omit<PowerStatus, 'hosts' | 'events' | 'capabilities' | 'summary'>
> & {
  hosts?: Record<string, RawHostPower | null>
  events?: Partial<PowerEvent>[]
  capabilities?: Partial<PowerCapabilities>
  summary?: Partial<PowerSummary>
}

export function normalizePowerSummary(raw: Partial<PowerSummary> | null | undefined): PowerSummary {
  return {
    up: raw?.up ?? 0,
    off: raw?.off ?? 0,
    unreachable: raw?.unreachable ?? 0,
    inFlight: raw?.inFlight ?? 0
  }
}

export function normalizePowerStatus(raw: RawPowerStatus | null | undefined): PowerStatus {
  const hosts: Record<string, HostPower> = {}
  for (const [mac, p] of Object.entries(raw?.hosts ?? {})) {
    hosts[mac] = normalizeHostPower(p)
  }
  return {
    hosts,
    events: (raw?.events ?? []).map((e) => ({
      at: e.at ?? '',
      kind: e.kind ?? 'power',
      mac: e.mac ?? '',
      text: e.text ?? ''
    })),
    capabilities: {
      wol: raw?.capabilities?.wol ?? false,
      actuator: raw?.capabilities?.actuator ?? 'none'
    },
    summary: normalizePowerSummary(raw?.summary)
  }
}

/** POST /power/{mac}/{action} body. */
export interface PowerActionRequest {
  reason?: string
  force?: boolean
  drain?: boolean
}

/** POST /power/{mac}/{action} 202 body (a 409 carries `error` and `power` instead). */
export interface PowerActionResponse {
  status: string
  mac: string
  power: RawHostPower
}
