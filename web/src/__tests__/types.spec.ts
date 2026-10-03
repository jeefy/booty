import { describe, expect, it } from 'vitest'
import {
  OS_OPTIONS,
  ROLE_OPTIONS,
  STALE_AFTER_MS,
  acceptsInstallDisk,
  fleetTargets,
  formatBytes,
  hostOS,
  hostReporting,
  hostRole,
  hostTarget,
  normalizeAutopilotStatus,
  normalizeBootyData,
  normalizeClusterInfo,
  normalizeEffectiveConfig,
  normalizeHost,
  normalizeHostPower,
  normalizePowerStatus,
  normalizeSecureBootInfo,
  normalizeStorage,
  normalizeTemplateDocument,
  normalizeTemplateValidation,
  registerPayload,
  versionMatch
} from '@/types'

const NOW = new Date('2026-10-01T12:00:00Z')

function minutesAgo(minutes: number): string {
  return new Date(NOW.getTime() - minutes * 60_000).toISOString()
}

describe('hostReporting', () => {
  it('is never when lastCheck, booted and health are all empty', () => {
    expect(hostReporting({ lastCheck: '', booted: '', health: null }, NOW)).toBe('never')
    expect(hostReporting({ lastCheck: '', booted: '', health: undefined }, NOW)).toBe('never')
  })

  it('is reporting once anything was recorded and the last check is fresh', () => {
    expect(hostReporting({ lastCheck: minutesAgo(5), booted: '', health: null }, NOW)).toBe(
      'reporting'
    )
    expect(hostReporting({ lastCheck: '', booted: minutesAgo(2), health: null }, NOW)).toBe(
      'reporting'
    )
    expect(
      hostReporting({ lastCheck: '', booted: '', health: { receivedAt: minutesAgo(1) } }, NOW)
    ).toBe('reporting')
  })

  it('is stale when the last check is older than 30 minutes', () => {
    expect(hostReporting({ lastCheck: minutesAgo(31), booted: '', health: null }, NOW)).toBe(
      'stale'
    )
    expect(hostReporting({ lastCheck: minutesAgo(30), booted: '', health: null }, NOW)).toBe(
      'reporting'
    )
    expect(STALE_AFTER_MS).toBe(30 * 60 * 1000)
  })

  it('treats an unparseable lastCheck as reporting rather than stale', () => {
    expect(hostReporting({ lastCheck: 'garbage', booted: '', health: null }, NOW)).toBe('reporting')
  })
})

describe('hostOS', () => {
  it('defaults "" and unknown values to flatcar', () => {
    expect(hostOS('')).toBe('flatcar')
    expect(hostOS(undefined)).toBe('flatcar')
    expect(hostOS('bluefin')).toBe('bluefin')
    expect(hostOS('coreos')).toBe('coreos')
  })
})

describe('versionMatch / hostTarget', () => {
  const targets = { flatcar: '4593.2.1', coreos: '44.1', bluefin: '26.10.816' }

  it('matches running against the fleet target of the host OS', () => {
    expect(versionMatch({ os: 'flatcar', running: '4593.2.1' }, targets)).toBe('match')
    expect(versionMatch({ os: '', running: '4593.2.1' }, targets)).toBe('match')
    expect(versionMatch({ os: 'bluefin', running: '26.10.816' }, targets)).toBe('match')
    expect(versionMatch({ os: 'bluefin', running: '26.09.747' }, targets)).toBe('behind')
  })

  it('prefers the host targetVersion over the fleet target', () => {
    const host = { os: 'flatcar' as const, running: '4593.2.1', targetVersion: '4500.0.0' }
    expect(hostTarget(host, targets)).toBe('4500.0.0')
    expect(versionMatch(host, targets)).toBe('behind')
    expect(versionMatch({ ...host, running: '4500.0.0' }, targets)).toBe('match')
  })

  it('is unknown without a running version, without a target, or for ostree images', () => {
    expect(versionMatch({ os: 'flatcar', running: '' }, targets)).toBe('unknown')
    expect(versionMatch({ os: 'coreos', running: '44.1' }, {})).toBe('unknown')
    expect(
      versionMatch(
        { os: 'coreos', running: 'ghcr.io/x/y@sha256:abc', ostreeImage: 'ghcr.io/x/y:stable' },
        targets
      )
    ).toBe('unknown')
    expect(versionMatch({ os: 'coreos', running: 'ghcr.io/x/y@sha256:abc' }, targets)).toBe(
      'unknown'
    )
  })
})

