import { describe, expect, it } from 'vitest'
import {
  OS_OPTIONS,
  ROLE_OPTIONS,
  acceptsInstallDisk,
  hostRole,
  normalizeBootyData,
  normalizeClusterInfo,
  normalizeEffectiveConfig,
  normalizeHost,
  normalizeSecureBootInfo,
  normalizeTemplateDocument,
  normalizeTemplateValidation,
  registerPayload
} from '@/types'

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
  it('returns kubeadm/external/none, not ready and empty lists for a null payload', () => {
    expect(normalizeClusterInfo(null)).toEqual({
      distribution: 'kubeadm',
      controlPlane: 'external',
      endpoint: '',
      cni: 'none',
      ready: false,
      caFingerprint: '',
      hosts: [],
      warnings: []
    })
  })

  it('keeps known values, falls back on unknown enums and normalises host roles', () => {
    const info = normalizeClusterInfo({
      distribution: 'k0s',
      controlPlane: 'managed',
      endpoint: 'https://10.0.0.1:6443',
      cni: 'cilium',
      ready: true,
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
      endpoint: 'https://10.0.0.1:6443',
      cni: 'cilium',
      ready: true,
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
