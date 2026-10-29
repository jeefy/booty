/** Helpers shared by the wire-type normalisers; not part of the public `@/types` surface. */

export interface StatusResponse {
  status: string
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function nonNegative(value: number | null | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.trunc(value) : 0
}

export function strings(values: (string | null)[] | null | undefined): string[] {
  return (values ?? []).filter((v): v is string => typeof v === 'string' && v !== '')
}

export function oneOf<T extends string>(value: string | undefined, options: readonly T[], fallback: T): T {
  return options.includes(value as T) ? (value as T) : fallback
}
