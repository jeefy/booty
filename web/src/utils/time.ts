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
