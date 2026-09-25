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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, init)
  if (!response.ok) {
    throw await errorFromResponse(response)
  }
  return (await response.json()) as T
}

export function apiGet<T>(path: string): Promise<T> {
  return request<T>(path)
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
