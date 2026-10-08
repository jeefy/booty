import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises as flushMicrotasks, mount } from '@vue/test-utils'
import AutopilotView from '@/views/AutopilotView.vue'
import { flushPromises, jsonResponse, mockFetch, requestBody } from '@/__tests__/helpers'

const RouterLinkStub = {
  props: ['to'],
  template: '<a :href="to"><slot /></a>'
}

const hosts = {
  hosts: {
    'aa:bb:cc:dd:ee:01': { mac: 'aa:bb:cc:dd:ee:01', hostname: 'ehrlitan', os: 'flatcar' },
    'aa:bb:cc:dd:ee:02': { mac: 'aa:bb:cc:dd:ee:02', hostname: 'aren', os: 'bluefin', canary: true },
    'aa:bb:cc:dd:ee:03': { mac: 'aa:bb:cc:dd:ee:03', hostname: 'gredfallan', os: 'flatcar' },
    'aa:bb:cc:dd:ee:04': { mac: 'aa:bb:cc:dd:ee:04', hostname: 'kalam', os: 'flatcar' },
    'aa:bb:cc:dd:ee:05': { mac: 'aa:bb:cc:dd:ee:05', hostname: 'brys', os: 'bluefin' },
    'aa:bb:cc:dd:ee:06': { mac: 'aa:bb:cc:dd:ee:06', hostname: 'cuttle', os: 'bluefin' },
    'aa:bb:cc:dd:ee:07': {
      mac: 'aa:bb:cc:dd:ee:07',
      hostname: 'dujek',
      os: 'bluefin',
      mode: 'installed'
    }
  },
  unknownHosts: {}
}

const guard = {
  mode: 'guard',
  actuator: 'kured',
  dryRun: false,
  healthWindow: '15m0s',
  retryAfter: '1h0m0s',
  cluster: { reachable: true, kured: true, nodes: 6, apiServer: 'https://cp:6443' },
  os: {
    flatcar: {
      fleetTarget: '4757.2.0',
      current: '4800.0.0',
      lastGood: '4757.2.0',
      held: true,
      releases: [
        {
          os: 'flatcar',
          version: '4800.0.0',
          state: 'quarantined',
          since: '2026-09-28T12:00:00Z',
          attempts: 3,
          class: 'failed-units',
          report: 'flatcar-4800.0.0'
        },
        { os: 'flatcar', version: '4757.2.0', state: 'good', since: '2026-09-20T12:00:00Z' },
        {
          os: 'flatcar',
          version: '4700.0.0',
          state: 'good',
          since: '2026-09-01T12:00:00Z',
          cached: false
        }
      ]
    },
    bluefin: {
      fleetTarget: '26.09.673',
      current: '26.09.673',
      lastGood: '26.09.673',
      held: false,
      releases: [
        { os: 'bluefin', version: '26.09.673', state: 'good', since: '2026-09-27T00:00:00Z' }
      ]
    },
    coreos: { fleetTarget: '', current: '', lastGood: '', held: false, releases: [] }
  },
  hosts: [
    {
      mac: 'aa:bb:cc:dd:ee:01',
      os: 'flatcar',
      healthyOn: '4757.2.0',
      episode: {
        os: 'flatcar',
        release: '4800.0.0',
        target: '4757.2.0',
        attempt: 3,
        state: 'rolled-back',
        class: 'failed-units',
        since: '2026-09-28T12:30:00Z',
        rollingBack: true,
        done: true,
        note: 'node Ready, workloads healthy',
        signals: { timeline: ['t+0s kernel fetched'] },
        attempts: []
      }
    },
    { mac: 'aa:bb:cc:dd:ee:02', os: 'bluefin', healthyOn: '26.09.673', episode: null },
    {
      mac: 'aa:bb:cc:dd:ee:03',
      os: 'flatcar',
      healthyOn: '4757.2.0',
      pinned: true,
      episode: {
        os: 'flatcar',
        release: '4800.0.0',
        target: '4800.0.0',
        attempt: 2,
        state: 'retrying',
        class: 'workloads-unhealthy',
        since: '2026-09-28T12:40:00Z',
        note: 'waiting for the host to reboot (api)',
        signals: {},
        attempts: []
      }
    },
    {
      mac: 'aa:bb:cc:dd:ee:04',
      os: 'flatcar',
      healthyOn: '4757.2.0',
      episode: {
        os: 'flatcar',
        release: '4800.0.0',
        target: '4800.0.0',
        attempt: 1,
        state: 'gating',
        since: '2026-09-28T12:45:00Z',
        note: 'waiting for the health report',
        signals: {},
        attempts: []
      }
    }
  ],
  events: [
    {
      at: '2026-09-28T12:00:00Z',
      kind: 'episode',
      os: 'flatcar',
      release: '4800.0.0',
      mac: 'aa:bb:cc:dd:ee:01',
      text: 'attempt 1 into 4800.0.0 failed: failed-units'
    },
    {
      at: '2026-09-28T12:31:00Z',
      kind: 'alert',
      os: 'flatcar',
      release: '4800.0.0',
      text: 'NEEDS HANDS: something'
    }
  ],
  reports: [
    {
      key: 'flatcar-4800.0.0',
      os: 'flatcar',
      version: '4800.0.0',
      lastGood: '4757.2.0',
      draft: true,
      createdAt: '2026-09-28T12:30:00Z',
      updatedAt: '2026-09-28T13:00:00Z',
      class: 'failed-units',
      attempts: 3,
      rollbackResult: 'lastGood 4757.2.0 healthy on the same hardware',
      path: '/autopilot/reports/flatcar-4800.0.0.md'
    },
    {
      key: 'bluefin-26.09.700',
      os: 'bluefin',
      version: '26.09.700',
      lastGood: '26.09.673',
      draft: false,
      createdAt: '2026-09-27T12:30:00Z',
      updatedAt: '2026-09-27T14:00:00Z',
      class: 'boot-loop',
      attempts: 4,
      rollbackResult: 'retry failed with boot-loop',
      path: '/autopilot/reports/bluefin-26.09.700.md',
      postedURL: 'https://github.com/jeefy/booty-autopilot-scratch/issues/12',
      postedAt: '2026-09-27T14:00:05Z',
      postAction: 'created'
    },
    {
      key: 'bluefin-26.09.701',
      os: 'bluefin',
      version: '26.09.701',
      lastGood: '26.09.673',
      draft: false,
      createdAt: '2026-09-27T15:30:00Z',
      class: 'no-ignition',
      attempts: 4,
      path: '/autopilot/reports/bluefin-26.09.701.md',
      postError: 'POST /repos/x/y/issues: HTTP 403: rate limit exceeded'
    }
  ],
  held: ['flatcar'],
  quarantined: 1,
  needsHands: 0
}