describe('fleetTargets', () => {
  const info = {
    flatcar: { version: '4593.2.1', pinnedVersion: '' },
    coreos: { version: '44.1' },
    bluefin: { version: '26.10.816', pinnedVersion: '' }
  }

  it('uses /info.targets when present and fills gaps from the version blocks', () => {
    expect(
      fleetTargets({ ...info, targets: { flatcar: '4500.0.0', coreos: '', bluefin: null } })
    ).toEqual({ flatcar: '4500.0.0', coreos: '44.1', bluefin: '26.10.816' })
  })

  it('falls back to the version blocks for older servers, preferring a Flatcar pin', () => {
    expect(fleetTargets(info)).toEqual({ flatcar: '4593.2.1', coreos: '44.1', bluefin: '26.10.816' })
    expect(fleetTargets({ ...info, flatcar: { version: '4593.2.1', pinnedVersion: '4500.0.0' } }).flatcar).toBe(
      '4500.0.0'
    )
    expect(fleetTargets({ bluefin: { version: '0.0.0' } })).toEqual({ flatcar: '', coreos: '', bluefin: '' })
    expect(fleetTargets({})).toEqual({ flatcar: '', coreos: '', bluefin: '' })
  })
})

describe('normalizeHost', () => {
  it('fills the fleet fields with empty defaults when an old server omits them', () => {
    const host = normalizeHost({ mac: 'aa:bb:cc:dd:ee:01', hostname: 'alpha' })
    expect(host).toMatchObject({
      mac: 'aa:bb:cc:dd:ee:01',
      hostname: 'alpha',
      ip: '',
      booted: '',
      installDisk: '',
      running: '',
      lastCheck: '',
      rebootPending: false
    })
  })

  it('preserves populated fleet fields', () => {
    const host = normalizeHost({
      mac: 'aa:bb:cc:dd:ee:01',
      running: '3815.2.0',
      lastCheck: '2026-09-24T11:30:00Z',
      rebootPending: true
    })
    expect(host.running).toBe('3815.2.0')
    expect(host.lastCheck).toBe('2026-09-24T11:30:00Z')
    expect(host.rebootPending).toBe(true)
  })

  it('normalises a missing installDisk to "" and keeps a set one', () => {
    expect(normalizeHost({ mac: 'aa:bb:cc:dd:ee:01' }).installDisk).toBe('')
    expect(normalizeHost({ mac: 'aa:bb:cc:dd:ee:01', installDisk: '/dev/sda' }).installDisk).toBe(
      '/dev/sda'
    )
  })

  it('passes role through as-is so /register payloads stay byte-for-byte compatible', () => {
    expect(normalizeHost({ mac: 'aa:bb:cc:dd:ee:01' })).not.toHaveProperty('role')
    expect(normalizeHost({ mac: 'aa:bb:cc:dd:ee:01', role: '' }).role).toBe('')
    expect(normalizeHost({ mac: 'aa:bb:cc:dd:ee:01', role: 'control-plane' }).role).toBe(
      'control-plane'
    )
  })
})

describe('hostRole', () => {
  it('treats "", undefined and "worker" as worker and keeps control-plane', () => {
    expect(hostRole(undefined)).toBe('worker')
    expect(hostRole('')).toBe('worker')
    expect(hostRole('worker')).toBe('worker')
    expect(hostRole('control-plane')).toBe('control-plane')
  })
})

describe('ROLE_OPTIONS', () => {
  it('lists worker first so it is the select default', () => {
    expect([...ROLE_OPTIONS]).toEqual(['worker', 'control-plane'])
  })
})

