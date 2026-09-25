import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import HomeView from '@/views/HomeView.vue'
import { jsonResponse, mockFetch, requestBody } from '@/__tests__/helpers'

const RouterLinkStub = {
  props: ['to'],
  template: '<a :href="to"><slot /></a>'
}

type Handlers = Record<string, (init?: RequestInit) => Response>

function mountHome(overrides: Handlers = {}) {
  const handlers: Handlers = {
    '/booty.json': () => jsonResponse({ hosts: { a: {} }, unknownHosts: {} }),
    '/info': () =>
      jsonResponse({
        flatcar: { version: '3815.2.0', pinnedVersion: '' },
        coreos: { version: '40.20240101.3.0' },
        booty: { version: 'v0.9.0', timestamp: '2026-09-01T00:00:00Z' }
      }),
    '/flatcar/pin': (init) => {
      if (init?.method === 'POST') {
        const { version } = requestBody<{ version: string }>(init)
        if (version && !/^\d+\.\d+\.\d+$/.test(version)) {
          return jsonResponse({ error: `invalid version format: ${version}` }, 400)
        }
        return jsonResponse({ pinned: Boolean(version), version, current: '3815.2.0' })
      }
      return jsonResponse({ pinned: false, version: '', current: '3815.2.0' })
    },
    ...overrides
  }
  const spy = mockFetch((url, init) => {
    const handler = handlers[url]
    if (!handler) return jsonResponse({ error: `unexpected ${url}` }, 500)
    return handler(init)
  })
  const wrapper = mount(HomeView, { global: { stubs: { RouterLink: RouterLinkStub } } })
  return { wrapper, handlers, spy }
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('HomeView', () => {
  it('renders the status header from /info, /booty.json and /flatcar/pin', async () => {
    const { wrapper } = mountHome()
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('3815.2.0')
    expect(text).toContain('40.20240101.3.0')
    expect(text).toContain('v0.9.0')
    expect(text).toContain('Tracking latest')
    expect(text).toContain('No version pinned')
  })

  it('shows the server error when pinning an invalid version and keeps the input', async () => {
    const { wrapper } = mountHome()
    await flushPromises()

    await wrapper.find('input[type="text"]').setValue('not-a-version')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('[data-testid="pin-error"]').text()).toBe(
      'invalid version format: not-a-version'
    )
    expect((wrapper.find('input[type="text"]').element as HTMLInputElement).value).toBe(
      'not-a-version'
    )
    expect(wrapper.text()).toContain('No version pinned')
  })

  it('updates the pin card after a successful pin', async () => {
    const { wrapper } = mountHome()
    await flushPromises()

    await wrapper.find('input[type="text"]').setValue('3760.2.0')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('[data-testid="pin-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pin-status"]').text()).toContain('Pinned to 3760.2.0')
    expect(wrapper.text()).toContain('Pinned')
  })

  it('shows an error alert when a status request fails and retries on click', async () => {
    const { wrapper, handlers } = mountHome({
      '/info': () => jsonResponse({ error: 'boom' }, 500)
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="error-message"]').text()).toBe('boom')

    handlers['/info'] = () => jsonResponse({ booty: { version: 'v1' } })
    await wrapper.find('.alert button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="error-message"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('v1')
  })

  it('polls every 30s and stops after unmount', async () => {
    const { wrapper, spy } = mountHome()
    await flushPromises()
    const initialCalls = spy.mock.calls.length
    expect(initialCalls).toBe(3)

    await vi.advanceTimersByTimeAsync(30_000)
    expect(spy.mock.calls.length).toBe(initialCalls + 3)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(spy.mock.calls.length).toBe(initialCalls + 3)
  })
})
