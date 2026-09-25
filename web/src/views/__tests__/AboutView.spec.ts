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

  it('shows the Bluefin version only when /info reports a bluefin block', async () => {
    const without = mountAbout(baseInfo)
    await flushPromises()
    expect(without.find('[data-testid="about-bluefin"]').exists()).toBe(false)

    vi.unstubAllGlobals()
    const pinned = mountAbout({
      ...baseInfo,
      bluefin: { version: '42.20260901', pinnedVersion: '42.20260901' }
    })
    await flushPromises()
    const stat = pinned.find('[data-testid="about-bluefin"]')
    expect(stat.exists()).toBe(true)
    expect(stat.find('.stat-value').text()).toBe('42.20260901')
    expect(stat.text()).toContain('Pinned to 42.20260901')

    vi.unstubAllGlobals()
    const empty = mountAbout({ ...baseInfo, bluefin: { version: '0.0.0', pinnedVersion: '' } })
    await flushPromises()
    const emptyStat = empty.find('[data-testid="about-bluefin"]')
    expect(emptyStat.find('.stat-value').text()).toBe('—')
    expect(emptyStat.text()).toContain('Not downloaded yet')
  })
})
