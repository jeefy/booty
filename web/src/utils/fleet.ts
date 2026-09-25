import type { BootyData, Host, Info } from '@/types'

export const IGNITION_PARTS = ['merged', 'user', 'builtin'] as const
export type IgnitionPart = (typeof IGNITION_PARTS)[number]

export function ignitionPreviewUrl(mac: string, part: IgnitionPart = 'merged'): string {
  return `/ignition.json?mac=${encodeURIComponent(mac)}&preview=1&part=${part}`
}

export interface RunningLabel {
  image: string
  digest: string
  shortDigest: string
  short: string
  full: string
}

const DIGEST_SHORT_LENGTH = 12

export function splitRunning(running: string): RunningLabel | null {
  if (!running) return null
  const at = running.indexOf('@')
  if (at <= 0) {
    return { image: running, digest: '', shortDigest: '', short: running, full: running }
  }
  const image = running.slice(0, at)
  const digest = running.slice(at + 1)
  const hex = digest.includes(':') ? digest.slice(digest.indexOf(':') + 1) : digest
  const shortDigest = hex.slice(0, DIGEST_SHORT_LENGTH)
  return { image, digest, shortDigest, short: `${image}@${shortDigest}`, full: running }
}

export type HostStatus = 'pending' | 'current' | 'unknown'

export function hostStatus(host: Pick<Host, 'running' | 'rebootPending'>): HostStatus {
  if (host.rebootPending) return 'pending'
  if (host.running) return 'current'
  return 'unknown'
}

export const HOST_STATUS_LABEL: Record<HostStatus, { text: string; badge: string }> = {
  pending: { text: 'Reboot pending', badge: 'text-bg-warning' },
  current: { text: 'Up to date', badge: 'text-bg-success' },
  unknown: { text: 'Unknown', badge: 'text-bg-secondary' }
}

export function pendingHosts(data: BootyData): Host[] {
  return Object.values(data.hosts)
    .filter((host) => host.rebootPending)
    .sort((a, b) => (a.hostname || a.mac).localeCompare(b.hostname || b.mac))
}

export function targetVersion(host: Host, info: Info): string {
  switch (host.os) {
    case 'coreos':
      return info.coreos?.version || ''
    case 'ublue':
      return host.ostreeImage || ''
    default:
      return info.flatcar?.pinnedVersion || info.flatcar?.version || ''
  }
}

export interface FleetSummary {
  hosts: number
  pendingReboots: number
  fromServer: boolean
}

export function fleetSummary(data: BootyData, info: Info): FleetSummary {
  const derived = {
    hosts: Object.keys(data.hosts).length,
    pendingReboots: pendingHosts(data).length
  }
  const fleet = info.fleet
  if (!fleet) return { ...derived, fromServer: false }
  return {
    hosts: fleet.hosts ?? derived.hosts,
    pendingReboots: fleet.pendingReboots ?? derived.pendingReboots,
    fromServer: true
  }
}