describe('acceptsInstallDisk', () => {
  it('is true for bluefin and coreos only', () => {
    expect(acceptsInstallDisk('bluefin')).toBe(true)
    expect(acceptsInstallDisk('coreos')).toBe(true)
    expect(acceptsInstallDisk('flatcar')).toBe(false)
    expect(acceptsInstallDisk('')).toBe(false)
    expect(acceptsInstallDisk(undefined)).toBe(false)
  })
})

describe('OS_OPTIONS', () => {
  it('lists flatcar, coreos and bluefin', () => {
    expect([...OS_OPTIONS]).toEqual(['flatcar', 'coreos', 'bluefin'])
  })
})

describe('normalizeBootyData', () => {
  it('normalises every host and falls back to the map key for a missing mac', () => {
    const data = normalizeBootyData({ hosts: { 'aa:bb:cc:dd:ee:02': {} } })
    expect(data.unknownHosts).toEqual({})
    expect(data.hosts['aa:bb:cc:dd:ee:02']).toEqual({
      mac: 'aa:bb:cc:dd:ee:02',
      hostname: '',
      ip: '',
      booted: '',
      installDisk: '',
      running: '',
      lastCheck: '',
      rebootPending: false
    })
  })

  it('returns empty maps for null payloads', () => {
    expect(normalizeBootyData(null)).toEqual({ hosts: {}, unknownHosts: {} })
  })
})

describe('normalizeEffectiveConfig', () => {
  it('fills defaults for a sparse payload and drops host templates without a name', () => {
    const cfg = normalizeEffectiveConfig({
      settings: [{ key: 'serverIP' }, { key: 'githubToken', redacted: true, source: 'env' }],
      templates: {
        default: { name: 'config/ignition.yaml', writable: true },
        hosts: [
          { mac: 'aa:bb:cc:dd:ee:01', hostname: 'alpha', name: 'config/alpha.yaml' },
          { mac: 'x' }
        ]
      },
      readOnlyReason: ''
    })
    expect(cfg.settings).toEqual([
      { key: 'serverIP', value: '', source: 'default', redacted: false },
      { key: 'githubToken', value: '', source: 'env', redacted: true }
    ])
    expect(cfg.templates.default).toEqual({
      name: 'config/ignition.yaml',
      source: 'file',
      writable: true
    })
    expect(cfg.templates.hosts).toEqual([
      { mac: 'aa:bb:cc:dd:ee:01', hostname: 'alpha', name: 'config/alpha.yaml' }
    ])
    expect(cfg.readOnlyReason).toBeUndefined()
  })

  it('returns an empty, read-only default for null payloads', () => {
    expect(normalizeEffectiveConfig(null)).toEqual({
      settings: [],
      templates: { default: { name: '', source: 'file', writable: false }, hosts: [] }
    })
  })
})

describe('normalizeTemplateDocument', () => {
  it('keeps content and the read-only reason', () => {
    expect(
      normalizeTemplateDocument({
        name: 'config/ignition.yaml',
        source: 'embedded',
        writable: false,
        content: 'variant: flatcar\n',
        readOnlyReason: 'mounted read-only'
      })
    ).toEqual({
      name: 'config/ignition.yaml',
      source: 'embedded',
      writable: false,
      content: 'variant: flatcar\n',
      readOnlyReason: 'mounted read-only'
    })
    expect(normalizeTemplateDocument(undefined).content).toBe('')
  })
})

describe('normalizeTemplateValidation', () => {
  it('treats unknown entry kinds as errors and defaults ok to false', () => {
    const result = normalizeTemplateValidation({
      entries: [{ kind: 'warning', message: 'w' }, { message: 'e' }]
    })
    expect(result.ok).toBe(false)
    expect(result.ignition).toBe('')
    expect(result.entries).toEqual([
      { kind: 'warning', message: 'w' },
      { kind: 'error', message: 'e' }
    ])
  })
})

