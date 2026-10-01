/** Wire types for the Booty HTTP API. Keep in sync with the Go structs in pkg/. */

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

export interface CachedImage {
  registry: string
  image: string
  tag: string
  digest: string
  upToDate: boolean
}

export interface Health {
  status: string
}

export interface RegisterResponse {
  status: string
  host: Host
}

export interface StatusResponse {
  status: string
}

export type SettingSource = 'flag' | 'env' | 'default'

export interface ConfigSetting {
  key: string
  /** Rendered value; "" when redacted (the server never sends secrets). */
  value: string
  source: SettingSource
  redacted: boolean
}

export type TemplateSource = 'file' | 'embedded'

export interface TemplateInfo {
  name: string
  /** `embedded` when no file exists yet and Booty renders its built-in template. */
  source: TemplateSource
  writable: boolean
}

export interface HostTemplateRef {
  mac: string
  hostname: string
  name: string
}

export interface EffectiveConfig {
  settings: ConfigSetting[]
  templates: {
    default: TemplateInfo
    hosts: HostTemplateRef[]
  }
  /** Set when the template store as a whole is read-only (e.g. mounted ConfigMap). */
  readOnlyReason?: string
}

export interface TemplateDocument extends TemplateInfo {
  content: string
  readOnlyReason?: string
}

export type ValidationKind = 'warning' | 'error'

export interface ValidationEntry {
  kind: ValidationKind
  message: string
}

/** POST /config/template/validate response (200 even when `ok` is false). */
export interface TemplateValidation {
  ok: boolean
  /** Rendered Ignition JSON; may be "" when translation failed. */
  ignition: string
  entries: ValidationEntry[]
}

export type RawEffectiveConfig = Partial<Omit<EffectiveConfig, 'settings' | 'templates'>> & {
  settings?: Partial<ConfigSetting>[]
  templates?: {
    default?: Partial<TemplateInfo>
    hosts?: Partial<HostTemplateRef>[]
  }
}

export function normalizeTemplateInfo(raw: Partial<TemplateInfo> | null | undefined): TemplateInfo {
  return {
    name: raw?.name ?? '',
    source: raw?.source ?? 'file',
    writable: raw?.writable ?? false
  }
}

export function normalizeEffectiveConfig(
  raw: RawEffectiveConfig | null | undefined
): EffectiveConfig {
  const settings: ConfigSetting[] = (raw?.settings ?? []).map((s) => ({
    key: s.key ?? '',
    value: s.value ?? '',
    source: s.source ?? 'default',
    redacted: s.redacted ?? false
  }))
  const hosts: HostTemplateRef[] = (raw?.templates?.hosts ?? [])
    .filter((h) => Boolean(h.name))
    .map((h) => ({ mac: h.mac ?? '', hostname: h.hostname ?? '', name: h.name ?? '' }))
  return {
    settings,
    templates: { default: normalizeTemplateInfo(raw?.templates?.default), hosts },
    ...(raw?.readOnlyReason ? { readOnlyReason: raw.readOnlyReason } : {})
  }
}

export function normalizeTemplateDocument(
  raw: Partial<TemplateDocument> | null | undefined
): TemplateDocument {
  return {
    ...normalizeTemplateInfo(raw),
    content: raw?.content ?? '',
    ...(raw?.readOnlyReason ? { readOnlyReason: raw.readOnlyReason } : {})
  }
}

export type RawTemplateValidation = Partial<Omit<TemplateValidation, 'entries'>> & {
  entries?: Partial<ValidationEntry>[]
}

export function normalizeTemplateValidation(
  raw: RawTemplateValidation | null | undefined
): TemplateValidation {
  return {
    ok: raw?.ok ?? false,
    ignition: raw?.ignition ?? '',
    entries: (raw?.entries ?? []).map((e) => ({
      kind: e.kind === 'warning' ? 'warning' : 'error',
      message: e.message ?? ''
    }))
  }
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
    hosts[mac] = normalizeHost(host ?? {}, mac)
  }
  return {
    hosts,
    unknownHosts: raw?.unknownHosts ?? {}
  }
}

export const CLUSTER_DISTRIBUTIONS = ['kubeadm', 'k0s'] as const
export type ClusterDistribution = (typeof CLUSTER_DISTRIBUTIONS)[number]

export const CONTROL_PLANE_MODES = ['managed', 'external'] as const
export type ControlPlaneMode = (typeof CONTROL_PLANE_MODES)[number]

export const CLUSTER_CNIS = ['cilium', 'calico', 'flannel', 'none'] as const
export type ClusterCNI = (typeof CLUSTER_CNIS)[number]

export interface ClusterHost {
  mac: string
  hostname: string
  os: HostOS | ''
  role: HostRole
  /** RFC3339 timestamp of the last Ignition fetch, or "" if never booted. */
  booted: string
  secureBoot: boolean
}

/**
 * GET /cluster response. `caFingerprint` is the CA's hash, never key material.
 * `source` says whose facts `ready`, `endpoint` and `caFingerprint` are:
 * `managed` (Booty's CA and the control-plane host's ready report) or
 * `external` (the API server Booty talks to, where `ready` means `connected`).
 * `connected`/`nodes`/`apiServer` describe the live API connection in both
 * modes; older servers omit them.
 */
