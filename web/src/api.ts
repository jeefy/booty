import {
  normalizeAutopilotStatus,
  normalizeBootyData,
  normalizeClusterInfo,
  normalizeEffectiveConfig,
  normalizeInfo,
  normalizePowerStatus,
  normalizeStorage,
  type AutopilotStatus,
  type BootyData,
  type ClusterInfo,
  type EffectiveConfig,
  type Info,
  type PowerStatus,
  type RawAutopilotStatus,
  type RawBootyData,
  type RawClusterInfo,
  type RawEffectiveConfig,
  type RawInfo,
  type RawPowerStatus,
  type RawStorage,
  type Storage
} from '@/types'

export class ApiError extends Error {
  readonly status: number
  readonly details: Record<string, unknown>

  constructor(status: number, message: string, details: Record<string, unknown> = {}) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.details = details
  }
}

async function errorFromResponse(response: Response): Promise<ApiError> {
  const fallback = `HTTP ${response.status}`
  try {
    const body: unknown = await response.json()
    if (body && typeof body === 'object' && 'error' in body) {
      const { error: message, ...details } = body as { error: unknown } & Record<string, unknown>
      if (typeof message === 'string' && message.length > 0) {
        return new ApiError(response.status, message, details)
      }
    }
  } catch {
    // body was not JSON; fall through to the status-code message
  }
  return new ApiError(response.status, fallback)
}

async function fetchJSON(
  path: string,
  init?: RequestInit
): Promise<{ body: unknown; status: number }> {
  const response = await fetch(path, init)
  if (!response.ok) {
    throw await errorFromResponse(response)
  }
  try {
    return { body: await response.json(), status: response.status }
  } catch {
    throw new ApiError(response.status, `Unexpected response from ${path}: not JSON`)
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  // Callers of the untyped helpers take responsibility for the shape; the
  // endpoint helpers below run the body through a normaliser instead.
  return (await fetchJSON(path, init)).body as T
}

function isJSONObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/**
 * GETs path and hands the body to normalize. A body that is not a JSON
 * object, or that the normaliser cannot digest, becomes an ApiError so the
 * view's error alert shows it instead of the page rendering garbage.
 */
export async function apiGetNormalized<T>(
  path: string,
  normalize: (raw: Record<string, unknown>) => T
): Promise<T> {
  const { body, status } = await fetchJSON(path)
  if (!isJSONObject(body)) {
    const kind = body === null ? 'null' : Array.isArray(body) ? 'an array' : typeof body
    throw new ApiError(status, `Unexpected response from ${path}: expected an object, got ${kind}`)
  }
  try {
    return normalize(body)
  } catch (err) {
    const reason = err instanceof Error ? err.message : String(err)
    throw new ApiError(status, `Unexpected response from ${path}: ${reason}`)
  }
}

// The casts below only widen an already-checked JSON object to the
// all-optional Raw* wire type; the normaliser defends every field.

/** GET /booty.json: registered and unknown hosts. */
export function getBootyData(): Promise<BootyData> {
  return apiGetNormalized('/booty.json', (raw) => normalizeBootyData(raw as RawBootyData))
}

/** GET /info: versions, fleet, targets, Secure Boot, autopilot and power summaries. */
export function getInfo(): Promise<Info> {
  return apiGetNormalized('/info', (raw) => normalizeInfo(raw as RawInfo))
}

/** Query variants of GET /autopilot the UI uses. */
export type AutopilotQuery = '' | '?events=all' | '?releases=all'

/** GET /autopilot (optionally the whole event ring or release history). */
export function getAutopilotStatus(query: AutopilotQuery = ''): Promise<AutopilotStatus> {
  return apiGetNormalized(`/autopilot${query}`, (raw) =>
    normalizeAutopilotStatus(raw as RawAutopilotStatus)
  )
}

/** GET /power. */
export function getPowerStatus(): Promise<PowerStatus> {
  return apiGetNormalized('/power', (raw) => normalizePowerStatus(raw as RawPowerStatus))
}

/** GET /storage. */
export function getStorage(): Promise<Storage> {
  return apiGetNormalized('/storage', (raw) => normalizeStorage(raw as RawStorage))
}

/** GET /cluster. */
export function getClusterInfo(): Promise<ClusterInfo> {
  return apiGetNormalized('/cluster', (raw) => normalizeClusterInfo(raw as RawClusterInfo))
}

/** GET /config. */
export function getEffectiveConfig(): Promise<EffectiveConfig> {
  return apiGetNormalized('/config', (raw) => normalizeEffectiveConfig(raw as RawEffectiveConfig))
}

export function apiGet<T>(path: string): Promise<T> {
  return request<T>(path)
}

export async function apiGetText(path: string): Promise<string> {
  const response = await fetch(path)
  if (!response.ok) {
    throw await errorFromResponse(response)
  }
  return response.text()
}

export function apiPost<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  })
}

export function apiPut<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  })
}

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}
