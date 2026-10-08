import { describe, expect, it } from 'vitest'
import {
  formatAbsolute,
  formatDuration,
  formatElapsed,
  formatRelative,
  formatSpan,
  formatTime,
  formatUntil,
  parseTime
} from '@/utils/time'

const now = new Date('2026-09-24T12:00:00Z')

describe('parseTime', () => {
  it('returns null for empty, invalid and zero-value timestamps', () => {
    expect(parseTime('')).toBeNull()
    expect(parseTime(undefined)).toBeNull()
    expect(parseTime('not a date')).toBeNull()
    expect(parseTime('0001-01-01T00:00:00Z')).toBeNull()
  })

  it('parses RFC3339', () => {
    expect(parseTime('2026-09-24T11:59:00Z')?.toISOString()).toBe('2026-09-24T11:59:00.000Z')
  })
})

describe('formatRelative', () => {
  it('says never for an unset booted timestamp', () => {
    expect(formatRelative('', now)).toBe('never')
  })

  it('collapses very recent times to "just now"', () => {
    expect(formatRelative('2026-09-24T11:59:30Z', now)).toBe('just now')
    expect(formatRelative('2026-09-24T12:00:05Z', now)).toBe('just now')
  })

  it.each([
    ['2026-09-24T11:59:00Z', '1m ago'],
    ['2026-09-24T09:00:00Z', '3h ago'],
    ['2026-09-22T12:00:00Z', '2d ago'],
    ['2026-07-01T12:00:00Z', '2mo ago'],
    ['2024-09-24T12:00:00Z', '2y ago']
  ])('formats %s as %s', (value, expected) => {
    expect(formatRelative(value, now)).toBe(expected)
  })

  it('is what formatTime delegates to', () => {
    expect(formatTime('2026-09-24T11:00:00Z', now)).toBe('1h ago')
  })
})

describe('formatAbsolute', () => {
  it('returns empty string for unset values and a locale string otherwise', () => {
    expect(formatAbsolute('')).toBe('')
    expect(formatAbsolute('2026-09-24T11:00:00Z')).not.toBe('')
  })
})

describe('formatSpan, formatElapsed and formatUntil', () => {
  it.each([
    [30, '<1m'],
    [90, '1m'],
    [13 * 3600, '13h'],
    [28 * 3600, '1d'],
    [40 * 24 * 3600, '1mo']
  ])('formats %d seconds as %s', (seconds, expected) => {
    expect(formatSpan(seconds)).toBe(expected)
  })

  it('measures elapsed time without the "ago" suffix', () => {
    expect(formatElapsed('2026-09-23T23:00:00Z', now)).toBe('13h')
    expect(formatElapsed('', now)).toBe('')
  })

  it('measures time until a future instant and is empty once it passed', () => {
    expect(formatUntil('2026-09-25T16:00:00Z', now)).toBe('1d')
    expect(formatUntil('2026-09-24T14:00:00Z', now)).toBe('2h')
    expect(formatUntil('2026-09-24T11:00:00Z', now)).toBe('')
    expect(formatUntil('', now)).toBe('')
  })
})

describe('formatDuration', () => {
  it.each([
    ['24h0m0s', '24h'],
    ['48h0m0s', '48h'],
    ['15m0s', '15m'],
    ['1h30m0s', '1h30m'],
    ['1m30s', '1m30s'],
    ['0s', '0s'],
    ['', '']
  ])('tidies %s to %s', (value, expected) => {
    expect(formatDuration(value)).toBe(expected)
  })
})
