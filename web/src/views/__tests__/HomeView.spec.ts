import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import HomeView from '@/views/HomeView.vue'
import { jsonResponse, mockFetch, requestBody } from '@/__tests__/helpers'

const RouterLinkStub = {
  props: ['to'],
  template: '<a :href="to"><slot /></a>'
}

type Handlers = Record<string, (init?: RequestInit) => Response>

const DIGEST = 'sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210'

const fleetHosts = {
  'aa:bb:cc:dd:ee:01': {
    mac: 'aa:bb:cc:dd:ee:01',
    hostname: 'alpha',
    ip: '10.0.0.1',
    booted: '2026-09-24T10:00:00Z',
    os: 'flatcar',
    running: '3760.2.0',
    lastCheck: '2026-09-24T11:30:00Z',
    rebootPending: true
  },
  'aa:bb:cc:dd:ee:02': {
    mac: 'aa:bb:cc:dd:ee:02',
    hostname: 'bravo',
    ip: '10.0.0.2',
    booted: '',
    os: 'bluefin',
    ostreeImage: 'ghcr.io/projectbluefin/bluefin:stable',
    running: `ghcr.io/projectbluefin/bluefin@${DIGEST}`,
    lastCheck: '2026-09-24T11:45:00Z',
    rebootPending: true
  },
  'aa:bb:cc:dd:ee:03': {
    mac: 'aa:bb:cc:dd:ee:03',
    hostname: 'charlie',
    ip: '10.0.0.3',
    booted: '2026-09-24T10:00:00Z',
    os: 'coreos',
    running: '40.20240101.3.0',
    lastCheck: '2026-09-24T11:50:00Z',
    rebootPending: false
  }
}

const baseInfo = {
  flatcar: { version: '3815.2.0', pinnedVersion: '' },
  coreos: { version: '40.20240101.3.0' },
  bluefin: { version: '42.20260901', pinnedVersion: '' },
  booty: { version: 'v0.9.0', timestamp: '2026-09-01T00:00:00Z' }
}

const FINGERPRINT = 'sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08'

const FLATCAR_CA_SHA = 'ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2'

const secureBootInfo = {
  enabled: true,
  ready: true,
  bundleVersion: 'ipxe-16.1_v2.0.0_shim-16.1-7_grub-2.12-64.fc44',
  trusted: ['microsoft', 'flatcar'],
  bootURL: 'http://10.0.0.1:8080/boot/sb',
  flatcarCA: {
    flatcarVersion: '4757.2.0',
    sha256: FLATCAR_CA_SHA,
    subject: 'CN=Flatcar Container Linux Secure Boot Development CA',
    notAfter: '2037-01-19T00:00:00Z',
    url: 'http://10.0.0.1:8080/boot/secureboot/flatcar-ca.der'
  },
  warnings: []
}

const clusterInfo = {
  distribution: 'kubeadm',
  controlPlane: 'managed',
  endpoint: 'https://10.0.0.1:6443',
  cni: 'cilium',
  ready: true,
  caFingerprint: FINGERPRINT,
  hosts: [
    { mac: 'aa:bb:cc:dd:ee:01', hostname: 'alpha', os: 'flatcar', role: 'control-plane', booted: '' },
    { mac: 'aa:bb:cc:dd:ee:02', hostname: 'bravo', os: 'flatcar', role: 'worker', booted: '' },
    { mac: 'aa:bb:cc:dd:ee:03', hostname: 'charlie', os: 'coreos', role: '', booted: '' }
  ],
  warnings: []
}

const externalClusterInfo = {
  distribution: 'kubeadm',
  controlPlane: 'external',
  source: 'external',
  endpoint: '10.96.0.1:443',
  apiServer: 'https://10.96.0.1:443',
  cni: 'cilium',
  ready: true,
  connected: true,
  nodes: 6,
  caFingerprint: FINGERPRINT,
  hosts: [{ mac: 'aa:bb:cc:dd:ee:02', hostname: 'bravo', os: 'flatcar', role: 'worker', booted: '' }],
  warnings: []
}

