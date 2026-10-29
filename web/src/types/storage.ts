/** GET /storage (Go: pkg/versions.Storage). */

import { nonNegative, oneOf, strings } from './common'
import { OS_OPTIONS, type HostOS } from './host'

export const ASSET_KINDS = ['ipxe', 'shim', 'syslinux', 'grub', 'other'] as const
export type AssetKind = (typeof ASSET_KINDS)[number]

export const RETAINED_BY = [
  'current',
  'previous',
  'lastGood',
  'pinned',
  'held',
  'netboot',
  'demoted',
  '-'
] as const
export type RetainedBy = (typeof RETAINED_BY)[number]

export const RELEASE_LINKS = ['current', 'previous', 'lastGood'] as const
export type ReleaseLink = (typeof RELEASE_LINKS)[number]

/** One DataDir/<os>/<version>/ directory as GET /storage reports it. */
export interface StorageRelease {
  version: string
  bytes: number
  files: number
  modified: string
  links: ReleaseLink[]
  hosts: { running: string[]; pinned: string[] }
  retained: RetainedBy
  cached: boolean
}

export interface StorageOS {
  tracked: boolean
  channel: string
  pin: string
  source: string
  bytes: number
  releases: StorageRelease[]
}

export interface StorageAsset {
  name: string
  bytes: number
  modified: string
  kind: AssetKind
}

export interface StorageEntry {
  name: string
  bytes: number
}

/** GET /storage: what the data directory holds, by release, and why it is kept. */
export interface Storage {
  dataDir: string
  total: number
  free: number
  used: number
  os: Record<HostOS, StorageOS>
  assets: StorageAsset[]
  other: StorageEntry[]
  autopilot: { stateBytes: number; reportsBytes: number }
  computedAt: string
}

export type RawStorage = Partial<
  Omit<Storage, 'total' | 'free' | 'used' | 'os' | 'assets' | 'other' | 'autopilot'>
> & {
  total?: { bytes?: number } | null
  free?: { bytes?: number } | null
  used?: { bytes?: number } | null
  os?: Partial<
    Record<
      HostOS,
      Partial<Omit<StorageOS, 'releases'>> & {
        releases?:
          | (Partial<Omit<StorageRelease, 'hosts' | 'links' | 'retained'>> & {
              links?: (string | null)[] | null
              hosts?: {
                running?: (string | null)[] | null
                pinned?: (string | null)[] | null
              } | null
              retained?: string
            })[]
          | null
      }
    >
  > | null
  assets?: (Partial<Omit<StorageAsset, 'kind'>> & { kind?: string })[] | null
  other?: Partial<StorageEntry>[] | null
  autopilot?: Partial<Storage['autopilot']> | null
}

export function normalizeStorage(raw: RawStorage | null | undefined): Storage {
  const os = {} as Record<HostOS, StorageOS>
  for (const name of OS_OPTIONS) {
    const block = raw?.os?.[name]
    os[name] = {
      tracked: block?.tracked ?? true,
      channel: block?.channel ?? '',
      pin: block?.pin ?? '',
      source: block?.source ?? '',
      bytes: nonNegative(block?.bytes),
      releases: (block?.releases ?? []).map((r) => ({
        version: r.version ?? '',
        bytes: nonNegative(r.bytes),
        files: nonNegative(r.files),
        modified: r.modified ?? '',
        links: strings(r.links).filter((l): l is ReleaseLink =>
          RELEASE_LINKS.includes(l as ReleaseLink)
        ),
        hosts: { running: strings(r.hosts?.running), pinned: strings(r.hosts?.pinned) },
        retained: oneOf(r.retained, RETAINED_BY, '-'),
        cached: r.cached ?? false
      }))
    }
  }
  return {
    dataDir: raw?.dataDir ?? '',
    total: nonNegative(raw?.total?.bytes),
    free: nonNegative(raw?.free?.bytes),
    used: nonNegative(raw?.used?.bytes),
    os,
    assets: (raw?.assets ?? [])
      .filter((a) => typeof a.name === 'string' && a.name !== '')
      .map((a) => ({
        name: a.name ?? '',
        bytes: nonNegative(a.bytes),
        modified: a.modified ?? '',
        kind: oneOf(a.kind, ASSET_KINDS, 'other')
      })),
    other: (raw?.other ?? [])
      .filter((e) => typeof e.name === 'string' && e.name !== '')
      .map((e) => ({ name: e.name ?? '', bytes: nonNegative(e.bytes) })),
    autopilot: {
      stateBytes: nonNegative(raw?.autopilot?.stateBytes),
      reportsBytes: nonNegative(raw?.autopilot?.reportsBytes)
    },
    computedAt: raw?.computedAt ?? ''
  }
}

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB'] as const

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  const digits = unit === 0 ? 0 : value >= 100 ? 0 : value >= 10 ? 1 : 2
  return `${value.toFixed(digits)} ${BYTE_UNITS[unit]}`
}
