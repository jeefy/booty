/** GET /info, /flatcar/pin and /healthz (Go: pkg/server infoResponse, pinResponse). */

import { isRecord } from './common'
import { OS_OPTIONS, type FleetTargets, type HostOS } from './host'
import type { PowerSummary } from './power'

export interface FleetInfo {
  hosts?: number
  pendingReboots?: number
}

export interface FlatcarCAInfo {
  flatcarVersion: string
  sha256: string
  subject: string
  notAfter: string
  /** Download URL of the DER certificate to enroll in the firmware db. */
  url: string
}

/** GET /info `secureBoot` block; `warnings` names hosts that cannot boot through Secure Boot. */
export interface SecureBootInfo {
  enabled: boolean
  ready: boolean
  bundleVersion: string
  trusted: string[]
  bootURL: string
  flatcarCA: FlatcarCAInfo | null
  warnings: string[]
}

export type RawSecureBootInfo = Partial<
  Omit<SecureBootInfo, 'flatcarCA' | 'trusted' | 'warnings'>
> & {
  flatcarCA?: Partial<FlatcarCAInfo> | null
  trusted?: (string | null)[]
  warnings?: (string | null)[]
}

export function normalizeSecureBootInfo(raw: RawSecureBootInfo | null | undefined): SecureBootInfo {
  const ca = raw?.flatcarCA
  return {
    enabled: raw?.enabled ?? false,
    ready: raw?.ready ?? false,
    bundleVersion: raw?.bundleVersion ?? '',
    trusted: (raw?.trusted ?? []).filter((t): t is string => typeof t === 'string' && t !== ''),
    bootURL: raw?.bootURL ?? '',
    flatcarCA:
      ca && ca.sha256
        ? {
            flatcarVersion: ca.flatcarVersion ?? '',
            sha256: ca.sha256,
            subject: ca.subject ?? '',
            notAfter: ca.notAfter ?? '',
            url: ca.url ?? ''
          }
        : null,
    warnings: (raw?.warnings ?? []).filter((w): w is string => typeof w === 'string' && w !== '')
  }
}

/** GET /info `autopilot` block. */
export interface AutopilotInfo {
  mode: string
  actuator: string
  held: string[]
  quarantined: number
  needsHands: number
}

export type RawAutopilotInfo = Partial<Omit<AutopilotInfo, 'held'>> & { held?: (string | null)[] }

export function normalizeAutopilotInfo(raw: RawAutopilotInfo | null | undefined): AutopilotInfo {
  return {
    mode: raw?.mode ?? 'off',
    actuator: raw?.actuator ?? 'none',
    held: (raw?.held ?? []).filter((h): h is string => typeof h === 'string' && h !== ''),
    quarantined: raw?.quarantined ?? 0,
    needsHands: raw?.needsHands ?? 0
  }
}

export interface Info {
  flatcar?: { version?: string; pinnedVersion?: string }
  coreos?: { version?: string }
  bluefin?: { version?: string; pinnedVersion?: string }
  booty?: { version?: string; timestamp?: string }
  fleet?: FleetInfo
  /** Fleet target per OS (hold-aware); older servers omit it. */
  targets?: Partial<Record<HostOS, string | null>>
  secureBoot?: RawSecureBootInfo
  autopilot?: RawAutopilotInfo
  power?: Partial<PowerSummary>
  /** Outstanding release-sync / scheduler failures by name; omitted when none. */
  syncErrors?: Record<string, SyncErrorInfo>
}

/** One /info `syncErrors` entry: the last failure and when it happened (RFC3339). */
export interface SyncErrorInfo {
  error: string
  at: string
}

/** Keeps the entries with a string `error`; a missing `at` becomes "". */
export function normalizeSyncErrors(value: unknown): Record<string, SyncErrorInfo> | undefined {
  if (!isRecord(value)) return undefined
  const out: Record<string, SyncErrorInfo> = {}
  for (const [name, entry] of Object.entries(value)) {
    if (!isRecord(entry) || typeof entry.error !== 'string') continue
    out[name] = { error: entry.error, at: typeof entry.at === 'string' ? entry.at : '' }
  }
  return Object.keys(out).length > 0 ? out : undefined
}

export const BLUEFIN_NOT_DOWNLOADED = '0.0.0'

/**
 * The fleet target per OS: `/info.targets` when the server sends it, else
 * the per-OS `version` blocks (a Flatcar pin wins; Bluefin's `0.0.0` means
 * no release yet).
 */
export function fleetTargets(info: Info): FleetTargets {
  const bluefin = info.bluefin?.version || ''
  const fallback: FleetTargets = {
    flatcar: info.flatcar?.pinnedVersion || info.flatcar?.version || '',
    coreos: info.coreos?.version || '',
    bluefin: bluefin === BLUEFIN_NOT_DOWNLOADED ? '' : bluefin
  }
  if (!info.targets) return fallback
  const targets: FleetTargets = {}
  for (const os of OS_OPTIONS) {
    targets[os] = info.targets[os] || fallback[os] || ''
  }
  return targets
}

export interface PinState {
  pinned: boolean
  version: string
  current: string
}

export interface Health {
  status: string
}

/** /info as the server sends it; every field may be missing or mistyped on old servers. */
export type RawInfo = { [K in keyof Info]?: unknown }

function section<T>(value: unknown): T | undefined {
  // Info's sections are all-optional bags of optional fields, so an object
  // is accepted as-is and anything else is dropped.
  return isRecord(value) ? (value as T) : undefined
}

/** Keeps the Info shape but drops sections that are not objects. */
export function normalizeInfo(raw: RawInfo | null | undefined): Info {
  if (!raw) return {}
  return {
    flatcar: section<Info['flatcar']>(raw.flatcar),
    coreos: section<Info['coreos']>(raw.coreos),
    bluefin: section<Info['bluefin']>(raw.bluefin),
    booty: section<Info['booty']>(raw.booty),
    fleet: section<FleetInfo>(raw.fleet),
    targets: section<Info['targets']>(raw.targets),
    secureBoot: section<RawSecureBootInfo>(raw.secureBoot),
    autopilot: section<RawAutopilotInfo>(raw.autopilot),
    power: section<Partial<PowerSummary>>(raw.power),
    syncErrors: normalizeSyncErrors(raw.syncErrors)
  }
}