const reportMarkdown =
  '# Flatcar 4800.0.0: failed-units (autopilot report)\n\n<!-- booty-autopilot: flatcar 4800.0.0 abc -->\n\n| Boot path | `uefi-pxe` |\n'

const NOW = new Date('2026-10-07T12:00:00Z')

const bluefinHosts = [
  { mac: 'aa:bb:cc:dd:ee:02', os: 'bluefin', healthyOn: '26.10.880', episode: null },
  { mac: 'aa:bb:cc:dd:ee:05', os: 'bluefin', healthyOn: '26.10.880', pinned: true, episode: null },
  { mac: 'aa:bb:cc:dd:ee:06', os: 'bluefin', healthyOn: '26.09.673', pinned: true, episode: null },
  { mac: 'aa:bb:cc:dd:ee:07', os: 'bluefin', healthyOn: '', episode: null }
]

const pacedBluefin = {
  fleetTarget: '26.09.673',
  current: '26.10.880',
  lastGood: '26.09.673',
  held: true,
  canaries: 1,
  wave: { release: '26.10.880', startedAt: '2026-10-07T09:00:00Z', outcome: 'rolling' },
  releases: [
    {
      os: 'bluefin',
      version: '26.10.880',
      state: 'rolling',
      since: '2026-10-06T18:00:00Z',
      firstHealthyAt: '2026-10-06T20:00:00Z',
      soaked: true
    },
    {
      os: 'bluefin',
      version: '26.10.870',
      state: 'rolling',
      since: '2026-10-06T22:00:00Z',
      firstHealthyAt: '2026-10-06T23:00:00Z',
      soaked: false
    },
    { os: 'bluefin', version: '26.10.865', state: 'rolling', since: '2026-10-07T11:00:00Z' },
    { os: 'bluefin', version: '26.10.860', state: 'skipped', since: '2026-10-05T12:00:00Z' },
    { os: 'bluefin', version: '26.09.673', state: 'good', since: '2026-09-27T00:00:00Z' }
  ]
}

const paced = {
  ...guard,
  soak: '24h0m0s',
  cooldown: '48h0m0s',
  os: { ...guard.os, bluefin: pacedBluefin },
  hosts: [...guard.hosts.filter((h) => h.os !== 'bluefin'), ...bluefinHosts]
}

function withWave(wave: object | null, extra: object = {}) {
  return {
    ...paced,
    os: { ...paced.os, bluefin: { ...pacedBluefin, ...extra, wave } }
  }
}

