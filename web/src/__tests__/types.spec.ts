import { describe, expect, it } from 'vitest'
import { normalizeBootyData, normalizeHost } from '@/types'

describe('normalizeHost', () => {
  it('fills the fleet fields with empty defaults when an old server omits them', () => {
    const host = normalizeHost({ mac: 'aa:bb:cc:dd:ee:01', hostname: 'alpha' })
    expect(host).toMatchObject({
      mac: 'aa:bb:cc:dd:ee:01',
      hostname: 'alpha',
      ip: '',
      booted: '',
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
      running: '',
      lastCheck: '',
      rebootPending: false
    })
  })

  it('returns empty maps for null payloads', () => {
    expect(normalizeBootyData(null)).toEqual({ hosts: {}, unknownHosts: {} })
  })
})