export interface ClusterInfo {
  distribution: ClusterDistribution
  controlPlane: ControlPlaneMode
  source: ControlPlaneMode
  endpoint: string
  apiServer: string
  cni: ClusterCNI
  ready: boolean
  connected: boolean
  nodes: number
  caFingerprint: string
  hosts: ClusterHost[]
  warnings: string[]
}

export type RawClusterHost = Partial<Omit<ClusterHost, 'role'>> & { role?: HostRole | '' }

export type RawClusterInfo = Partial<Omit<ClusterInfo, 'hosts' | 'warnings'>> & {
  hosts?: RawClusterHost[]
  warnings?: (string | null)[]
}

function oneOf<T extends string>(value: string | undefined, options: readonly T[], fallback: T): T {
  return options.includes(value as T) ? (value as T) : fallback
}

export function normalizeClusterInfo(raw: RawClusterInfo | null | undefined): ClusterInfo {
  const controlPlane = oneOf(raw?.controlPlane, CONTROL_PLANE_MODES, 'external')
  return {
    distribution: oneOf(raw?.distribution, CLUSTER_DISTRIBUTIONS, 'kubeadm'),
    controlPlane,
    source: oneOf(raw?.source, CONTROL_PLANE_MODES, controlPlane),
    endpoint: raw?.endpoint ?? '',
    apiServer: raw?.apiServer ?? '',
    cni: oneOf(raw?.cni, CLUSTER_CNIS, 'none'),
    ready: raw?.ready ?? false,
    connected: raw?.connected ?? false,
    nodes: Math.max(0, Math.trunc(raw?.nodes ?? 0)) || 0,
    caFingerprint: raw?.caFingerprint ?? '',
    hosts: (raw?.hosts ?? []).map((h) => ({
      mac: h.mac ?? '',
      hostname: h.hostname ?? '',
      os: h.os ?? '',
      role: hostRole(h.role),
      booted: h.booted ?? '',
      secureBoot: h.secureBoot ?? false
    })),
    warnings: (raw?.warnings ?? []).filter((w): w is string => typeof w === 'string' && w !== '')
  }
}

export const RELEASE_STATES = ['rolling', 'timeout', 'quarantined', 'good'] as const
export type ReleaseState = (typeof RELEASE_STATES)[number]

export const RELEASE_STATE_LABEL: Record<ReleaseState, { text: string; badge: string }> = {
  rolling: { text: 'Rolling', badge: 'text-bg-primary' },
  timeout: { text: 'Timeout', badge: 'text-bg-warning' },
  quarantined: { text: 'Quarantined', badge: 'text-bg-danger' },
  good: { text: 'Good', badge: 'text-bg-success' }
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
}

export interface AutopilotOS {
  fleetTarget: string
  current: string
  lastGood: string
  held: boolean
  releases: AutopilotRelease[]
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
  cluster: { reachable: boolean; kured: boolean; nodes: number; apiServer: string; error: string }
  os: Record<HostOS, AutopilotOS>
  hosts: AutopilotHost[]
  events: AutopilotEvent[]
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
      Partial<Omit<AutopilotOS, 'releases'>> & { releases?: Partial<AutopilotRelease>[] | null }
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
  return { fleetTarget: '', current: '', lastGood: '', held: false, releases: [] }
}

export function normalizeAutopilotStatus(
  raw: RawAutopilotStatus | null | undefined
): AutopilotStatus {
  const os = {} as Record<HostOS, AutopilotOS>
  for (const name of OS_OPTIONS) {
    const block = raw?.os?.[name]
    os[name] = {
      ...emptyOS(),
      fleetTarget: block?.fleetTarget ?? '',
      current: block?.current ?? '',
      lastGood: block?.lastGood ?? '',
      held: block?.held ?? false,
      releases: (block?.releases ?? []).map((r) => ({
        os: name,
        version: r.version ?? '',
        state: oneOf(r.state, RELEASE_STATES, 'rolling'),
        since: r.since ?? '',
        attempts: r.attempts ?? 0,
        class: r.class ?? '',
        report: r.report ?? '',
        healthy: r.healthy ?? [],
        failedOn: r.failedOn ?? '',
        failing: r.failing ?? false
      }))
    }
  }
  return {
    mode: raw?.mode ?? 'off',
    actuator: raw?.actuator ?? 'none',
    dryRun: raw?.dryRun ?? true,
    healthWindow: raw?.healthWindow ?? '',
    retryAfter: raw?.retryAfter ?? '',
    cluster: {
      reachable: raw?.cluster?.reachable ?? false,
      kured: raw?.cluster?.kured ?? false,
      nodes: raw?.cluster?.nodes ?? 0,
      apiServer: raw?.cluster?.apiServer ?? '',
      error: raw?.cluster?.error ?? ''
    },
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
    events: (raw?.events ?? []).map((e) => ({
      at: e.at ?? '',
      kind: e.kind ?? '',
      os: e.os ?? '',
      release: e.release ?? '',
      mac: e.mac ?? '',
      text: e.text ?? ''
    })),
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
