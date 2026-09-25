import { afterEach, describe, expect, it } from 'vitest'
import { vi } from 'vitest'
import { ApiError, apiGet, apiPost, errorMessage } from '@/api'
import { jsonResponse, mockFetch, requestBody, textResponse } from './helpers'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('apiGet', () => {
  it('returns the parsed JSON body on 2xx', async () => {
    mockFetch(() => jsonResponse({ status: 'ok' }))
    await expect(apiGet<{ status: string }>('/healthz')).resolves.toEqual({ status: 'ok' })
  })

  it('throws ApiError with the server message from the JSON error envelope', async () => {
    mockFetch(() => jsonResponse({ error: 'host not found' }, 404))
    const err = await apiGet('/booty.json').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(404)
    expect((err as ApiError).message).toBe('host not found')
  })

  it('falls back to HTTP <status> when the error body is not JSON', async () => {
    mockFetch(() => textResponse('<html>Bad Gateway</html>', 502))
    const err = await apiGet('/registry').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(502)
    expect((err as ApiError).message).toBe('HTTP 502')
  })

  it('falls back to HTTP <status> when the JSON body has no error string', async () => {
    mockFetch(() => jsonResponse({ nope: true }, 500))
    const err = await apiGet('/info').catch((e: unknown) => e)
    expect((err as ApiError).message).toBe('HTTP 500')
  })
})

describe('apiPost', () => {
  it('sends a JSON body with the right method and headers', async () => {
    const spy = mockFetch(() => jsonResponse({ status: 'ok' }))
    await apiPost('/unregister', { mac: 'aa:bb:cc:dd:ee:ff' })
    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0]!
    expect(url).toBe('/unregister')
    expect(init?.method).toBe('POST')
    expect(new Headers(init?.headers).get('Content-Type')).toBe('application/json')
    expect(requestBody(init)).toEqual({ mac: 'aa:bb:cc:dd:ee:ff' })
  })

  it('surfaces 400 validation errors from the server', async () => {
    mockFetch(() => jsonResponse({ error: 'invalid version format' }, 400))
    await expect(apiPost('/flatcar/pin', { version: 'nope' })).rejects.toMatchObject({
      status: 400,
      message: 'invalid version format'
    })
  })
})

describe('errorMessage', () => {
  it('unwraps ApiError, Error and unknown values', () => {
    expect(errorMessage(new ApiError(400, 'bad mac'))).toBe('bad mac')
    expect(errorMessage(new TypeError('Failed to fetch'))).toBe('Failed to fetch')
    expect(errorMessage('boom')).toBe('boom')
  })
})