describe('normalizeClusterInfo', () => {
  it('returns kubeadm/external/none, not ready, not connected and empty lists for a null payload', () => {
    expect(normalizeClusterInfo(null)).toEqual({
      distribution: 'kubeadm',
      controlPlane: 'external',
      source: 'external',
      endpoint: '',
      apiServer: '',
      cni: 'none',
      ready: false,
      connected: false,
      nodes: 0,
      caFingerprint: '',
      hosts: [],
      warnings: []
    })
  })

  it('keeps known values, falls back on unknown enums and normalises host roles', () => {
    const info = normalizeClusterInfo({
      distribution: 'k0s',
      controlPlane: 'managed',
      source: 'managed',
      endpoint: 'https://10.0.0.1:6443',
      apiServer: 'https://10.0.0.1:6443',
      cni: 'cilium',
      ready: true,
      connected: true,
      nodes: 3,
      caFingerprint: 'sha256:abc',
      hosts: [
        {
          mac: 'aa:bb:cc:dd:ee:01',
          hostname: 'cp',
          os: 'flatcar',
          role: 'control-plane',
          secureBoot: true
        },
        { mac: 'aa:bb:cc:dd:ee:02', hostname: 'w1', role: '' },
        { mac: 'aa:bb:cc:dd:ee:03' }
      ],
      warnings: ['CA key readable on the boot VLAN', '', null]
    })
    expect(info).toMatchObject({
      distribution: 'k0s',
      controlPlane: 'managed',
      source: 'managed',
      endpoint: 'https://10.0.0.1:6443',
      apiServer: 'https://10.0.0.1:6443',
      cni: 'cilium',
      ready: true,
      connected: true,
      nodes: 3,
      caFingerprint: 'sha256:abc'
    })
    expect(info.hosts).toEqual([
      {
        mac: 'aa:bb:cc:dd:ee:01',
        hostname: 'cp',
        os: 'flatcar',
        role: 'control-plane',
        booted: '',
        secureBoot: true
      },
      {
        mac: 'aa:bb:cc:dd:ee:02',
        hostname: 'w1',
        os: '',
        role: 'worker',
        booted: '',
        secureBoot: false
      },
      {
        mac: 'aa:bb:cc:dd:ee:03',
        hostname: '',
        os: '',
        role: 'worker',
        booted: '',
        secureBoot: false
      }
    ])
    expect(info.warnings).toEqual(['CA key readable on the boot VLAN'])

    const unknown = normalizeClusterInfo({
      distribution: 'rke2' as never,
      controlPlane: 'hosted' as never,
      cni: 'weave' as never
    })
    expect(unknown.distribution).toBe('kubeadm')
    expect(unknown.controlPlane).toBe('external')
    expect(unknown.cni).toBe('none')
  })

  it('derives source from controlPlane and defaults the connection fields on an older server', () => {
    const legacy = normalizeClusterInfo({ controlPlane: 'managed', ready: true })
    expect(legacy).toMatchObject({
      controlPlane: 'managed',
      source: 'managed',
      ready: true,
      connected: false,
      nodes: 0,
      apiServer: ''
    })
    expect(
      normalizeClusterInfo({ controlPlane: 'external', source: 'bogus' as never }).source
    ).toBe('external')
    expect(normalizeClusterInfo({ nodes: -2 }).nodes).toBe(0)
    expect(normalizeClusterInfo({ nodes: Number.NaN }).nodes).toBe(0)
    expect(normalizeClusterInfo({ nodes: 6.9 }).nodes).toBe(6)
  })
})

