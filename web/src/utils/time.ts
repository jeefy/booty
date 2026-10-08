const UNITS: Array<[label: string, seconds: number]> = [
  ['y', 365 * 24 * 3600],
  ['mo', 30 * 24 * 3600],
  ['d', 24 * 3600],
  ['h', 3600],
  ['m', 60]
]

export function parseTime(value: string | null | undefined): Date | null {
  if (!value) return null
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return null
  if (date.getTime() <= 0) return null
  return date
}

export function formatRelative(value: string | null | undefined, now: Date = new Date()): string {
  const date = parseTime(value)
  if (!date) return 'never'
  const diff = Math.round((now.getTime() - date.getTime()) / 1000)
  if (diff < 0) return 'just now'
  if (diff < 45) return 'just now'
  for (const [label, seconds] of UNITS) {
    if (diff >= seconds) {
      return `${Math.floor(diff / seconds)}${label} ago`
    }
  }
  return `${diff}s ago`
}

export function formatAbsolute(value: string | null | undefined): string {
  const date = parseTime(value)
  if (!date) return ''
  return date.toLocaleString()
}

export function formatTime(value: string | null | undefined, now?: Date): string {
  return formatRelative(value, now)
}

export function formatSpan(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 60) return '<1m'
  for (const [label, unit] of UNITS) {
    if (seconds >= unit) return `${Math.floor(seconds / unit)}${label}`
  }
  return '<1m'
}

export function formatElapsed(value: string | null | undefined, now: Date = new Date()): string {
  const date = parseTime(value)
  if (!date) return ''
  return formatSpan((now.getTime() - date.getTime()) / 1000)
}

export function formatUntil(value: string | null | undefined, now: Date = new Date()): string {
  const date = parseTime(value)
  if (!date || date.getTime() <= now.getTime()) return ''
  return formatSpan((date.getTime() - now.getTime()) / 1000)
}

/** Drops the zero tail Go's Duration.String() prints: `24h0m0s` → `24h`, `15m0s` → `15m`. */
export function formatDuration(value: string | null | undefined): string {
  if (!value) return ''
  return value.replace(/h0m0s$/, 'h').replace(/m0s$/, 'm')
}
