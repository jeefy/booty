import { afterEach, describe, expect, it } from 'vitest'
import { vi } from 'vitest'
import { ApiError, apiGet, apiPost, apiPut, errorMessage } from '@/api'
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

describe('apiPut', () => {
  it('sends a JSON body with the PUT method and JSON headers', async () => {
    const spy = mockFetch(() =>
      jsonResponse({ name: 'ignition.yaml', source: 'file', writable: true, content: 'x' })
    )
    const result = await apiPut<{ name: string }>('/config/template', {
      name: 'ignition.yaml',
      content: 'variant: flatcar\n'
    })
    expect(result.name).toBe('ignition.yaml')
    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0]!
    expect(url).toBe('/config/template')
    expect(init?.method).toBe('PUT')
    expect(new Headers(init?.headers).get('Content-Type')).toBe('application/json')
    expect(requestBody(init)).toEqual({ name: 'ignition.yaml', content: 'variant: flatcar\n' })
  })

  it('surfaces the 409 read-only error from the JSON envelope', async () => {
    mockFetch(() =>
      jsonResponse({ error: 'template is read-only', reason: 'mounted from a ConfigMap' }, 409)
    )
    const err = await apiPut('/config/template', { name: 'x', content: '' }).catch(
      (e: unknown) => e
    )
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).message).toBe('template is read-only')
    expect((err as ApiError).details).toEqual({ reason: 'mounted from a ConfigMap' })
  })
})

describe('errorMessage', () => {
  it('unwraps ApiError, Error and unknown values', () => {
    expect(errorMessage(new ApiError(400, 'bad mac'))).toBe('bad mac')
    expect(errorMessage(new TypeError('Failed to fetch'))).toBe('Failed to fetch')
    expect(errorMessage('boom')).toBe('boom')
  })
})
