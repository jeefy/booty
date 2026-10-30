import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiGetNormalized, getAutopilotStatus, getBootyData, getInfo } from '@/api'
import { jsonResponse, mockFetch, textResponse } from './helpers'

afterEach(() => {
  vi.unstubAllGlobals()
})

async function failure(promise: Promise<unknown>): Promise<ApiError> {
  const err = await promise.catch((e: unknown) => e)
  expect(err).toBeInstanceOf(ApiError)
  return err as ApiError
}

describe('apiGetNormalized', () => {
  it.each([
    ['null', null],
    ['an array', [1, 2]],
    ['string', 'oops'],
    ['number', 42]
  ])('rejects a top-level %s body', async (kind, body) => {
    mockFetch(() => jsonResponse(body))
    const err = await failure(apiGetNormalized('/info', (raw) => raw))
    expect(err.message).toBe(`Unexpected response from /info: expected an object, got ${kind}`)
    expect(err.status).toBe(200)
  })

  it('rejects a 2xx body that is not JSON', async () => {
    mockFetch(() => textResponse('<html>proxy login</html>'))
    const err = await failure(apiGetNormalized('/booty.json', (raw) => raw))
    expect(err.message).toBe('Unexpected response from /booty.json: not JSON')
  })

  it('wraps a normaliser exception as an ApiError', async () => {
    mockFetch(() => jsonResponse({}))
    const err = await failure(
      apiGetNormalized('/x', () => {
        throw new TypeError('boom')
      })
    )
    expect(err.message).toBe('Unexpected response from /x: boom')
  })
})

describe('typed fetchers', () => {
  it('getBootyData skips broken host entries and defaults unknownHosts', async () => {
    mockFetch(() =>
      jsonResponse({
        hosts: { 'aa:bb': { hostname: 'one' }, 'cc:dd': null, 'ee:ff': 'junk' },
        unknownHosts: []
      })
    )
    const data = await getBootyData()
    expect(Object.keys(data.hosts)).toEqual(['aa:bb'])
    expect(data.hosts['aa:bb']?.mac).toBe('aa:bb')
    expect(data.unknownHosts).toEqual({})
  })

  it('getInfo drops non-object sections and keeps good ones', async () => {
    mockFetch(() => jsonResponse({ flatcar: { version: '1.2.3' }, coreos: 'x', fleet: null }))
    const info = await getInfo()
    expect(info.flatcar).toEqual({ version: '1.2.3' })
    expect(info.coreos).toBeUndefined()
    expect(info.fleet).toBeUndefined()
  })

  it('getInfo rejects a null body', async () => {
    mockFetch(() => jsonResponse(null))
    await failure(getInfo())
  })

  it('getAutopilotStatus keeps the query string and normalises', async () => {
    const fetchMock = mockFetch(() => jsonResponse({}))
    const status = await getAutopilotStatus('?releases=all')
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/autopilot?releases=all')
    expect(status).toBeTypeOf('object')
  })
})
