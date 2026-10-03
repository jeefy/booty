import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import StorageView from '@/views/StorageView.vue'
import { flushPromises, jsonResponse, mockFetch } from '@/__tests__/helpers'

const RouterLinkStub = {
  props: ['to'],
  template: '<a :href="to"><slot /></a>'
}

const GiB = 1024 ** 3

const hosts = {
  hosts: {
    'aa:bb:cc:dd:ee:01': { mac: 'aa:bb:cc:dd:ee:01', hostname: 'ehrlitan', os: 'bluefin' },
    'aa:bb:cc:dd:ee:02': { mac: 'aa:bb:cc:dd:ee:02', hostname: 'aren', os: 'bluefin' },
    'aa:bb:cc:dd:ee:03': { mac: 'aa:bb:cc:dd:ee:03', hostname: 'kalam', os: 'flatcar' }
  },
  unknownHosts: {}
}

const storage = {
  dataDir: '/data',
  total: { bytes: 100 * GiB },
  free: { bytes: 40 * GiB },
  used: { bytes: 6 * GiB },
  computedAt: '2026-10-03T12:00:00Z',
  os: {
    flatcar: {
      tracked: true,
      channel: 'stable',
      pin: '4757.2.0',
      bytes: 0.5 * GiB,
      releases: [
        {
          version: '4757.2.0',
          bytes: 0.5 * GiB,
          files: 3,
          modified: '2026-09-28T23:34:00Z',
          links: ['current', 'lastGood'],
          hosts: { running: ['aa:bb:cc:dd:ee:03'], pinned: [] },
          retained: 'current',
          cached: true
        }
      ]
    },
    coreos: {
      tracked: false,
      channel: 'none',
      pin: '',
      bytes: 2 * GiB,
      releases: [
        {
          version: '44.20260829.3.1',
          bytes: 2 * GiB,
          files: 3,
          modified: '2026-09-28T23:35:00Z',
          links: ['current'],
          hosts: { running: [], pinned: [] },
          retained: '-',
          cached: false
        }
      ]
    },
    bluefin: {
      tracked: true,
      channel: '',
      pin: '',
      source: 'projectbluefin/server',
      bytes: 3.5 * GiB,
      releases: [
        {
          version: '26.09.678',
          bytes: 1.2 * GiB,
          files: 9,
          modified: '2026-09-28T23:36:00Z',
          links: ['current'],
          hosts: { running: ['aa:bb:cc:dd:ee:01'], pinned: [] },
          retained: 'current',
          cached: true
        },
        {
          version: '26.09.673',
          bytes: 1.2 * GiB,
          files: 9,
          modified: '2026-09-27T23:36:00Z',
          links: ['previous', 'lastGood'],
          hosts: { running: ['aa:bb:cc:dd:ee:02'], pinned: ['aa:bb:cc:dd:ee:02'] },
          retained: 'previous',
          cached: true
        },
        {
          version: '2026.09.2',
          bytes: 1.1 * GiB,
          files: 9,
          modified: '2026-09-20T23:36:00Z',
          links: [],
          hosts: { running: [], pinned: [] },
          retained: '-',
          cached: true
        }
      ]
    }
  },
  assets: [
    { name: 'ipxe.efi', bytes: 1152512, modified: '2026-09-28T10:01:00Z', kind: 'ipxe' },
    { name: 'version.txt', bytes: 169, modified: '2026-09-25T16:18:00Z', kind: 'other' }
  ],
  other: [{ name: 'config', bytes: 4096 }],
  autopilot: { stateBytes: 8192, reportsBytes: 20480 }
}

const autopilot = {
  mode: 'guard',
  actuator: 'kured',
  os: {
    bluefin: {
      fleetTarget: '26.09.678',
      current: '26.09.678',
      lastGood: '26.09.673',
      held: false,
      releases: [
        {
          os: 'bluefin',
          version: '26.09.678',
          state: 'rolling',
          since: '2026-09-28T23:36:00Z',
          cached: true
        },
        {
          os: 'bluefin',
          version: '26.09.673',
          state: 'good',
          since: '2026-09-27T23:36:00Z',
          cached: true
        },
        {
          os: 'bluefin',
          version: '26.09.650',
          state: 'quarantined',
          since: '2026-09-10T23:36:00Z',
          attempts: 3,
          cached: false
        },
        {
          os: 'bluefin',
          version: '26.09.640',
          state: 'good',
          since: '2026-09-01T23:36:00Z',
          cached: false
        }
      ]
    }
  }
}

