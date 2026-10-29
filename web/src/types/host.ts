/** Hosts as /booty.json, /hosts and /register carry them (Go: pkg/hardware.Host, BootyData). */

import { isRecord } from './common'
import type { HostAutopilot } from './autopilot'
import type { RawHostPower } from './power'

export const OS_OPTIONS = ['flatcar', 'coreos', 'bluefin'] as const
export type HostOS = (typeof OS_OPTIONS)[number]

export const INSTALL_DISK_OS: readonly HostOS[] = ['bluefin', 'coreos']

export function acceptsInstallDisk(os: HostOS | '' | undefined): boolean {
  return Boolean(os) && INSTALL_DISK_OS.includes(os as HostOS)
}

export const ROLE_OPTIONS = ['worker', 'control-plane'] as const
export type HostRole = (typeof ROLE_OPTIONS)[number]

/** Bluefin Server opt-in sysexts with a fixed name; kubestellar runs on k0s and needs it. */
export const BLUEFIN_EXTENSIONS = ['zfs', 'kubestellar', 'k0s', 'nvidia-container-toolkit'] as const

/**
 * NVIDIA driver sysext, one per host and not alongside zfs. The server
 * accepts any nvidia-open-<branch>; the known branches are only suggestions.
 */
export type NvidiaDriverFlavour = `nvidia-open-${number}`
export const NVIDIA_DRIVER_FLAVOURS: readonly NvidiaDriverFlavour[] = ['nvidia-open-595']

export type BluefinExtension = (typeof BLUEFIN_EXTENSIONS)[number] | NvidiaDriverFlavour

/** Bluefin Server boot modes; the server treats "" as `diskless`. */
export const BLUEFIN_MODES = ['diskless', 'installed'] as const
export type BluefinMode = (typeof BLUEFIN_MODES)[number]

/**
 * The /register body for host: the Bluefin-only fields are dropped for other
 * operating systems, which the server refuses them for.
 */
export function registerPayload(host: Host): Host {
  if (host.os === 'bluefin') return host
  const payload = { ...host }
  delete payload.stateDisk
  delete payload.extensions
  delete payload.mode
  return payload
}

/** Role for display: the server treats a missing/empty role as `worker`. */
export function hostRole(role: HostRole | '' | undefined): HostRole {
  return role === 'control-plane' ? 'control-plane' : 'worker'
}

export interface Host {
  mac: string
  hostname: string
  ip: string
  /** RFC3339 timestamp of the last Ignition fetch, or "" if never booted. */
  booted: string
  ignitionFile?: string
  os?: HostOS | ''
  ostreeImage?: string
  /**
   * Target disk for the installer (e.g. `/dev/sda`); "" lets the CoreOS
   * installer pick the first writable disk, while a Bluefin install needs
   * one. Only meaningful for bluefin and coreos.
   */
  installDisk: string
  doInstall?: boolean
  /**
   * Cluster role; "" (or absent, on older servers) means `worker`. Sent to
   * `/register` exactly as stored so payloads from older UIs stay identical.
   */
  role?: HostRole | ''
  /** Bluefin only: disk Ignition keeps /var on (partition bluefin-var, never wiped). */
  stateDisk?: string
  /** Bluefin only: opt-in sysexts Ignition installs on every boot. */
  extensions?: BluefinExtension[]
  /**
   * Bluefin only: "" or `diskless` boots over UEFI HTTP Boot; `installed`
   * boots its disk (set by the server once an install finishes).
   */
  mode?: BluefinMode | ''
  /**
   * True when the host's last boot script was fetched through the Secure
   * Boot path (the signed iPXE handed out over UEFI HTTP Boot). Set by the
   * server, cleared by a plain PXE fetch; not a firmware attestation.
   */
  secureBoot?: boolean
  /**
   * Autopilot canary: under `--autopilot=full` a new Bluefin release goes to
   * canary hosts first (and a TIMEOUT release is retried on one).
   */
  canary?: boolean
  /** Server-owned autopilot summary of the host's current episode; read-only in the UI. */
  autopilot?: HostAutopilot
  /** Server-owned power state (see `HostPower`); read-only in the UI. */
  power?: RawHostPower
  /**
   * Cached release of the host's OS Booty serves this MAC; "" means the
   * fleet target. Set by the operator or the autopilot (`autopilot.pinned`).
   */
  targetVersion?: string
  /** Server-owned: the last report from the node's booty-health.service, if any. */
  health?: RawHostHealth | null
  /**
   * Version the host last reported, or `image@digest` for ostree hosts.
   * "" if the host has never checked in.
   */
  running: string
  /** RFC3339 timestamp of the last check-in, or "" if never. */
  lastCheck: string
  /** True when the server has a newer version/image than the host is running. */
  rebootPending: boolean
}

