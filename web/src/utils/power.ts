import {
  POWER_ACTION_FROM,
  POWER_ACTION_LABEL,
  POWER_STATE_LABEL,
  normalizeHostPower,
  powerState,
  type Host,
  type HostPower,
  type PowerAction,
  type PowerCapabilities,
  type PowerState,
  type PowerSummary
} from '@/types'

export const NO_ACTUATOR_REASON =
  'no actuator: configure --rebootSSHKey or run Booty in the cluster'
export const AUTOPILOT_DRIVING_REASON = 'autopilot is driving this host'

const IN_FLIGHT: readonly PowerState[] = ['powering-on', 'draining', 'rebooting', 'shutting-down']
const DRIVING = ['rolling', 'gating', 'retrying', 'rolled-back']

export function hostPower(host: Pick<Host, 'power'>): HostPower {
  return normalizeHostPower(host.power)
}

export function autopilotDriving(host: Pick<Host, 'autopilot'>): boolean {
  return DRIVING.includes(host.autopilot?.state ?? '')
}

export interface PowerButton {
  action: PowerAction
  label: string
  enabled: boolean
  /** Tooltip: why the button is disabled, or what it does. */
  title: string
  /** True when the server would refuse without `force` (control plane, last worker). */
  needsForce: boolean
  forceReason: string
}

export interface PowerContext {
  capabilities: PowerCapabilities | null
  /** True when this host is the only worker that is up. */
  lastUpWorker: boolean
}

export function powerButton(
  action: PowerAction,
  host: Pick<Host, 'power' | 'role' | 'autopilot'>,
  ctx: PowerContext
): PowerButton {
  const state = powerState(host.power?.state)
  const label = POWER_ACTION_LABEL[action]
  const button: PowerButton = {
    action,
    label,
    enabled: true,
    title: label,
    needsForce: false,
    forceReason: ''
  }
  const disable = (title: string) => ({ ...button, enabled: false, title })
  if (IN_FLIGHT.includes(state)) {
    return disable(`host is ${POWER_STATE_LABEL[state].text.toLowerCase()}`)
  }
  if (!POWER_ACTION_FROM[action].includes(state)) {
    return disable(
      `host is ${POWER_STATE_LABEL[state].text.toLowerCase()}; ${label.toLowerCase()} needs ${POWER_ACTION_FROM[action].join(', ')}`
    )
  }
  if (action === 'on') return button
  if (autopilotDriving(host)) return disable(AUTOPILOT_DRIVING_REASON)
  if (!ctx.capabilities || ctx.capabilities.actuator === 'none') return disable(NO_ACTUATOR_REASON)
  if (host.role === 'control-plane') {
    button.needsForce = true
    button.forceReason = 'control-plane host'
  } else if (action === 'shutdown' && ctx.lastUpWorker) {
    button.needsForce = true
    button.forceReason = 'last worker that is up'
  }
  return button
}

export function powerButtons(
  host: Pick<Host, 'power' | 'role' | 'autopilot'>,
  ctx: PowerContext
): PowerButton[] {
  return (['on', 'reboot', 'shutdown'] as const).map((a) => powerButton(a, host, ctx))
}

/** True when `host` is the only non-control-plane host in state `up`. */
export function isLastUpWorker(host: Pick<Host, 'mac' | 'role' | 'power'>, hosts: Host[]): boolean {
  if (host.role === 'control-plane' || powerState(host.power?.state) !== 'up') return false
  return !hosts.some(
    (h) => h.mac !== host.mac && h.role !== 'control-plane' && powerState(h.power?.state) === 'up'
  )
}

export function summarizePower(hosts: Host[]): PowerSummary {
  const s: PowerSummary = { up: 0, off: 0, unreachable: 0, inFlight: 0 }
  for (const h of hosts) {
    const state = powerState(h.power?.state)
    if (state === 'up') s.up++
    else if (state === 'off') s.off++
    else if (state === 'unreachable') s.unreachable++
    if (IN_FLIGHT.includes(state)) s.inFlight++
  }
  return s
}

export function powerSummaryText(s: PowerSummary): string {
  return `${s.up} up · ${s.off} off · ${s.unreachable} unreachable`
}
