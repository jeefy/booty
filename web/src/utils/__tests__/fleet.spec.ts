import { describe, expect, it } from 'vitest'
import type { BootyData, Host } from '@/types'
import {
  fleetSummary,
  hostStatus,
  ignitionPreviewUrl,
  pendingHosts,
  splitRunning,
  targetVersion
} from '@/utils/fleet'

const DIGEST = 'sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'

function host(overrides: Partial<Host>): Host {
  return {
    mac: 'aa:bb:cc:dd:ee:00',
    hostname: '',
    ip: '',
    booted: '',
    running: '',
    lastCheck: '',
    rebootPending: false,
    ...overrides
  }
}

describe('ignitionPreviewUrl', () => {
  it('defaults to the merged part and encodes the MAC', () => {
    expect(ignitionPreviewUrl('aa:bb:cc:dd:ee:01')).toBe(
      '/ignition.json?mac=aa%3Abb%3Acc%3Add%3Aee%3A01&preview=1&part=merged'
    )
    expect(ignitionPreviewUrl('aa:bb:cc:dd:ee:01', 'user')).toContain('&part=user')
    expect(ignitionPreviewUrl('aa:bb:cc:dd:ee:01', 'builtin')).toContain('&part=builtin')
  })
})

describe('splitRunning', () => {
  it('returns null for an empty value', () => {
    expect(splitRunning('')).toBeNull()
  })

  it('passes plain versions through unchanged', () => {
    expect(splitRunning('3815.2.0')).toEqual({
      image: '3815.2.0',
      digest: '',
      shortDigest: '',
      short: '3815.2.0',
      full: '3815.2.0'
    })
  })

  it('splits image@digest and shortens the digest to 12 hex chars', () => {
    const label = splitRunning(`ghcr.io/ublue-os/bazzite@${DIGEST}`)
    expect(label).toEqual({
      image: 'ghcr.io/ublue-os/bazzite',
      digest: DIGEST,
      shortDigest: '0123456789ab',
      short: 'ghcr.io/ublue-os/bazzite@0123456789ab',
      full: `ghcr.io/ublue-os/bazzite@${DIGEST}`
    })
  })
})

describe('hostStatus', () => {
  it('maps the three fleet states', () => {
    expect(hostStatus({ running: '1', rebootPending: true })).toBe('pending')
    expect(hostStatus({ running: '', rebootPending: true })).toBe('pending')
    expect(hostStatus({ running: '1', rebootPending: false })).toBe('current')
    expect(hostStatus({ running: '', rebootPending: false })).toBe('unknown')
  })
})

describe('targetVersion', () => {
  const info = {
    flatcar: { version: '3815.2.0', pinnedVersion: '' },
    coreos: { version: '40.1' }
  }

  it('picks the per-OS target and prefers a Flatcar pin', () => {
    expect(targetVersion(host({ os: 'flatcar' }), info)).toBe('3815.2.0')
    expect(targetVersion(host({ os: '' }), info)).toBe('3815.2.0')
    expect(targetVersion(host({ os: 'coreos' }), info)).toBe('40.1')
    expect(targetVersion(host({ os: 'ublue', ostreeImage: 'img:tag' }), info)).toBe('img:tag')
    expect(
      targetVersion(host({ os: 'flatcar' }), { flatcar: { version: '3815.2.0', pinnedVersion: '3760.2.0' } })
    ).toBe('3760.2.0')
  })
})

describe('pendingHosts / fleetSummary', () => {
  const data: BootyData = {
    hosts: {
      b: host({ mac: 'b', hostname: 'bravo', rebootPending: true }),
      a: host({ mac: 'a', hostname: 'alpha', rebootPending: true }),
      c: host({ mac: 'c', hostname: 'charlie', running: '1' })
    },
    unknownHosts: {}
  }

  it('lists pending hosts sorted by hostname', () => {
    expect(pendingHosts(data).map((h) => h.hostname)).toEqual(['alpha', 'bravo'])
  })

  it('derives counts when /info has no fleet block and prefers the server values otherwise', () => {
    expect(fleetSummary(data, {})).toEqual({ hosts: 3, pendingReboots: 2, fromServer: false })
    expect(fleetSummary(data, { fleet: { hosts: 10, pendingReboots: 4 } })).toEqual({
      hosts: 10,
      pendingReboots: 4,
      fromServer: true
    })
    expect(fleetSummary(data, { fleet: {} })).toEqual({ hosts: 3, pendingReboots: 2, fromServer: true })
  })
})
