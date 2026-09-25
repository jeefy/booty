import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AboutView from '@/views/AboutView.vue'
import { jsonResponse, mockFetch } from '@/__tests__/helpers'

const baseInfo = {
  flatcar: { version: '3815.2.0' },
  coreos: { version: '40.1' },
  booty: { version: 'v0.9.0', timestamp: '2026-09-01T00:00:00Z' }
}

function mountAbout(info: unknown) {
  mockFetch((url) => {
    if (url === '/info') return jsonResponse(info)
    if (url === '/healthz') return jsonResponse({ status: 'ok' })
    return jsonResponse({ error: `unexpected ${url}` }, 500)
  })
  return mount(AboutView)
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('AboutView', () => {
  it('shows the fleet line when /info reports it', async () => {
    const wrapper = mountAbout({ ...baseInfo, fleet: { hosts: 4, pendingReboots: 1 } })
    await flushPromises()
    const line = wrapper.find('[data-testid="about-fleet"]')
    expect(line.exists()).toBe(true)
    expect(line.text()).toContain('4')
    expect(line.text()).toContain('1')
    expect(line.text()).toContain('pending reboots')
  })

  it('omits the fleet line on servers without a fleet block', async () => {
    const wrapper = mountAbout(baseInfo)
    await flushPromises()
    expect(wrapper.find('[data-testid="about-fleet"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('v0.9.0')
  })
})
