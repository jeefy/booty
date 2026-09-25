import { vi } from 'vitest'

export type FetchHandler = (url: string, init?: RequestInit) => Response | Promise<Response>

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  })
}

export function textResponse(body: string, status = 200): Response {
  return new Response(body, { status, headers: { 'Content-Type': 'text/plain' } })
}

export function mockFetch(handler: FetchHandler) {
  const spy = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    return Promise.resolve(handler(url, init))
  })
  vi.stubGlobal('fetch', spy)
  return spy
}

export function requestBody<T = unknown>(init?: RequestInit): T {
  return JSON.parse(String(init?.body ?? 'null')) as T
}

export function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0))
}