const powerEvents = {
  hosts: {},
  events: [
    {
      at: '2026-09-28T12:05:00Z',
      kind: 'power',
      mac: 'aa:bb:cc:dd:ee:02',
      text: 'reboot requested through the ssh actuator (drain: true, force: false)'
    },
    {
      at: '2026-09-28T12:00:00Z',
      kind: 'power',
      mac: 'aa:bb:cc:dd:ee:01',
      text: 'duplicate of the autopilot ring'
    }
  ],
  capabilities: { wol: true, actuator: 'ssh' },
  summary: { up: 2, off: 0, unreachable: 0, inFlight: 1 }
}

function mountView(
  status: unknown,
  onClear?: (url: string, init?: RequestInit) => Response,
  power: unknown = { ...powerEvents, events: [] }
) {
  const spy = mockFetch((url, init) => {
    if (url === '/autopilot') return jsonResponse(status)
    if (url === '/booty.json') return jsonResponse(hosts)
    if (url === '/power') return jsonResponse(power)
    if (url === '/autopilot/reports/flatcar-4800.0.0.md') {
      return new Response(reportMarkdown, {
        status: 200,
        headers: { 'Content-Type': 'text/markdown; charset=utf-8' }
      })
    }
    if (url.startsWith('/autopilot/reports/')) return jsonResponse({ error: 'no such report' }, 404)
    if (url.startsWith('/autopilot/') && init?.method === 'POST') {
      return onClear ? onClear(url, init) : jsonResponse({ status: 'ok' })
    }
    return jsonResponse({ error: `unexpected ${url}` }, 500)
  })
  return { wrapper: mount(AutopilotView, { global: { stubs: { RouterLink: RouterLinkStub } } }), spy }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('AutopilotView', () => {
  it('shows the off state without tables', async () => {
    const { wrapper } = mountView({ mode: 'off', actuator: 'none', dryRun: true })
    await flushPromises()
    expect(wrapper.find('[data-testid="autopilot-summary"]').text()).toContain('off')
    expect(wrapper.text()).toContain('Autopilot is off')
    expect(wrapper.find('[data-testid="autopilot-os-table"]').exists()).toBe(false)
  })

  it('renders fleet targets with hold, releases with state and since, hosts, reports and the timeline', async () => {
    const { wrapper } = mountView(guard)
    await flushPromises()

    const summary = wrapper.find('[data-testid="autopilot-summary"]')
    expect(summary.text()).toContain('guard')
    expect(summary.text()).toContain('kured')
    expect(summary.text()).toContain('6 nodes')
    expect(summary.find('[data-testid="autopilot-attention"]').text()).toContain('1 quarantined')
    expect(summary.text()).toContain('held for flatcar')

    const flatcar = wrapper.find('[data-testid="autopilot-os-table"] tr[data-os="flatcar"]')
    expect(flatcar.exists()).toBe(true)
    expect(flatcar.find('[data-testid="autopilot-held"]').exists()).toBe(true)
    expect(flatcar.text()).toContain('4757.2.0')
    expect(flatcar.text()).toContain('4800.0.0')
    const quarantined = flatcar.find('[data-release="4800.0.0"]')
    expect(quarantined.find('[data-state="quarantined"]').text()).toBe('Quarantined')
    expect(quarantined.text()).toContain('since')
    expect(quarantined.text()).toContain('3 attempts')
    expect(quarantined.text()).toContain('failed-units')
    expect(quarantined.find('[data-action="clear"]').exists()).toBe(true)
    await flatcar.find('[data-action="toggle-good"]').trigger('click')
    expect(flatcar.find('[data-release="4757.2.0"] [data-action="clear"]').exists()).toBe(false)
    expect(flatcar.find('[data-release="4757.2.0"] [data-testid="pruned-hint"]').exists()).toBe(
      false
    )
    const pruned = flatcar.find('[data-release="4700.0.0"]')
    expect(pruned.classes()).toContain('muted')
    expect(pruned.find('[data-testid="pruned-hint"]').attributes('href')).toBe('/storage')
    expect(quarantined.classes()).not.toContain('muted')
    expect(wrapper.find('[data-testid="storage-link"]').attributes('href')).toBe('/storage')
    expect(wrapper.find('[data-testid="autopilot-os-table"] tr[data-os="coreos"]').exists()).toBe(
      false
    )

    const row = wrapper.find(
      '[data-testid="autopilot-hosts-table"] tr[data-mac="aa:bb:cc:dd:ee:01"]'
    )
    expect(row.text()).toContain('ehrlitan')
    expect(row.find('[data-state="rolled-back"]').text()).toBe('Rolled back')
    expect(row.text()).toContain('4800.0.0')
    expect(row.text()).toContain('→ 4757.2.0')
    expect(row.text()).toContain('3')
    expect(row.text()).toContain('failed-units')
    const idle = wrapper.find(
      '[data-testid="autopilot-hosts-table"] tr[data-mac="aa:bb:cc:dd:ee:02"]'
    )
    expect(idle.find('[data-state="idle"]').text()).toBe('Idle')

    expect(wrapper.find('[data-testid="autopilot-reports-table"]').text()).toContain(
      'same hardware'
    )

    const timeline = wrapper.findAll('[data-testid="autopilot-timeline"] li')
    expect(timeline).toHaveLength(2)
    expect(timeline[0]!.attributes('data-kind')).toBe('alert')
    expect(timeline[0]!.find('.text').classes()).toContain('text-danger')
    expect(timeline[1]!.text()).toContain('ehrlitan')
    expect(timeline[1]!.text()).toContain('attempt 1 into 4800.0.0 failed')
  })

  it('POSTs the clear endpoint and reloads', async () => {
    const { wrapper, spy } = mountView(guard)
    await flushPromises()
    await wrapper.find('[data-release="4800.0.0"] [data-action="clear"]').trigger('click')
    await flushPromises()
    const post = spy.mock.calls.find(([, init]) => init?.method === 'POST')
    expect(post?.[0]).toBe('/autopilot/flatcar/release/4800.0.0/clear')
    expect(requestBody(post?.[1])).toEqual({})
    expect(spy.mock.calls.filter(([url]) => url === '/autopilot')).toHaveLength(2)
    expect(wrapper.find('[data-testid="clear-error"]').exists()).toBe(false)
  })

  it('shows the server error when clear is refused', async () => {
    const { wrapper } = mountView(guard, () =>
      jsonResponse({ error: 'flatcar 4800.0.0 is rolling, nothing to clear' }, 409)
    )
    await flushPromises()
    await wrapper.find('[data-release="4800.0.0"] [data-action="clear"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="clear-error"]').text()).toContain('nothing to clear')
  })

  it('offers Clear on needs-hands, retrying and rolled-back hosts only', async () => {
    const needsHands = {
      ...guard,
      hosts: [
        {
          mac: 'aa:bb:cc:dd:ee:02',
          os: 'bluefin',
          healthyOn: '',
          episode: {
            os: 'bluefin',
            release: '26.09.700',
            target: '26.09.673',
            attempt: 3,
            state: 'needs-hands',
            class: 'hung',
            since: '2026-09-28T13:00:00Z',
            done: true,
            signals: {},
            attempts: []
          }
        },
        ...guard.hosts
      ]
    }
    const { wrapper } = mountView(needsHands)
    await flushPromises()
    const table = wrapper.find('[data-testid="autopilot-hosts-table"]')
    const button = (mac: string) => table.find(`tr[data-mac="${mac}"] [data-action="clear-host"]`)
    expect(button('aa:bb:cc:dd:ee:02').exists()).toBe(true)
    expect(button('aa:bb:cc:dd:ee:03').exists()).toBe(true)
    expect(button('aa:bb:cc:dd:ee:01').exists()).toBe(true)
    expect(button('aa:bb:cc:dd:ee:04').exists()).toBe(false)
    expect(button('aa:bb:cc:dd:ee:03').attributes('title')).toContain('second attempt')
  })

  it('asks for confirmation, then POSTs the host clear endpoint and reloads', async () => {
    const { wrapper, spy } = mountView(guard)
    await flushPromises()
    const row = wrapper.find('[data-testid="autopilot-hosts-table"] tr[data-mac="aa:bb:cc:dd:ee:03"]')
    await row.find('[data-action="clear-host"]').trigger('click')
    expect(spy.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
    const confirm = row.find('[data-testid="clear-host-confirm"]')
    expect(confirm.text()).toContain('gredfallan')
    expect(confirm.text()).toContain('second attempt')

    await confirm.find('[data-action="clear-host-cancel"]').trigger('click')
    expect(row.find('[data-testid="clear-host-confirm"]').exists()).toBe(false)
    expect(row.find('[data-action="clear-host"]').exists()).toBe(true)
    expect(spy.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)

    await row.find('[data-action="clear-host"]').trigger('click')
    await row.find('[data-action="clear-host-confirm"]').trigger('click')
    await flushPromises()
    const post = spy.mock.calls.find(([, init]) => init?.method === 'POST')
    expect(post?.[0]).toBe('/autopilot/host/aa%3Abb%3Acc%3Add%3Aee%3A03/clear')
    expect(requestBody(post?.[1])).toEqual({})
    expect(spy.mock.calls.filter(([url]) => url === '/autopilot')).toHaveLength(2)
    expect(wrapper.find('[data-testid="clear-host-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="clear-host-confirm"]').exists()).toBe(false)
  })

  it('shows the server error on the host row when the host clear is refused', async () => {
    const { wrapper } = mountView(guard, (url) =>
      url.startsWith('/autopilot/host/')
        ? jsonResponse({ error: 'host has no episode to clear' }, 409)
        : jsonResponse({ status: 'ok' })
    )
    await flushPromises()
    const row = wrapper.find('[data-testid="autopilot-hosts-table"] tr[data-mac="aa:bb:cc:dd:ee:03"]')
    await row.find('[data-action="clear-host"]').trigger('click')
    await row.find('[data-action="clear-host-confirm"]').trigger('click')
    await flushPromises()
    expect(row.find('[data-testid="clear-host-error"]').text()).toContain('no episode to clear')
    expect(wrapper.find('[data-testid="clear-error"]').exists()).toBe(false)
  })

  it('lists reports with draft/final badges, the Markdown link, the issue link and post failures', async () => {
    const { wrapper } = mountView(guard)
    await flushPromises()
    const table = wrapper.find('[data-testid="autopilot-reports-table"]')
    expect(table.findAll('tr[data-report]')).toHaveLength(3)

    const draft = table.find('tr[data-report="flatcar-4800.0.0"]')
    expect(draft.text()).toContain('draft')
    expect(draft.find('[data-action="download-report"]').attributes('href')).toBe(
      '/autopilot/reports/flatcar-4800.0.0.md'
    )
    expect(draft.find('[data-action="issue-link"]').exists()).toBe(false)

    const posted = table.find('tr[data-report="bluefin-26.09.700"]')
    expect(posted.text()).toContain('final')
    expect(posted.text()).not.toContain('draft')
    const issue = posted.find('[data-action="issue-link"]')
    expect(issue.attributes('href')).toBe(
      'https://github.com/jeefy/booty-autopilot-scratch/issues/12'
    )
    expect(issue.attributes('target')).toBe('_blank')
    expect(issue.text()).toBe('GitHub #12')

    const failed = table.find('tr[data-report="bluefin-26.09.701"]')
    expect(failed.find('[data-action="issue-link"]').exists()).toBe(false)
    expect(failed.find('[data-testid="report-post-error"]').attributes('title')).toContain('403')
  })

  it('fetches and shows the redacted Markdown inline, and hides it again', async () => {
    const { wrapper, spy } = mountView(guard)
    await flushPromises()
    const row = wrapper.find('tr[data-report="flatcar-4800.0.0"]')
    await row.find('[data-action="view-report"]').trigger('click')
    await flushPromises()
    expect(spy.mock.calls.some(([url]) => url === '/autopilot/reports/flatcar-4800.0.0.md')).toBe(
      true
    )
    const body = wrapper.find('tr[data-report-body="flatcar-4800.0.0"]')
    expect(body.exists()).toBe(true)
    expect(body.find('[data-testid="report-preview"]').text()).toContain(
      'booty-autopilot: flatcar 4800.0.0 abc'
    )
    expect(row.find('[data-action="view-report"]').text()).toBe('Hide')

    await row.find('[data-action="view-report"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('tr[data-report-body="flatcar-4800.0.0"]').exists()).toBe(false)

    const missing = wrapper.find('tr[data-report="bluefin-26.09.701"]')
    await missing.find('[data-action="view-report"]').trigger('click')
    await flushPromises()
    expect(
      wrapper.find('tr[data-report-body="bluefin-26.09.701"] [data-testid="report-error"]').text()
    ).toContain('no such report')
  })

  it('merges the power tracker\'s events into the timeline with a power badge, without duplicates', async () => {
    const withDuplicate = {
      ...guard,
      events: [
        ...guard.events,
        { at: '2026-09-28T12:00:00Z', kind: 'power', mac: 'aa:bb:cc:dd:ee:01', text: 'duplicate of the autopilot ring' }
      ]
    }
    const { wrapper } = mountView(withDuplicate, undefined, powerEvents)
    await flushPromises()
    const items = wrapper.findAll('[data-testid="autopilot-timeline"] li[data-kind="power"]')
    expect(items).toHaveLength(2)
    expect(items[0]!.text()).toContain('reboot requested through the ssh actuator')
    expect(items[0]!.text()).toContain('aren')
    expect(items[0]!.find('[data-testid="event-kind"]').classes()).toContain('text-bg-info')
    expect(wrapper.findAll('[data-testid="autopilot-timeline"] li')[1]!.attributes('data-kind')).toBe('power')
  })

  it('shows power events even when the autopilot is off', async () => {
    const { wrapper } = mountView({ mode: 'off', actuator: 'none', dryRun: true }, undefined, powerEvents)
    await flushPromises()
    const items = wrapper.findAll('[data-testid="autopilot-timeline"] li')
    expect(items).toHaveLength(2)
    expect(items.every((li) => li.attributes('data-kind') === 'power')).toBe(true)
  })

  it('folds good releases behind a toggle and links the rest of the history to Storage', async () => {
    const withHistory = {
      ...guard,
      os: { ...guard.os, flatcar: { ...guard.os.flatcar, releaseCount: 30 } }
    }
    const { wrapper } = mountView(withHistory)
    await flushPromises()

    const flatcar = wrapper.find('[data-testid="autopilot-os-table"] tr[data-os="flatcar"]')
    expect(flatcar.findAll('[data-release]').map((r) => r.attributes('data-release'))).toEqual([
      '4800.0.0'
    ])
    expect(flatcar.find('[data-release="4800.0.0"] [data-action="clear"]').exists()).toBe(true)
    const toggle = flatcar.find('[data-action="toggle-good"]')
    expect(toggle.text()).toBe('Show 2 good releases')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    const history = flatcar.find('[data-testid="history-link"]')
    expect(history.text()).toBe('27 more in history → Storage')
    expect(history.attributes('href')).toBe('/storage')

    await toggle.trigger('click')
    expect(flatcar.findAll('[data-release]').map((r) => r.attributes('data-release'))).toEqual([
      '4800.0.0',
      '4757.2.0',
      '4700.0.0'
    ])
    expect(flatcar.find('[data-action="toggle-good"]').text()).toBe('Hide 2 good releases')
    expect(flatcar.find('[data-action="toggle-good"]').attributes('aria-expanded')).toBe('true')
    expect(flatcar.find('[data-release="4700.0.0"]').classes()).toContain('muted')
    await flatcar.find('[data-action="toggle-good"]').trigger('click')
    expect(flatcar.findAll('[data-release]')).toHaveLength(1)

    const bluefin = wrapper.find('[data-testid="autopilot-os-table"] tr[data-os="bluefin"]')
    expect(bluefin.findAll('[data-release]')).toHaveLength(0)
    expect(bluefin.find('[data-action="toggle-good"]').text()).toBe('Show 1 good release')
    expect(bluefin.find('[data-testid="history-link"]').exists()).toBe(false)
  })

  it('shows 50 events, then 100, then fetches the whole ring on demand', async () => {
    const event = (i: number) => ({
      at: `2026-09-28T${String(Math.floor(i / 60)).padStart(2, '0')}:${String(i % 60).padStart(2, '0')}:00Z`,
      kind: 'episode',
      os: 'bluefin',
      release: '26.09.673',
      mac: 'aa:bb:cc:dd:ee:02',
      text: `event ${i}`
    })
    const ring = Array.from({ length: 180 }, (_, i) => event(i))
    const page = { ...guard, events: ring.slice(80), eventCount: 180 }
    const spy = mockFetch((url) => {
      if (url === '/autopilot') return jsonResponse(page)
      if (url === '/autopilot?events=all') return jsonResponse({ ...page, events: ring })
      if (url === '/booty.json') return jsonResponse(hosts)
      if (url === '/power') return jsonResponse({ ...powerEvents, events: [] })
      return jsonResponse({ error: `unexpected ${url}` }, 500)
    })
    const wrapper = mount(AutopilotView, { global: { stubs: { RouterLink: RouterLinkStub } } })
    await flushPromises()

    const items = () => wrapper.findAll('[data-testid="autopilot-timeline"] li')
    expect(items()).toHaveLength(50)
    expect(items()[0]!.text()).toContain('event 179')
    expect(wrapper.find('[data-testid="events-shown"]').text()).toBe('50 of 180 events')

    await wrapper.find('[data-action="show-more-events"]').trigger('click')
    expect(items()).toHaveLength(100)
    expect(wrapper.find('[data-testid="events-shown"]').text()).toBe('100 of 180 events')
    expect(spy.mock.calls.some(([url]) => url === '/autopilot?events=all')).toBe(false)

    await wrapper.find('[data-action="show-more-events"]').trigger('click')
    await flushPromises()
    expect(spy.mock.calls.some(([url]) => url === '/autopilot?events=all')).toBe(true)
    expect(items()).toHaveLength(180)
    expect(items()[179]!.text()).toContain('event 0')
    expect(wrapper.find('[data-action="show-more-events"]').exists()).toBe(false)
  })

  it('polls every 30s and leaves the state alone while the revision is unchanged', async () => {
    vi.useFakeTimers()
    try {
      let current = { ...guard, revision: 7 }
      const spy = mockFetch((url) => {
        if (url === '/autopilot') return jsonResponse(current)
        if (url === '/booty.json') return jsonResponse(hosts)
        if (url === '/power') return jsonResponse({ ...powerEvents, events: [] })
        return jsonResponse({ error: `unexpected ${url}` }, 500)
      })
      const wrapper = mount(AutopilotView, { global: { stubs: { RouterLink: RouterLinkStub } } })
      await flushMicrotasks()
      const vm = wrapper.vm as unknown as { status: object; hostnames: object }
      const before = vm.status
      const names = vm.hostnames
      expect(spy.mock.calls.filter(([url]) => url === '/autopilot')).toHaveLength(1)

      await vi.advanceTimersByTimeAsync(30_000)
      await flushMicrotasks()
      expect(spy.mock.calls.filter(([url]) => url === '/autopilot')).toHaveLength(2)
      expect(vm.status).toBe(before)
      expect(vm.hostnames).toBe(names)
      expect(wrapper.find('.loading, .spinner-border').exists()).toBe(false)

      current = {
        ...guard,
        revision: 8,
        events: [
          ...guard.events,
          {
            at: '2026-09-28T13:00:00Z',
            kind: 'release',
            os: 'flatcar',
            release: '4800.0.0',
            text: 'release cleared by the operator; the fleet may try it again'
          }
        ]
      }
      await vi.advanceTimersByTimeAsync(30_000)
      await flushMicrotasks()
      expect(vm.status).not.toBe(before)
      expect(wrapper.findAll('[data-testid="autopilot-timeline"] li')).toHaveLength(3)
      expect(wrapper.find('[data-testid="autopilot-timeline"] li').text()).toContain(
        'cleared by the operator'
      )

      wrapper.unmount()
      await vi.advanceTimersByTimeAsync(60_000)
      expect(spy.mock.calls.filter(([url]) => url === '/autopilot')).toHaveLength(3)
    } finally {
      vi.useRealTimers()
    }
  })

  describe('soak and cooldown', () => {
    const bluefinRow = (wrapper: ReturnType<typeof mountView>['wrapper']) =>
      wrapper.find('[data-testid="autopilot-os-table"] tr[data-os="bluefin"]')

    beforeEach(() => {
      vi.useFakeTimers({ toFake: ['Date'] })
      vi.setSystemTime(NOW)
    })

    afterEach(() => {
      vi.useRealTimers()
    })

    it('shows soak and cooldown next to the health window, and omits them when unset', async () => {
      const { wrapper } = mountView(paced)
      await flushPromises()
      const clocks = wrapper.find('[data-testid="autopilot-clocks"]')
      expect(clocks.text()).toBe('health window 15m · retry after 1h · soak 24h · cooldown 48h')

      vi.unstubAllGlobals()
      const plain = mountView(guard)
      await flushPromises()
      expect(plain.wrapper.find('[data-testid="autopilot-clocks"]').text()).toBe(
        'health window 15m · retry after 1h'
      )
    })

    it('shows the rolling wave with the non-canary host count from /booty.json', async () => {
      const { wrapper } = mountView(paced)
      await flushPromises()
      const wave = bluefinRow(wrapper).find('[data-wave]')
      expect(wave.attributes('data-wave')).toBe('rolling')
      expect(wave.text()).toBe('Wave wave into 26.10.880 · 1/2 hosts · started 3h ago')
      expect(
        wrapper.find('[data-testid="autopilot-os-table"] tr[data-os="flatcar"] [data-wave]').exists()
      ).toBe(false)
    })

    it('omits the host count when /booty.json is unavailable', async () => {
      mockFetch((url) => {
        if (url === '/autopilot') return jsonResponse(paced)
        if (url === '/power') return jsonResponse({ ...powerEvents, events: [] })
        return jsonResponse({ error: `unexpected ${url}` }, 500)
      })
      const wrapper = mount(AutopilotView, { global: { stubs: { RouterLink: RouterLinkStub } } })
      await flushPromises()
      expect(bluefinRow(wrapper).find('[data-wave]').text()).toBe(
        'Wave wave into 26.10.880 · started 3h ago'
      )
    })

    it('shows the last wave and the cooldown until the next one', async () => {
      const { wrapper } = mountView(
        withWave(
          {
            release: '26.10.880',
            startedAt: '2026-10-06T16:00:00Z',
            endedAt: '2026-10-06T20:00:00Z',
            outcome: 'done'
          },
          { nextWaveAt: '2026-10-08T16:00:00Z' }
        )
      )
      await flushPromises()
      const wave = bluefinRow(wrapper).find('[data-wave]')
      expect(wave.attributes('data-wave')).toBe('done')
      expect(wave.text()).toBe('Wave last wave done 16h ago · next wave in 1d')
      expect(wave.attributes('title')).not.toBe('')
    })

    it('shows a done wave without a pending cooldown, and a cooldown without any wave', async () => {
      const done = mountView(
        withWave({
          release: '26.10.880',
          startedAt: '2026-10-06T16:00:00Z',
          endedAt: '2026-10-06T20:00:00Z',
          outcome: 'done'
        })
      )
      await flushPromises()
      expect(bluefinRow(done.wrapper).find('[data-wave]').text()).toBe(
        'Wave last wave done 16h ago'
      )

      vi.unstubAllGlobals()
      const pending = mountView(withWave(null, { nextWaveAt: '2026-10-07T14:00:00Z' }))
      await flushPromises()
      const wave = bluefinRow(pending.wrapper).find('[data-wave]')
      expect(wave.attributes('data-wave')).toBe('pending')
      expect(wave.text()).toBe('Wave next wave in 2h')

      vi.unstubAllGlobals()
      const none = mountView(withWave(null))
      await flushPromises()
      expect(bluefinRow(none.wrapper).find('[data-wave]').exists()).toBe(false)
    })

    it('names the verdict of an aborted wave, falling back to "bad"', async () => {
      const quarantined = pacedBluefin.releases.map((r) =>
        r.version === '26.10.880' ? { ...r, state: 'quarantined' } : r
      )
      const { wrapper } = mountView(
        withWave(
          {
            release: '26.10.880',
            startedAt: '2026-10-07T09:00:00Z',
            endedAt: '2026-10-07T11:00:00Z',
            outcome: 'aborted'
          },
          { releases: quarantined }
        )
      )
      await flushPromises()
      const wave = bluefinRow(wrapper).find('[data-wave]')
      expect(wave.attributes('data-wave')).toBe('aborted')
      expect(wave.text()).toBe('Wave wave aborted: 26.10.880 quarantined')

      vi.unstubAllGlobals()
      const unknown = mountView(
        withWave({
          release: '26.10.999',
          startedAt: '2026-10-07T09:00:00Z',
          endedAt: '2026-10-07T11:00:00Z',
          outcome: 'aborted'
        })
      )
      await flushPromises()
      expect(bluefinRow(unknown.wrapper).find('[data-wave]').text()).toBe(
        'Wave wave aborted: 26.10.999 bad'
      )
    })

    it('badges rolling releases as soaking or soaked, and nothing before a host is healthy', async () => {
      const { wrapper } = mountView(paced)
      await flushPromises()
      const row = bluefinRow(wrapper)
      const soaked = row.find('[data-release="26.10.880"] [data-soak]')
      expect(soaked.attributes('data-soak')).toBe('soaked')
      expect(soaked.text()).toBe('soaked')
      expect(soaked.classes()).toContain('text-bg-success')
      const soaking = row.find('[data-release="26.10.870"] [data-soak]')
      expect(soaking.attributes('data-soak')).toBe('soaking')
      expect(soaking.text()).toBe('soaking 13h / 24h')
      expect(soaking.classes()).toContain('text-bg-light')
      expect(row.find('[data-release="26.10.865"] [data-soak]').exists()).toBe(false)
    })

    it('shows no soak badge when no soak is configured', async () => {
      const { wrapper } = mountView({ ...paced, soak: undefined })
      await flushPromises()
      expect(bluefinRow(wrapper).findAll('[data-soak]')).toHaveLength(0)
    })

    it('folds skipped releases in with the good ones and renders them alike', async () => {
      const { wrapper } = mountView(paced)
      await flushPromises()
      const row = bluefinRow(wrapper)
      expect(row.findAll('[data-release]').map((r) => r.attributes('data-release'))).toEqual([
        '26.10.880',
        '26.10.870',
        '26.10.865'
      ])
      const toggle = row.find('[data-action="toggle-good"]')
      expect(toggle.text()).toBe('Show 2 good releases')
      await toggle.trigger('click')
      expect(row.findAll('[data-release]').map((r) => r.attributes('data-release'))).toEqual([
        '26.10.880',
        '26.10.870',
        '26.10.865',
        '26.10.860',
        '26.09.673'
      ])
      const skipped = row.find('[data-release="26.10.860"] [data-state="skipped"]')
      expect(skipped.text()).toBe('Skipped')
      expect(skipped.classes()).toEqual(
        row.find('[data-release="26.09.673"] [data-state="good"]').classes()
      )
      expect(row.find('[data-release="26.10.860"] [data-action="clear"]').exists()).toBe(false)
      expect(row.find('[data-release="26.10.860"] [data-soak]').exists()).toBe(false)
    })
  })
})