/** POST /health body as stored on the host; only `receivedAt` matters to the UI. */
export interface RawHostHealth {
  receivedAt?: string
  bootID?: string
  running?: string
  failedUnits?: string[]
  journalErrors?: string[]
  firmware?: string
  kernel?: string
}

/** The OS a host boots; the server treats "" as flatcar. */
export function hostOS(os: HostOS | '' | undefined): HostOS {
  return OS_OPTIONS.includes(os as HostOS) ? (os as HostOS) : 'flatcar'
}

export const STALE_AFTER_MS = 30 * 60 * 1000

/**
 * Whether the host's agent (the `update`/`health` builtin units) has ever
 * talked to Booty: `never` when nothing was recorded at all (booted before
 * those units existed, or `--builtin=none`), `stale` when its last
 * check-in is older than STALE_AFTER_MS, else `reporting`.
 */
export type HostReporting = 'reporting' | 'never' | 'stale'

export function hostReporting(
  host: Pick<Host, 'lastCheck' | 'booted' | 'health'>,
  now: Date = new Date()
): HostReporting {
  if (!host.lastCheck && !host.booted && !host.health) return 'never'
  if (!host.lastCheck) return 'reporting'
  const last = new Date(host.lastCheck).getTime()
  if (Number.isNaN(last)) return 'reporting'
  return now.getTime() - last > STALE_AFTER_MS ? 'stale' : 'reporting'
}

/** GET /info `targets`: the fleet target per OS (`versions.FleetTarget`). */
export type FleetTargets = Partial<Record<HostOS, string>>

export type VersionMatch = 'match' | 'behind' | 'unknown'

/** The release `versionMatch` compares against for host. */
export function hostTarget(
  host: Pick<Host, 'os' | 'targetVersion'>,
  targets: FleetTargets
): string {
  return host.targetVersion || targets[hostOS(host.os)] || ''
}

/**
 * Compares what the host runs with the release Booty would boot it into:
 * its `targetVersion`, else the fleet target of its OS. `unknown` when
 * either side is missing or the host runs an ostree image (`image@digest`,
 * compared by digest on the server, not by version).
 */
export function versionMatch(
  host: Pick<Host, 'os' | 'running' | 'targetVersion' | 'ostreeImage'>,
  targets: FleetTargets
): VersionMatch {
  const target = hostTarget(host, targets)
  if (!host.running || !target || host.ostreeImage || host.running.includes('@')) return 'unknown'
  return host.running === target ? 'match' : 'behind'
}

export interface UnknownHost {
  mac: string
  ip: string
  firstSeen: string
  lastSeen: string
  count: number
}

export interface BootyData {
  hosts: Record<string, Host>
  unknownHosts: Record<string, UnknownHost>
}

export interface RegisterResponse {
  status: string
  host: Host
}

export function normalizeHost(raw: Partial<Host>, fallbackMac = ''): Host {
  return {
    ...raw,
    mac: raw.mac ?? fallbackMac,
    hostname: raw.hostname ?? '',
    ip: raw.ip ?? '',
    booted: raw.booted ?? '',
    installDisk: raw.installDisk ?? '',
    running: raw.running ?? '',
    lastCheck: raw.lastCheck ?? '',
    rebootPending: raw.rebootPending ?? false
  }
}

export type RawBootyData = Partial<Omit<BootyData, 'hosts'>> & {
  hosts?: Record<string, Partial<Host>>
}

export function normalizeBootyData(raw: RawBootyData | null | undefined): BootyData {
  const hosts: Record<string, Host> = {}
  for (const [mac, host] of Object.entries(raw?.hosts ?? {})) {
    // A null or non-object entry is a broken record, not a host.
    if (isRecord(host)) hosts[mac] = normalizeHost(host, mac)
  }
  return {
    hosts,
    unknownHosts: isRecord(raw?.unknownHosts) ? raw.unknownHosts : {}
  }
}