describe('normalizeSecureBootInfo', () => {
  it('is off with empty lists and no CA for a null or sparse payload', () => {
    expect(normalizeSecureBootInfo(null)).toEqual({
      enabled: false,
      ready: false,
      bundleVersion: '',
      trusted: [],
      bootURL: '',
      flatcarCA: null,
      warnings: []
    })
    expect(normalizeSecureBootInfo({ enabled: true, flatcarCA: {} })).toMatchObject({
      enabled: true,
      flatcarCA: null
    })
  })

  it('keeps the CA when it has a fingerprint and drops empty warnings and trust entries', () => {
    const info = normalizeSecureBootInfo({
      enabled: true,
      ready: true,
      bundleVersion: 'ipxe-16.1_v2.0.0_shim-16.1-7_grub-2.12-64.fc44',
      trusted: ['microsoft', 'flatcar', '', null],
      bootURL: 'http://10.0.0.1/boot/sb',
      flatcarCA: {
        sha256: 'ebb170da',
        flatcarVersion: '4757.2.0',
        url: 'http://10.0.0.1/boot/secureboot/flatcar-ca.der'
      },
      warnings: ['host aa (flatcar): Secure Boot host; boot refused', '', null]
    })
    expect(info.trusted).toEqual(['microsoft', 'flatcar'])
    expect(info.flatcarCA).toEqual({
      sha256: 'ebb170da',
      flatcarVersion: '4757.2.0',
      subject: '',
      notAfter: '',
      url: 'http://10.0.0.1/boot/secureboot/flatcar-ca.der'
    })
    expect(info.warnings).toEqual(['host aa (flatcar): Secure Boot host; boot refused'])
  })
})

describe('normalizeHost secureBoot', () => {
  it('passes the server flag through and leaves it absent for older servers', () => {
    expect(normalizeHost({ mac: 'aa:bb:cc:dd:ee:01' })).not.toHaveProperty('secureBoot')
    expect(normalizeHost({ mac: 'aa:bb:cc:dd:ee:01', secureBoot: true }).secureBoot).toBe(true)
  })
})

describe('registerPayload', () => {
  const base = {
    mac: 'aa:bb:cc:dd:ee:01',
    hostname: 'alpha',
    ip: '',
    booted: '',
    installDisk: '',
    running: '',
    lastCheck: '',
    rebootPending: false,
    stateDisk: '/dev/sdb',
    extensions: ['zfs' as const],
    mode: 'installed' as const
  }

  it('keeps the bluefin fields for bluefin hosts', () => {
    const host = { ...base, os: 'bluefin' as const }
    expect(registerPayload(host)).toBe(host)
  })

  it('drops them for other operating systems, which the server refuses them for', () => {
    for (const os of ['', 'flatcar', 'coreos'] as const) {
      const host = { ...base, os }
      const payload = registerPayload(host)
      expect(payload).not.toHaveProperty('stateDisk')
      expect(payload).not.toHaveProperty('extensions')
      expect(payload).not.toHaveProperty('mode')
      expect(payload.hostname).toBe('alpha')
      expect(host.stateDisk).toBe('/dev/sdb')
    }
  })
})

describe('normalizeHostPower / normalizePowerStatus', () => {
  it('defaults an absent or unknown block to unknown', () => {
    expect(normalizeHostPower(undefined)).toMatchObject({
      state: 'unknown',
      request: '',
      cordoned: false
    })
    expect(normalizeHostPower({ state: 'nonsense', request: 'bogus' })).toMatchObject({
      state: 'unknown',
      request: ''
    })
  })

  it('keeps every known field', () => {
    const p = normalizeHostPower({
      state: 'rebooting',
      since: '2026-09-30T10:00:00Z',
      reason: 'kernel update',
      request: 'reboot',
      requestedBy: '192.168.1.20',
      requestedAt: '2026-09-30T09:59:00Z',
      lastSeen: '2026-09-30T09:58:00Z',
      cordoned: true,
      probe: { ok: true, at: '2026-09-30T09:58:30Z', method: 'tcp/22' }
    })
    expect(p.state).toBe('rebooting')
    expect(p.request).toBe('reboot')
    expect(p.cordoned).toBe(true)
    expect(p.probe).toEqual({ ok: true, at: '2026-09-30T09:58:30Z', method: 'tcp/22' })
  })

  it('normalizes GET /power with defaults for an older server', () => {
    const st = normalizePowerStatus({
      hosts: { a: { state: 'up' }, b: null },
      events: [{ text: 'x' }]
    })
    expect(st.hosts.a!.state).toBe('up')
    expect(st.hosts.b!.state).toBe('unknown')
    expect(st.events[0]).toEqual({ at: '', kind: 'power', mac: '', text: 'x' })
    expect(st.capabilities).toEqual({ wol: false, actuator: 'none' })
    expect(st.summary).toEqual({ up: 0, off: 0, unreachable: 0, inFlight: 0 })
  })
})

