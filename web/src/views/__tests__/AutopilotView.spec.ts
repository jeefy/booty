import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AutopilotView from '@/views/AutopilotView.vue'
import { flushPromises, jsonResponse, mockFetch, requestBody } from '@/__tests__/helpers'

const hosts = {
  hosts: {
    'aa:bb:cc:dd:ee:01': { mac: 'aa:bb:cc:dd:ee:01', hostname: 'ehrlitan', os: 'flatcar' },
    'aa:bb:cc:dd:ee:02': { mac: 'aa:bb:cc:dd:ee:02', hostname: 'aren', os: 'bluefin' }
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
        { os: 'flatcar', version: '4757.2.0', state: 'good', since: '2026-09-20T12:00:00Z' }
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
    { mac: 'aa:bb:cc:dd:ee:02', os: 'bluefin', healthyOn: '26.09.673', episode: null }
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

function mountView(status: unknown, onClear?: (url: string, init?: RequestInit) => Response) {
  const spy = mockFetch((url, init) => {
    if (url === '/autopilot') return jsonResponse(status)
    if (url === '/booty.json') return jsonResponse(hosts)
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
  return { wrapper: mount(AutopilotView), spy }
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
    expect(flatcar.find('[data-release="4757.2.0"] [data-action="clear"]').exists()).toBe(false)
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
})