function mountView(overrides: { storage?: unknown; autopilot?: unknown } = {}) {
  mockFetch((url) => {
    if (url === '/storage') return jsonResponse(overrides.storage ?? storage)
    if (url === '/autopilot') return jsonResponse(overrides.autopilot ?? autopilot)
    if (url === '/booty.json') return jsonResponse(hosts)
    return jsonResponse({ error: `unexpected ${url}` }, 500)
  })
  return mount(StorageView, { global: { stubs: { RouterLink: RouterLinkStub } } })
}

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('StorageView', () => {
  it('shows totals, the usage bar and the data directory', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('.page-header').text()).toContain('/data')
    expect(wrapper.find('[data-testid="storage-used"]').text()).toBe('6.00 GiB')
    expect(wrapper.find('[data-testid="storage-free"]').text()).toBe('40.0 GiB')
    expect(wrapper.find('[data-testid="storage-summary"]').text()).toContain('of 100 GiB total')
    const bar = wrapper.find('[data-testid="storage-bar"]')
    expect(bar.find('.usage-seg.usage-free').attributes('style')).toContain('width: 40%')
    expect(bar.find('.usage-seg.usage-bluefin').attributes('style')).toContain('width: 3.5%')
    expect(bar.text()).toContain('Other data on this filesystem')
  })

  it('renders each OS with links, retention, host counts and tooltips', async () => {
    const wrapper = mountView()
    await flushPromises()

    const bluefin = wrapper.find('[data-testid="storage-os-bluefin"]')
    const current = bluefin.find('[data-release="26.09.678"]')
    expect(current.find('[data-link="current"]').classes()).toContain('text-bg-success')
    expect(current.find('[data-testid="retained"]').text()).toBe('current')
    expect(current.find('[data-testid="hosts-running"]').text()).toBe('1 running')
    expect(current.find('[data-testid="hosts-running"]').attributes('title')).toContain(
      'ehrlitan (aa:bb:cc:dd:ee:01)'
    )
    expect(current.text()).toContain('1.20 GiB')
    expect(current.text()).toContain('9 files')
    expect(current.find('[data-state="rolling"]').exists()).toBe(true)

    const previous = bluefin.find('[data-release="26.09.673"]')
    expect(previous.find('[data-link="previous"]').exists()).toBe(true)
    expect(previous.find('[data-link="lastGood"]').exists()).toBe(true)
    expect(previous.find('[data-testid="hosts-pinned"]').text()).toBe('1 pinned')

    const orphan = bluefin.find('[data-release="2026.09.2"]')
    expect(orphan.find('[data-testid="retained"]').text()).toBe('—')
    expect(orphan.find('[data-testid="retained"]').attributes('title')).toContain('prunes it')
    expect(bluefin.find('thead').text()).toContain('Autopilot')

    const flatcar = wrapper.find('[data-testid="storage-os-flatcar"]')
    expect(flatcar.find('[data-release="4757.2.0"] [data-testid="hosts-running"]').text()).toBe(
      '1 running'
    )
    expect(flatcar.find('thead').text()).not.toContain('Autopilot')
    expect(flatcar.find('[data-testid="autopilot-cell"]').exists()).toBe(false)
    expect(flatcar.findAll('thead th')).toHaveLength(6)
    expect(bluefin.findAll('thead th')).toHaveLength(7)
    expect(wrapper.text()).toContain('channel stable')
    expect(wrapper.text()).toContain('pinned 4757.2.0')
    expect(wrapper.text()).toContain('projectbluefin/server')
  })

  it('hides pruned history by default but keeps quarantined and timeout records', async () => {
    const wrapper = mountView()
    await flushPromises()
    const bluefin = wrapper.find('[data-testid="storage-os-bluefin"]')
    const versions = bluefin.findAll('tbody tr').map((tr) => tr.attributes('data-release'))
    expect(versions).toEqual(['2026.09.2', '26.09.678', '26.09.673', '26.09.650'])

    const quarantined = bluefin.find('[data-release="26.09.650"]')
    expect(quarantined.classes()).toContain('pruned')
    expect(quarantined.classes()).toContain('prominent')
    expect(quarantined.find('[data-testid="pruned-badge"]').text()).toBe('files pruned')
    expect(quarantined.find('[data-state="quarantined"]').text()).toBe('Quarantined')
    expect(bluefin.find('[data-release="26.09.640"]').exists()).toBe(false)

    const toggle = wrapper.find('[data-testid="show-pruned"]')
    expect((toggle.element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('[data-testid="pruned-count"]').text()).toBe('1')
    const note = bluefin.find('[data-testid="pruned-note"]')
    expect(note.text()).toContain('1 older release known to the autopilot, files pruned')
    expect(
      wrapper.find('[data-testid="storage-os-flatcar"] [data-testid="pruned-note"]').exists()
    ).toBe(false)
    expect(bluefin.find('[data-release="2026.09.2"] [data-testid="autopilot-cell"]').text()).toBe(
      '—'
    )
    expect(bluefin.find('[data-release="26.09.678"]').classes()).not.toContain('pruned')
  })

  it('the toggle and the per-OS note reveal pruned history and persist the choice', async () => {
    const wrapper = mountView()
    await flushPromises()
    const bluefin = wrapper.find('[data-testid="storage-os-bluefin"]')
    await bluefin.find('[data-testid="pruned-note"] button').trigger('click')

    expect((wrapper.find('[data-testid="show-pruned"]').element as HTMLInputElement).checked).toBe(
      true
    )
    expect(bluefin.findAll('tbody tr').map((tr) => tr.attributes('data-release'))).toEqual([
      '2026.09.2',
      '26.09.678',
      '26.09.673',
      '26.09.650',
      '26.09.640'
    ])
    const old = bluefin.find('[data-release="26.09.640"]')
    expect(old.classes()).toContain('pruned')
    expect(old.classes()).not.toContain('prominent')
    expect(old.find('[data-testid="pruned-badge"]').exists()).toBe(true)
    expect(old.find('[data-state="good"]').text()).toBe('Good')
    expect(bluefin.find('[data-testid="pruned-note"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pruned-count"]').text()).toBe('1')
    expect(localStorage.getItem('booty.storage.showPruned')).toBe('true')

    await wrapper.find('[data-testid="show-pruned"]').setValue(false)
    expect(bluefin.find('[data-release="26.09.640"]').exists()).toBe(false)
    expect(localStorage.getItem('booty.storage.showPruned')).toBe('false')
  })

  it('starts with pruned history shown when localStorage says so', async () => {
    localStorage.setItem('booty.storage.showPruned', 'true')
    const wrapper = mountView()
    await flushPromises()
    expect((wrapper.find('[data-testid="show-pruned"]').element as HTMLInputElement).checked).toBe(
      true
    )
    expect(
      wrapper.find('[data-testid="storage-os-bluefin"] [data-release="26.09.640"]').exists()
    ).toBe(true)
  })

  it('marks an untracked OS and its leftover releases', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('not tracked · channel none')
    const coreos = wrapper.find('[data-testid="storage-os-coreos"]')
    const row = coreos.find('[data-release="44.20260829.3.1"]')
    expect(row.find('td:first-child [data-testid="ignored-badge"]').text()).toBe('ignored')
    expect(row.find('[data-testid="retained"]').text()).toBe('untracked')
    expect(row.find('[data-testid="retained"]').classes()).toContain('text-secondary')
    const link = row.find('[data-link="current"]')
    expect(link.classes()).toContain('link-ignored')
    expect(link.classes()).not.toContain('text-bg-success')
    expect(link.attributes('title')).toBe('links are kept but ignored while the OS is untracked')
    expect(coreos.text()).not.toContain(' - ')
  })

  it('lists assets with kinds and the autopilot state line', async () => {
    const wrapper = mountView()
    await flushPromises()
    const assets = wrapper.find('[data-testid="storage-assets"]')
    expect(assets.find('[data-asset="ipxe.efi"] [data-kind="ipxe"]').exists()).toBe(true)
    expect(assets.find('[data-asset="ipxe.efi"]').text()).toContain('1.10 MiB')
    expect(assets.find('[data-asset="version.txt"] [data-kind="other"]').exists()).toBe(true)
    const line = assets.find('[data-testid="storage-autopilot"]').text()
    expect(line).toContain('Autopilot state 8.00 KiB')
    expect(line).toContain('reports 20.0 KiB')
    expect(line).toContain('config/ 4.00 KiB')
    expect(line).toContain('3 registered hosts')
  })

  it('shows empty states and survives a missing autopilot', async () => {
    const wrapper = mountView({
      storage: {
        dataDir: '/data',
        total: { bytes: GiB },
        free: { bytes: GiB },
        os: {},
        assets: []
      },
      autopilot: { mode: 'off' }
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="storage-os-flatcar"]').text()).toContain(
      'No releases cached'
    )
    expect(wrapper.find('[data-testid="storage-assets"]').text()).toContain('No files')
    expect(wrapper.find('.alert').exists()).toBe(false)
  })

  it('reports a failed /storage and retries', async () => {
    const spy = mockFetch((url) => {
      if (url === '/storage') return jsonResponse({ error: 'walk failed' }, 500)
      return jsonResponse({})
    })
    const wrapper = mount(StorageView, { global: { stubs: { RouterLink: RouterLinkStub } } })
    await flushPromises()
    expect(wrapper.text()).toContain('walk failed')
    expect(spy).toHaveBeenCalledWith('/storage', undefined)
  })
})