describe('normalizeStorage', () => {
  it('fills every OS block and defaults missing values', () => {
    const s = normalizeStorage(null)
    expect(s.dataDir).toBe('')
    expect(s.total).toBe(0)
    expect(s.free).toBe(0)
    for (const os of OS_OPTIONS) {
      expect(s.os[os]).toEqual({
        tracked: true,
        channel: '',
        pin: '',
        source: '',
        bytes: 0,
        releases: []
      })
    }
    expect(s.assets).toEqual([])
    expect(s.other).toEqual([])
    expect(s.autopilot).toEqual({ stateBytes: 0, reportsBytes: 0 })
  })

  it('maps the wire shape and drops junk', () => {
    const s = normalizeStorage({
      dataDir: '/data',
      total: { bytes: 1000 },
      free: { bytes: 400 },
      used: { bytes: -5 },
      computedAt: '2026-10-03T12:00:00Z',
      os: {
        bluefin: {
          tracked: false,
          channel: 'none',
          source: 'ghcr.io/x/y',
          bytes: 300,
          releases: [
            {
              version: '26.09.678',
              bytes: 300,
              files: 9,
              modified: '2026-09-28T23:36:00Z',
              links: ['current', 'bogus', null],
              hosts: { running: ['aa', null], pinned: null },
              retained: 'current',
              cached: true
            },
            { version: '1.0', retained: 'nope' }
          ]
        }
      },
      assets: [
        { name: 'ipxe.efi', bytes: 10, kind: 'ipxe' },
        { name: 'x.efi', kind: 'weird' },
        { bytes: 3 }
      ],
      other: [{ name: 'config', bytes: 4 }, { bytes: 9 }],
      autopilot: { stateBytes: 1 }
    })
    expect(s.total).toBe(1000)
    expect(s.used).toBe(0)
    expect(s.os.bluefin.tracked).toBe(false)
    expect(s.os.bluefin.releases[0]).toEqual({
      version: '26.09.678',
      bytes: 300,
      files: 9,
      modified: '2026-09-28T23:36:00Z',
      links: ['current'],
      hosts: { running: ['aa'], pinned: [] },
      retained: 'current',
      cached: true
    })
    expect(s.os.bluefin.releases[1]).toEqual({
      version: '1.0',
      bytes: 0,
      files: 0,
      modified: '',
      links: [],
      hosts: { running: [], pinned: [] },
      retained: '-',
      cached: false
    })
    expect(s.os.flatcar.releases).toEqual([])
    expect(s.assets).toEqual([
      { name: 'ipxe.efi', bytes: 10, modified: '', kind: 'ipxe' },
      { name: 'x.efi', bytes: 0, modified: '', kind: 'other' }
    ])
    expect(s.other).toEqual([{ name: 'config', bytes: 4 }])
    expect(s.autopilot).toEqual({ stateBytes: 1, reportsBytes: 0 })
  })
})

describe('formatBytes', () => {
  it('picks binary units with sensible precision', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(-1)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1536)).toBe('1.50 KiB')
    expect(formatBytes(15 * 1024)).toBe('15.0 KiB')
    expect(formatBytes(150 * 1024)).toBe('150 KiB')
    expect(formatBytes(6 * 1024 ** 3)).toBe('6.00 GiB')
    expect(formatBytes(2 * 1024 ** 4)).toBe('2.00 TiB')
  })
})

describe('normalizeAutopilotStatus cached', () => {
  it('defaults cached to true for older servers and keeps false', () => {
    const st = normalizeAutopilotStatus({
      os: {
        flatcar: {
          releases: [
            { version: '1', state: 'good' },
            { version: '2', state: 'good', cached: false }
          ]
        }
      }
    })
    expect(st.os.flatcar.releases.map((r) => r.cached)).toEqual([true, false])
  })
})
