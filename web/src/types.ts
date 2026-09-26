/** Wire types for the Booty HTTP API. Keep in sync with the Go structs in pkg/. */

export const OS_OPTIONS = ['flatcar', 'coreos', 'bluefin'] as const
export type HostOS = (typeof OS_OPTIONS)[number]

export const INSTALL_DISK_OS: readonly HostOS[] = ['bluefin', 'coreos']

export function acceptsInstallDisk(os: HostOS | '' | undefined): boolean {
  return Boolean(os) && INSTALL_DISK_OS.includes(os as HostOS)
}

export const ROLE_OPTIONS = ['worker', 'control-plane'] as const
export type HostRole = (typeof ROLE_OPTIONS)[number]

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
   * Target disk for the installer (e.g. `/dev/sda`); "" lets the installer
   * pick the first writable disk. Only meaningful for bluefin and coreos.
   */
  installDisk: string
  doInstall?: boolean
  /**
   * Cluster role; "" (or absent, on older servers) means `worker`. Sent to
   * `/register` exactly as stored so payloads from older UIs stay identical.
   */
  role?: HostRole | ''
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

export interface Info {
  flatcar?: { version?: string; pinnedVersion?: string }
  coreos?: { version?: string }
  bluefin?: { version?: string; pinnedVersion?: string }
  booty?: { version?: string; timestamp?: string }
  fleet?: FleetInfo
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
}

/** GET /cluster response. `caFingerprint` is the public-key hash, never key material. */
export interface ClusterInfo {
  distribution: ClusterDistribution
  controlPlane: ControlPlaneMode
  endpoint: string
  cni: ClusterCNI
  ready: boolean
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
  return {
    distribution: oneOf(raw?.distribution, CLUSTER_DISTRIBUTIONS, 'kubeadm'),
    controlPlane: oneOf(raw?.controlPlane, CONTROL_PLANE_MODES, 'external'),
    endpoint: raw?.endpoint ?? '',
    cni: oneOf(raw?.cni, CLUSTER_CNIS, 'none'),
    ready: raw?.ready ?? false,
    caFingerprint: raw?.caFingerprint ?? '',
    hosts: (raw?.hosts ?? []).map((h) => ({
      mac: h.mac ?? '',
      hostname: h.hostname ?? '',
      os: h.os ?? '',
      role: hostRole(h.role),
      booted: h.booted ?? ''
    })),
    warnings: (raw?.warnings ?? []).filter((w): w is string => typeof w === 'string' && w !== '')
  }
}