function mountHome(overrides: Handlers = {}) {
  const handlers: Handlers = {
    '/booty.json': () => jsonResponse({ hosts: { a: {} }, unknownHosts: {} }),
    '/info': () => jsonResponse(baseInfo),
    '/cluster': () => jsonResponse({ error: 'not found' }, 404),
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
    expect(text).toContain('42.20260901')
    expect(text).toContain('v0.9.0')
    expect(text).toContain('Tracking latest')
    expect(text).toContain('No version pinned')
  })

  it('renders the Bluefin stat as tracking latest, pinned, or not downloaded', async () => {
    const { wrapper, handlers } = mountHome()
    await flushPromises()
    let stat = wrapper.find('[data-testid="bluefin-stat"]')
    expect(stat.find('.stat-value').text()).toBe('42.20260901')
    expect(stat.find('.stat-value').attributes('title')).toBeUndefined()
    expect(stat.text()).toContain('Tracking latest')

    handlers['/info'] = () =>
      jsonResponse({ ...baseInfo, bluefin: { version: '42.20260901', pinnedVersion: '42.20260901' } })
    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    stat = wrapper.find('[data-testid="bluefin-stat"]')
    expect(stat.text()).toContain('Pinned')
    expect(stat.text()).toContain('42.20260901')

    handlers['/info'] = () => jsonResponse({ ...baseInfo, bluefin: { version: '0.0.0', pinnedVersion: '' } })
    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    stat = wrapper.find('[data-testid="bluefin-stat"]')
    expect(stat.find('.stat-value').text()).toBe('—')
    expect(stat.find('.stat-value').attributes('title')).toBe('not downloaded yet')
    expect(stat.text()).toContain('Not downloaded yet')
  })

  it('renders a dash for Bluefin on servers without a bluefin block', async () => {
    const { flatcar, coreos, booty } = baseInfo
    const { wrapper } = mountHome({ '/info': () => jsonResponse({ flatcar, coreos, booty }) })
    await flushPromises()
    const stat = wrapper.find('[data-testid="bluefin-stat"]')
    expect(stat.exists()).toBe(true)
    expect(stat.find('.stat-value').text()).toBe('—')
    expect(stat.find('.stat-value').attributes('title')).toBe('not downloaded yet')
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

  it('renders the fleet card from /info.fleet with the pending-reboot list and highlight', async () => {
    const { wrapper } = mountHome({
      '/booty.json': () => jsonResponse({ hosts: fleetHosts, unknownHosts: {} }),
      '/info': () => jsonResponse({ ...baseInfo, fleet: { hosts: 3, pendingReboots: 2 } })
    })
    await flushPromises()

    const card = wrapper.find('[data-testid="fleet-card"]')
    expect(card.exists()).toBe(true)
    expect(card.classes()).toContain('fleet-panel--alert')
    expect(card.find('[data-testid="fleet-hosts"]').text()).toBe('3')
    const pending = card.find('[data-testid="fleet-pending"]')
    expect(pending.text()).toBe('2')
    expect(pending.classes()).toContain('fleet-pending')
    expect(card.text()).toContain('Reported by /info')

    const rows = card.findAll('[data-testid="fleet-list"] tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('alpha')
    expect(rows[0]!.text()).toContain('3760.2.0')
    expect(rows[0]!.text()).toContain('3815.2.0')
    expect(rows[1]!.text()).toContain('bravo')
    expect(rows[1]!.text()).toContain('ghcr.io/projectbluefin/bluefin@fedcba987654')
    expect(rows[1]!.text()).toContain('42.20260901')
    expect(card.text()).not.toContain('charlie')
  })

  it('derives fleet counts from /booty.json when /info has no fleet block', async () => {
    const { wrapper } = mountHome({
      '/booty.json': () => jsonResponse({ hosts: fleetHosts, unknownHosts: {} })
    })
    await flushPromises()

    const card = wrapper.find('[data-testid="fleet-card"]')
    expect(card.find('[data-testid="fleet-hosts"]').text()).toBe('3')
    expect(card.find('[data-testid="fleet-pending"]').text()).toBe('2')
    expect(card.classes()).toContain('fleet-panel--alert')
    expect(card.text()).toContain('Derived from host records')
    expect(card.findAll('[data-testid="fleet-list"] tbody tr')).toHaveLength(2)
  })

  it('does not highlight the fleet card when nothing is pending', async () => {
    const { wrapper } = mountHome({
      '/info': () => jsonResponse({ ...baseInfo, fleet: { hosts: 1, pendingReboots: 0 } })
    })
    await flushPromises()

    const card = wrapper.find('[data-testid="fleet-card"]')
    expect(card.classes()).not.toContain('fleet-panel--alert')
    expect(card.find('[data-testid="fleet-pending"]').text()).toBe('0')
    expect(card.find('[data-testid="fleet-pending"]').classes()).not.toContain('fleet-pending')
    expect(card.find('[data-testid="fleet-list"]').exists()).toBe(false)
    expect(card.find('[data-testid="fleet-empty"]').exists()).toBe(true)
  })

  it('refreshes the fleet card on the 30s poll', async () => {
    const { wrapper, handlers } = mountHome({
      '/info': () => jsonResponse({ ...baseInfo, fleet: { hosts: 1, pendingReboots: 0 } })
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="fleet-pending"]').text()).toBe('0')

    handlers['/booty.json'] = () => jsonResponse({ hosts: fleetHosts, unknownHosts: {} })
    handlers['/info'] = () => jsonResponse({ ...baseInfo, fleet: { hosts: 3, pendingReboots: 2 } })
    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()

    expect(wrapper.find('[data-testid="fleet-pending"]').text()).toBe('2')
    expect(wrapper.findAll('[data-testid="fleet-list"] tbody tr')).toHaveLength(2)
  })

  it('polls every 30s and stops after unmount', async () => {
    const { wrapper, spy } = mountHome()
    await flushPromises()
    const initialCalls = spy.mock.calls.length
    expect(initialCalls).toBe(4)

    await vi.advanceTimersByTimeAsync(30_000)
    expect(spy.mock.calls.length).toBe(initialCalls + 4)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(spy.mock.calls.length).toBe(initialCalls + 4)
  })

  it('renders the cluster card from /cluster with facts, readiness, role counts and fingerprint', async () => {
    const { wrapper } = mountHome({ '/cluster': () => jsonResponse(clusterInfo) })
    await flushPromises()

    const card = wrapper.find('[data-testid="cluster-card"]')
    expect(card.exists()).toBe(true)
    expect(card.classes()).toContain('cluster-panel--ready')
    expect(card.find('.status-dot').classes()).toContain('status-dot--ready')
    expect(card.find('[data-testid="cluster-state"]').text()).toBe('bootstrapped')
    expect(card.find('[data-testid="cluster-roles"]').text()).toBe('1 control-plane · 2 workers')

    const facts = card.find('[data-testid="cluster-facts"]')
    const pairs = facts.findAll('dt').map((dt, i) => [dt.text(), facts.findAll('dd')[i]!.text()])
    expect(pairs.slice(0, 4)).toEqual([
      ['Distribution', 'kubeadm'],
      ['Control plane', 'managed'],
      ['Endpoint', 'https://10.0.0.1:6443'],
      ['CNI', 'cilium']
    ])
    expect(pairs[4]![0]).toBe('CA fingerprint')
    const fingerprint = card.find('[data-testid="cluster-fingerprint"]')
    expect(fingerprint.text()).toBe(FINGERPRINT)
    expect(fingerprint.classes()).toContain('mono')
    expect(fingerprint.attributes('title')).toBe(FINGERPRINT)
    expect(card.find('[data-testid="cluster-warnings"]').exists()).toBe(false)
  })

  it('copies the CA fingerprint to the clipboard and shows transient feedback', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const { wrapper } = mountHome({ '/cluster': () => jsonResponse(clusterInfo) })
    await flushPromises()

    const button = wrapper.find('[data-testid="cluster-copy"]')
    expect(button.text()).toBe('Copy')
    await button.trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith(FINGERPRINT)
    expect(wrapper.find('[data-testid="cluster-copy"]').text()).toBe('Copied')

    await vi.advanceTimersByTimeAsync(1_500)
    expect(wrapper.find('[data-testid="cluster-copy"]').text()).toBe('Copy')
  })

  it('shows not-ready state, a dash for missing endpoint/fingerprint and the warnings list', async () => {
    const { wrapper } = mountHome({
      '/cluster': () =>
        jsonResponse({
          ...clusterInfo,
          ready: false,
          endpoint: '',
          caFingerprint: '',
          warnings: ['CA key is served on the boot VLAN', 'no control-plane host registered']
        })
    })
    await flushPromises()

    const card = wrapper.find('[data-testid="cluster-card"]')
    expect(card.classes()).not.toContain('cluster-panel--ready')
    expect(card.find('.status-dot').classes()).not.toContain('status-dot--ready')
    expect(card.find('[data-testid="cluster-state"]').text()).toBe('not ready yet')
    expect(card.find('[data-testid="cluster-fingerprint"]').exists()).toBe(false)
    expect(card.find('[data-testid="cluster-copy"]').exists()).toBe(false)
    expect(card.find('[data-testid="cluster-api-server"]').exists()).toBe(false)
    expect(card.find('[data-testid="cluster-nodes"]').exists()).toBe(false)
    expect(card.findAll('[data-testid="cluster-facts"] dd').map((dd) => dd.text())).toEqual([
      'kubeadm',
      'managed',
      '—',
      'cilium',
      '—'
    ])

    const warnings = card.find('[data-testid="cluster-warnings"]')
    expect(warnings.exists()).toBe(true)
    expect(warnings.classes()).toContain('alert-warning')
    expect(warnings.attributes('role')).toBe('status')
    expect(warnings.findAll('li').map((li) => li.text())).toEqual([
      'CA key is served on the boot VLAN',
      'no control-plane host registered'
    ])
  })

  it('reads connected, the node count and the API server for a reachable external control plane', async () => {
    const { wrapper } = mountHome({ '/cluster': () => jsonResponse(externalClusterInfo) })
    await flushPromises()

    const card = wrapper.find('[data-testid="cluster-card"]')
    expect(card.classes()).toContain('cluster-panel--ready')
    expect(card.find('.status-dot').classes()).toContain('status-dot--ready')
    expect(card.find('[data-testid="cluster-state"]').text()).toBe('connected')
    expect(card.find('[data-testid="cluster-roles"]').text()).toBe('0 control-plane · 1 worker')

    const facts = card.find('[data-testid="cluster-facts"]')
    const pairs = facts.findAll('dt').map((dt, i) => [dt.text(), facts.findAll('dd')[i]!.text()])
    expect(pairs).toEqual([
      ['Distribution', 'kubeadm'],
      ['Control plane', 'external'],
      ['Endpoint', '10.96.0.1:443'],
      ['API server', 'https://10.96.0.1:443'],
      ['CNI', 'cilium'],
      ['Nodes', '6'],
      ['CA fingerprint', `${FINGERPRINT}Copy`]
    ])
    expect(card.find('[data-testid="cluster-api-server"]').attributes('title')).toBe(
      'https://10.96.0.1:443'
    )
    expect(card.find('[data-testid="cluster-fingerprint"]').text()).toBe(FINGERPRINT)
    expect(card.find('[data-testid="cluster-copy"]').exists()).toBe(true)
    expect(card.find('[data-testid="cluster-warnings"]').exists()).toBe(false)
  })

  it('reads unreachable, hides the node count and lists the API warning for an external control plane Booty cannot reach', async () => {
    const { wrapper } = mountHome({
      '/cluster': () =>
        jsonResponse({
          ...externalClusterInfo,
          ready: false,
          connected: false,
          nodes: 0,
          caFingerprint: '',
          warnings: [
            'API server https://10.96.0.1:443 unreachable: GET /api/v1/nodes: API server unreachable: dial tcp 10.96.0.1:443: connect: connection refused'
          ]
        })
    })
    await flushPromises()

    const card = wrapper.find('[data-testid="cluster-card"]')
    expect(card.classes()).not.toContain('cluster-panel--ready')
    expect(card.find('.status-dot').classes()).not.toContain('status-dot--ready')
    expect(card.find('[data-testid="cluster-state"]').text()).toBe('unreachable')
    expect(card.find('[data-testid="cluster-nodes"]').exists()).toBe(false)
    expect(card.find('[data-testid="cluster-api-server"]').text()).toBe('https://10.96.0.1:443')
    expect(card.find('[data-testid="cluster-fingerprint"]').exists()).toBe(false)
    expect(card.findAll('[data-testid="cluster-facts"] dd').map((dd) => dd.text())).toEqual([
      'kubeadm',
      'external',
      '10.96.0.1:443',
      'https://10.96.0.1:443',
      'cilium',
      '—'
    ])
    expect(
      card
        .find('[data-testid="cluster-warnings"]')
        .findAll('li')
        .map((li) => li.text())
    ).toEqual([
      'API server https://10.96.0.1:443 unreachable: GET /api/v1/nodes: API server unreachable: dial tcp 10.96.0.1:443: connect: connection refused'
    ])
  })

  it('keeps the managed wording when an older server sends no source field', async () => {
    const { wrapper } = mountHome({
      '/cluster': () => jsonResponse({ ...clusterInfo, source: undefined, connected: undefined })
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="cluster-state"]').text()).toBe('bootstrapped')
  })

  it('hides the cluster card when /cluster answers 404 and keeps the rest of the page', async () => {
    const { wrapper } = mountHome()
    await flushPromises()
    expect(wrapper.find('[data-testid="cluster-card"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="cluster-unavailable"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Cluster')
    expect(wrapper.find('[data-testid="error-message"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="fleet-card"]').exists()).toBe(true)
  })

  it('shows an unavailable notice, not the page error, when /cluster fails for another reason', async () => {
    const { wrapper } = mountHome({
      '/cluster': () => jsonResponse({ error: 'cluster CA unreadable' }, 500)
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="cluster-card"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="cluster-unavailable"]').text()).toContain(
      'cluster CA unreadable'
    )
    expect(wrapper.find('[data-testid="error-message"]').exists()).toBe(false)
  })

  it('shows a one-line off state for Secure Boot when /info says it is disabled', async () => {
    const { wrapper } = mountHome({
      '/info': () =>
        jsonResponse({
          ...baseInfo,
          secureBoot: { enabled: false, ready: false, bundleVersion: '', trusted: ['microsoft'], bootURL: '', flatcarCA: null, warnings: [] }
        })
    })
    await flushPromises()

    const card = wrapper.find('[data-testid="secure-boot-card"]')
    expect(card.exists()).toBe(true)
    expect(card.classes()).not.toContain('secure-boot-panel--ready')
    expect(card.find('[data-testid="secure-boot-state"]').text()).toBe('Secure Boot: off')
    expect(card.find('[data-testid="secure-boot-off-hint"]').text()).toContain('--secureBoot --proxyDHCP')
    expect(card.find('[data-testid="secure-boot-facts"]').exists()).toBe(false)
    expect(card.find('[data-testid="secure-boot-warnings"]').exists()).toBe(false)
  })

  it('still shows the Secure Boot card as off for servers without the /info block', async () => {
    const { wrapper } = mountHome()
    await flushPromises()
    expect(wrapper.find('[data-testid="secure-boot-state"]').text()).toBe('Secure Boot: off')
  })

  it('renders the Secure Boot card with bundle, trusted list, Flatcar CA fingerprint and download link', async () => {
    const { wrapper } = mountHome({
      '/info': () => jsonResponse({ ...baseInfo, secureBoot: secureBootInfo })
    })
    await flushPromises()

    const card = wrapper.find('[data-testid="secure-boot-card"]')
    expect(card.classes()).toContain('secure-boot-panel--ready')
    expect(card.find('.status-dot').classes()).toContain('status-dot--ready')
    expect(card.find('[data-testid="secure-boot-state"]').text()).toBe('Secure Boot: ready')
    expect(card.find('[data-testid="secure-boot-trusted"]').text()).toBe('microsoft, flatcar')

    const facts = card.find('[data-testid="secure-boot-facts"]')
    const pairs = facts.findAll('dt').map((dt, i) => [dt.text(), facts.findAll('dd')[i]!.text()])
    expect(pairs[0]).toEqual(['Bundle', 'ipxe-16.1_v2.0.0_shim-16.1-7_grub-2.12-64.fc44'])
    expect(pairs[1]).toEqual(['Boot URL', 'http://10.0.0.1:8080/boot/sb'])
    expect(pairs[2]![0]).toBe('Flatcar CA')

    const ca = card.find('[data-testid="secure-boot-ca"]')
    expect(ca.text()).toBe(FLATCAR_CA_SHA)
    expect(ca.classes()).toContain('mono')
    expect(ca.attributes('title')).toContain('4757.2.0')
    const download = card.find('[data-testid="secure-boot-download"]')
    expect(download.attributes('href')).toBe('http://10.0.0.1:8080/boot/secureboot/flatcar-ca.der')
    expect(download.attributes('download')).toBe('flatcar-ca.der')
    expect(card.find('[data-testid="secure-boot-warnings"]').exists()).toBe(false)
  })

  it('copies the Flatcar CA fingerprint with transient feedback', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const { wrapper } = mountHome({
      '/info': () => jsonResponse({ ...baseInfo, secureBoot: secureBootInfo })
    })
    await flushPromises()

    const button = wrapper.find('[data-testid="secure-boot-copy"]')
    expect(button.text()).toBe('Copy')
    await button.trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith(FLATCAR_CA_SHA)
    expect(wrapper.find('[data-testid="secure-boot-copy"]').text()).toBe('Copied')
    await vi.advanceTimersByTimeAsync(1_500)
    expect(wrapper.find('[data-testid="secure-boot-copy"]').text()).toBe('Copy')
  })

  it('shows syncing state, a missing-CA notice and the warnings list', async () => {
    const { wrapper } = mountHome({
      '/info': () =>
        jsonResponse({
          ...baseInfo,
          secureBoot: {
            ...secureBootInfo,
            ready: false,
            trusted: ['microsoft'],
            flatcarCA: null,
            warnings: [
              'host aa:bb:cc:dd:ee:01 (flatcar): Secure Boot host; the Flatcar CA is not in --secureBootTrusted, boot refused',
              'host aa:bb:cc:dd:ee:02 (bluefin): Secure Boot host; the installed Bluefin image is unsigned, install refused'
            ]
          }
        })
    })
    await flushPromises()

    const card = wrapper.find('[data-testid="secure-boot-card"]')
    expect(card.classes()).not.toContain('secure-boot-panel--ready')
    expect(card.classes()).toContain('secure-boot-panel--alert')
    expect(card.find('[data-testid="secure-boot-state"]').text()).toBe('Secure Boot: syncing artefacts')
    expect(card.find('[data-testid="secure-boot-ca"]').exists()).toBe(false)
    expect(card.find('[data-testid="secure-boot-copy"]').exists()).toBe(false)
    expect(card.find('[data-testid="secure-boot-ca-missing"]').text()).toContain('not extracted yet')

    const warnings = card.find('[data-testid="secure-boot-warnings"]')
    expect(warnings.classes()).toContain('alert-warning')
    expect(warnings.attributes('role')).toBe('status')
    expect(warnings.findAll('li')).toHaveLength(2)
    expect(warnings.text()).toContain('install refused')
  })

  it('refreshes the cluster card on the 30s poll', async () => {
    const { wrapper, handlers } = mountHome({
      '/cluster': () => jsonResponse({ ...clusterInfo, ready: false })
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="cluster-state"]').text()).toBe('not ready yet')

    handlers['/cluster'] = () => jsonResponse(clusterInfo)
    await vi.advanceTimersByTimeAsync(30_000)
    await flushPromises()
    expect(wrapper.find('[data-testid="cluster-state"]').text()).toBe('bootstrapped')
  })

  it('shows the fleet power summary from /info and falls back to the host records', async () => {
    const { wrapper } = mountHome({
      '/info': () =>
        jsonResponse({ ...baseInfo, power: { up: 6, off: 0, unreachable: 1, inFlight: 2 } })
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="fleet-power"]').text()).toContain('6 up · 0 off · 1 unreachable')
    expect(wrapper.find('[data-testid="fleet-power-inflight"]').text()).toContain('2 in flight')

    const derived = mountHome({
      '/booty.json': () =>
        jsonResponse({
          hosts: {
            a: { mac: 'a', power: { state: 'up' } },
            b: { mac: 'b', power: { state: 'off' } },
            c: { mac: 'c' }
          },
          unknownHosts: {}
        })
    })
    await flushPromises()
    expect(derived.wrapper.find('[data-testid="fleet-power"]').text()).toContain('1 up · 1 off · 0 unreachable')
    expect(derived.wrapper.find('[data-testid="fleet-power-inflight"]').exists()).toBe(false)
  })
})
