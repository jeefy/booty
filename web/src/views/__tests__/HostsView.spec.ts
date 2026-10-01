import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import HostsView from '@/views/HostsView.vue'
import type { BootyData, Host } from '@/types'
import { jsonResponse, mockFetch, requestBody } from '@/__tests__/helpers'

const DIGEST = 'sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'

const hostA: Host = {
  mac: 'aa:bb:cc:dd:ee:01',
  hostname: 'alpha',
  ip: '10.0.0.1',
  booted: '2026-09-24T10:00:00Z',
  ignitionFile: 'alpha.yaml',
  os: 'flatcar',
  ostreeImage: '',
  installDisk: '',
  doInstall: false,
  running: '3815.2.0',
  lastCheck: '2026-09-24T11:30:00Z',
  rebootPending: false
}

const hostB: Host = {
  mac: 'aa:bb:cc:dd:ee:02',
  hostname: 'bravo',
  ip: '10.0.0.2',
  booted: '',
  ignitionFile: '',
  os: 'bluefin',
  ostreeImage: 'ghcr.io/projectbluefin/bluefin:stable',
  installDisk: '/dev/sda',
  doInstall: true,
  running: `ghcr.io/projectbluefin/bluefin@${DIGEST}`,
  lastCheck: '2026-09-24T11:45:00Z',
  rebootPending: true
}

const hostC: Host = {
  mac: 'aa:bb:cc:dd:ee:03',
  hostname: 'charlie-booting',
  ip: '',
  booted: '2026-09-24T11:55:00Z',
  os: 'coreos',
  installDisk: '',
  running: '',
  lastCheck: '',
  rebootPending: false
}

const data: BootyData = {
  hosts: { [hostA.mac]: hostA, [hostB.mac]: hostB },
  unknownHosts: {
    'aa:bb:cc:dd:ee:99': {
      mac: 'aa:bb:cc:dd:ee:99',
      ip: '10.0.0.99',
      firstSeen: '2026-09-24T09:00:00Z',
      lastSeen: '2026-09-24T11:50:00Z',
      count: 7
    }
  }
}

type Handlers = Record<string, (init?: RequestInit) => Response>

function mountWithData(payload: unknown = data, overrides: Handlers = {}) {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  const handlers: Handlers = {
    '/booty.json': () => jsonResponse(payload),
    ...overrides
  }
  mockFetch((url, init) => {
    calls.push({ url, init })
    const handler = handlers[url]
    if (!handler) return jsonResponse({ error: `unexpected ${url}` }, 500)
    return handler(init)
  })
  const wrapper = mount(HostsView)
  return { wrapper, calls, handlers }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('HostsView', () => {
  it('renders one row per registered host and per unknown host', async () => {
    const { wrapper } = mountWithData()
    expect(wrapper.text()).toContain('Loading hosts')
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="hosts-table"] tbody tr[data-mac]')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('alpha')
    expect(rows[1]!.text()).toContain('bravo')

    const link = rows[0]!.find('a')
    expect(link.attributes('href')).toBe(
      '/ignition.json?mac=aa%3Abb%3Acc%3Add%3Aee%3A01&preview=1&part=merged'
    )
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener noreferrer')

    expect(rows[0]!.text()).toContain('ago')
    expect(rows[1]!.text()).toContain('never')

    const unknownRows = wrapper.findAll('[data-testid="unknown-table"] tbody tr[data-mac]')
    expect(unknownRows).toHaveLength(1)
    expect(unknownRows[0]!.text()).toContain('10.0.0.99')
    expect(unknownRows[0]!.text()).toContain('7')
  })

  it('renders Running, Last check and a status badge for each fleet state', async () => {
    const { wrapper } = mountWithData({
      hosts: { [hostA.mac]: hostA, [hostB.mac]: hostB, [hostC.mac]: hostC },
      unknownHosts: {}
    })
    await flushPromises()

    const alpha = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"]')
    expect(alpha.find('[data-testid="host-status"]').text()).toBe('Up to date')
    expect(alpha.find('[data-testid="host-status"]').classes()).toContain('text-bg-success')
    expect(alpha.find('[data-testid="host-running"] .mono').text()).toBe('3815.2.0')
    expect(alpha.find('[data-testid="host-running"]').attributes('data-reporting')).toBe('stale')
    expect(alpha.find('[data-testid="host-last-check"]').text()).toContain('ago')

    const bravo = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"]')
    expect(bravo.find('[data-testid="host-status"]').text()).toBe('Reboot pending')
    expect(bravo.find('[data-testid="host-status"]').classes()).toContain('text-bg-warning')

    const charlie = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:03"]')
    expect(charlie.find('[data-testid="host-status"]').text()).toBe('Unknown')
    expect(charlie.find('[data-testid="host-status"]').classes()).toContain('text-bg-secondary')
    expect(charlie.find('[data-testid="host-running"]').text()).toBe('—')
    expect(charlie.find('[data-testid="host-last-check"]').text()).toBe('never')
  })

  it('shortens image@digest to the image plus 12 hex chars and keeps the full value in the title', async () => {
    const { wrapper } = mountWithData()
    await flushPromises()

    const cell = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-running"]')
    const [image, digest] = cell.findAll('.mono')
    expect(image!.text()).toBe('ghcr.io/projectbluefin/bluefin')
    expect(image!.attributes('title')).toBe(`ghcr.io/projectbluefin/bluefin@${DIGEST}`)
    expect(digest!.text()).toBe('@0123456789ab')
    expect(digest!.attributes('title')).toBe(DIGEST)
    expect(cell.text()).not.toContain(DIGEST)
  })

  it('links the merged preview from the MAC and exposes user/builtin parts as secondary links', async () => {
    const { wrapper } = mountWithData()
    await flushPromises()

    const row = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"]')
    const base = '/ignition.json?mac=aa%3Abb%3Acc%3Add%3Aee%3A02&preview=1'
    expect(row.find('[data-preview="merged"]').attributes('href')).toBe(`${base}&part=merged`)
    const user = row.find('[data-preview="user"]')
    const builtin = row.find('[data-preview="builtin"]')
    expect(user.attributes('href')).toBe(`${base}&part=user`)
    expect(user.text()).toBe('user config')
    expect(builtin.attributes('href')).toBe(`${base}&part=builtin`)
    expect(builtin.text()).toBe('builtin')
    for (const link of [user, builtin]) {
      expect(link.attributes('target')).toBe('_blank')
      expect(link.attributes('rel')).toBe('noopener noreferrer')
    }

    const unknownLink = wrapper.find('[data-testid="unknown-table"] tr[data-mac] a')
    expect(unknownLink.attributes('href')).toContain('&part=merged')
  })

  it('keeps ignition file, OSTree image and install flag visible in the host cell', async () => {
    const { wrapper } = mountWithData()
    await flushPromises()

    const alpha = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-testid="host-config"]')
    expect(alpha.text()).toContain('alpha.yaml')
    expect(alpha.text()).not.toContain('Install')

    const bravo = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-config"]')
    expect(bravo.text()).toContain('default ignition')
    expect(bravo.text()).toContain('ghcr.io/projectbluefin/bluefin:stable')
    expect(bravo.text()).toContain('Install')
  })

  it('shows the install disk in the host cell only when one is set', async () => {
    const { wrapper } = mountWithData()
    await flushPromises()

    const alpha = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-testid="host-config"]')
    expect(alpha.find('[data-testid="host-install-disk"]').exists()).toBe(false)
    expect(alpha.text()).not.toContain('disk')

    const bravo = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-config"]')
    const disk = bravo.find('[data-testid="host-install-disk"]')
    expect(disk.exists()).toBe(true)
    expect(disk.text()).toBe('disk /dev/sda')
    expect(disk.classes()).toContain('mono')
    expect(disk.attributes('title')).toContain('/dev/sda')
    expect(wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"]').text()).toContain('bluefin')
  })

  it('sends installDisk to /register when editing a bluefin host', async () => {
    const { wrapper, handlers, calls } = mountWithData()
    handlers['/register'] = (init) => jsonResponse({ status: 'ok', host: requestBody(init) })
    await flushPromises()

    await wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-action="edit"]').trigger('click')
    const editRow = wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:02"]')
    const disk = editRow.find('input[id^="disk-"]')
    expect((disk.element as HTMLInputElement).value).toBe('/dev/sda')
    await disk.setValue('/dev/nvme0n1')
    await editRow.find('form').trigger('submit')
    await flushPromises()

    const register = calls.find((c) => c.url === '/register')
    expect(requestBody<Host>(register!.init)).toMatchObject({
      mac: 'aa:bb:cc:dd:ee:02',
      os: 'bluefin',
      installDisk: '/dev/nvme0n1'
    })
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-install-disk"]').text()
    ).toBe('disk /dev/nvme0n1')
  })

  it('shows a control-plane badge only for control-plane hosts', async () => {
    const { wrapper } = mountWithData({
      hosts: {
        [hostA.mac]: { ...hostA, role: 'control-plane' },
        [hostB.mac]: { ...hostB, role: 'worker' },
        [hostC.mac]: { ...hostC, role: '' }
      },
      unknownHosts: {}
    })
    await flushPromises()

    const badge = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-testid="host-role"]')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('control-plane')
    expect(badge.classes()).toContain('badge')
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-role"]').exists()
    ).toBe(false)
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:03"] [data-testid="host-role"]').exists()
    ).toBe(false)
  })

  it('shows a Secure Boot badge next to the OS only for hosts flagged secureBoot', async () => {
    const { wrapper } = mountWithData({
      hosts: {
        [hostA.mac]: { ...hostA, secureBoot: true },
        [hostB.mac]: { ...hostB, secureBoot: false },
        [hostC.mac]: hostC
      },
      unknownHosts: {}
    })
    await flushPromises()

    const badge = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-testid="host-secure-boot"]')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('Secure Boot')
    expect(badge.classes()).toContain('badge')
    expect(badge.find('svg').exists()).toBe(true)
    expect(badge.attributes('title')).toContain('Secure Boot path')
    const osCell = badge.element.closest('td')!
    expect(osCell.textContent).toContain('flatcar')
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-secure-boot"]').exists()
    ).toBe(false)
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:03"] [data-testid="host-secure-boot"]').exists()
    ).toBe(false)
  })

  it('shows canary and autopilot state badges in the Host cell, idle hosts get none', async () => {
    const { wrapper } = mountWithData({
      hosts: {
        [hostA.mac]: {
          ...hostA,
          canary: true,
          autopilot: { state: 'retrying', attempt: 2, class: 'failed-units', target: '4800.0.0' }
        },
        [hostB.mac]: { ...hostB, autopilot: { state: 'idle' } },
        [hostC.mac]: hostC
      },
      unknownHosts: {}
    })
    await flushPromises()

    const rowA = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"]')
    expect(rowA.find('[data-testid="host-canary"]').text()).toBe('canary')
    const state = rowA.find('[data-testid="host-autopilot"]')
    expect(state.exists()).toBe(true)
    expect(state.text()).toBe('Retrying #2')
    expect(state.attributes('data-state')).toBe('retrying')
    expect(state.classes()).toContain('text-bg-warning')
    expect(state.attributes('title')).toContain('failed-units')
    expect(state.attributes('title')).toContain('4800.0.0')
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-autopilot"]').exists()
    ).toBe(false)
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-canary"]').exists()
    ).toBe(false)
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:03"] [data-testid="host-autopilot"]').exists()
    ).toBe(false)
  })

  it('keeps secureBoot in the /register payload when editing a flagged host', async () => {
    const { wrapper, handlers, calls } = mountWithData({
      hosts: { [hostA.mac]: { ...hostA, secureBoot: true } },
      unknownHosts: {}
    })
    handlers['/register'] = (init) => jsonResponse({ status: 'ok', host: requestBody(init) })
    await flushPromises()

    await wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-action="edit"]').trigger('click')
    await wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:01"] form').trigger('submit')
    await flushPromises()

    const register = calls.find((c) => c.url === '/register')
    expect(requestBody<Host>(register!.init).secureBoot).toBe(true)
    expect(
      wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-testid="host-secure-boot"]').exists()
    ).toBe(true)
  })

  it('sends role to /register when the user picks Control plane while editing', async () => {
    const { wrapper, handlers, calls } = mountWithData()
    handlers['/register'] = (init) => jsonResponse({ status: 'ok', host: requestBody(init) })
    await flushPromises()

    await wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-action="edit"]').trigger('click')
    const editRow = wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:01"]')
    await editRow.find('select[id^="role-"]').setValue('control-plane')
    await editRow.find('form').trigger('submit')
    await flushPromises()

    const register = calls.find((c) => c.url === '/register')
    expect(requestBody<Host>(register!.init)).toMatchObject({
      mac: 'aa:bb:cc:dd:ee:01',
      role: 'control-plane'
    })
    expect(wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-testid="host-role"]').text()).toBe(
      'control-plane'
    )
  })

  it('omits role from /register when the role select is left alone', async () => {
    const { wrapper, handlers, calls } = mountWithData()
    handlers['/register'] = (init) => jsonResponse({ status: 'ok', host: requestBody(init) })
    await flushPromises()

    await wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-action="edit"]').trigger('click')
    await wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:01"] form').trigger('submit')
    await flushPromises()

    const register = calls.find((c) => c.url === '/register')
    expect(requestBody<Host>(register!.init)).not.toHaveProperty('role')
  })

  it('treats hosts from an old server without fleet fields as never reported', async () => {
    const { wrapper } = mountWithData({
      hosts: { 'aa:bb:cc:dd:ee:10': { mac: 'aa:bb:cc:dd:ee:10', hostname: 'legacy' } },
      unknownHosts: {}
    })
    await flushPromises()

    const row = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:10"]')
    expect(row.find('[data-testid="host-status"]').exists()).toBe(false)
    expect(row.find('[data-testid="host-running"]').text()).toBe('no agent yet')
    expect(row.find('[data-testid="host-last-check"]').text()).toBe('never')
  })

  describe('reporting states in the Running cell', () => {
    const now = new Date('2026-10-01T12:00:00Z')
    const info = {
      flatcar: { version: '4593.2.1', pinnedVersion: '4593.2.1' },
      coreos: { version: '44.1' },
      bluefin: { version: '26.10.816', pinnedVersion: '' },
      targets: { flatcar: '4593.2.1', coreos: '44.1', bluefin: '26.10.816' }
    }
    const never: Host = {
      mac: 'aa:bb:cc:dd:ee:20',
      hostname: 'yghitan',
      ip: '192.168.50.31',
      booted: '',
      os: 'flatcar',
      installDisk: '',
      running: '',
      lastCheck: '',
      rebootPending: false,
      health: null,
      power: { state: 'up' }
    }
    const onTarget: Host = {
      ...never,
      mac: 'aa:bb:cc:dd:ee:21',
      hostname: 'aren',
      os: 'bluefin',
      booted: '2026-10-01T05:01:33Z',
      running: '26.10.816',
      lastCheck: new Date(now.getTime() - 5 * 60_000).toISOString()
    }
    const behind: Host = {
      ...onTarget,
      mac: 'aa:bb:cc:dd:ee:22',
      hostname: 'old-flatcar',
      os: 'flatcar',
      running: '4500.0.0',
      rebootPending: true
    }
    const stale: Host = {
      ...onTarget,
      mac: 'aa:bb:cc:dd:ee:23',
      hostname: 'quiet',
      lastCheck: new Date(now.getTime() - 45 * 60_000).toISOString()
    }

    function mountStates(hosts: Host[]) {
      vi.useFakeTimers({ now, toFake: ['Date'] })
      return mountWithData(
        { hosts: Object.fromEntries(hosts.map((h) => [h.mac, h])), unknownHosts: {} },
        { '/info': () => jsonResponse(info) }
      )
    }

    afterEach(() => {
      vi.useRealTimers()
    })

    it('shows a muted "no agent yet" badge instead of Unknown for a host that never reported', async () => {
      const { wrapper, calls } = mountStates([never])
      await flushPromises()

      expect(calls.some((c) => c.url === '/info')).toBe(true)
      const row = wrapper.find(`tr[data-mac="${never.mac}"]`)
      const cell = row.find('[data-testid="host-running"]')
      expect(cell.attributes('data-reporting')).toBe('never')
      const badge = cell.find('[data-testid="host-no-agent"]')
      expect(badge.text()).toBe('no agent yet')
      expect(badge.attributes('title')).toContain("Booted before Booty's update/health units existed")
      expect(badge.attributes('title')).toContain('reports every 10 min')
      expect(badge.classes()).toContain('badge')
      expect(row.find('[data-testid="host-status"]').exists()).toBe(false)
      expect(row.text()).not.toContain('Unknown')
      expect(row.find('[data-testid="host-power"]').exists()).toBe(true)
    })

    it('shows the running version with a match tick, or a "behind <target>" warning', async () => {
      const { wrapper } = mountStates([onTarget, behind])
      await flushPromises()

      const aren = wrapper.find(`tr[data-mac="${onTarget.mac}"] [data-testid="host-running"]`)
      expect(aren.attributes('data-reporting')).toBe('reporting')
      expect(aren.find('.mono').text()).toBe('26.10.816')
      const tick = aren.find('[data-testid="host-version-match"]')
      expect(tick.attributes('data-match')).toBe('match')
      expect(tick.attributes('title')).toContain('26.10.816')
      expect(tick.classes()).toContain('version-match--ok')
      expect(aren.find('[data-testid="host-stale"]').exists()).toBe(false)

      const old = wrapper.find(`tr[data-mac="${behind.mac}"] [data-testid="host-running"]`)
      expect(old.find('.mono').text()).toBe('4500.0.0')
      const warn = old.find('[data-testid="host-version-match"]')
      expect(warn.attributes('data-match')).toBe('behind')
      expect(warn.text()).toBe('behind 4593.2.1')
      expect(warn.classes()).toContain('text-bg-warning')
    })

    it('shows the running version plus "last seen" in warning colour when the last check is stale', async () => {
      const { wrapper } = mountStates([stale])
      await flushPromises()

      const cell = wrapper.find(`tr[data-mac="${stale.mac}"] [data-testid="host-running"]`)
      expect(cell.attributes('data-reporting')).toBe('stale')
      expect(cell.find('.mono').text()).toBe('26.10.816')
      const seen = cell.find('[data-testid="host-stale"]')
      expect(seen.text()).toBe('last seen 45m ago')
      expect(seen.classes()).toContain('version-stale')
      expect(cell.find('[data-testid="host-no-agent"]').exists()).toBe(false)
    })

    it('falls back to the per-OS version blocks when /info has no targets, and to no indicator when /info fails', async () => {
      const { wrapper } = mountStates([onTarget, behind])
      await flushPromises()
      expect(wrapper.findAll('[data-testid="host-version-match"]')).toHaveLength(2)

      vi.useRealTimers()
      const withoutTargets = { flatcar: info.flatcar, coreos: info.coreos, bluefin: info.bluefin }
      const fallback = mountWithData(
        { hosts: { [behind.mac]: behind }, unknownHosts: {} },
        { '/info': () => jsonResponse(withoutTargets) }
      )
      await flushPromises()
      expect(
        fallback.wrapper.find('[data-testid="host-version-match"]').text()
      ).toBe('behind 4593.2.1')

      const failing = mountWithData(
        { hosts: { [behind.mac]: behind }, unknownHosts: {} },
        { '/info': () => jsonResponse({ error: 'boom' }, 500) }
      )
      await flushPromises()
      expect(failing.wrapper.find('[data-testid="host-version-match"]').exists()).toBe(false)
      expect(failing.wrapper.find('[data-testid="host-running"] .mono').text()).toBe('4500.0.0')
    })
  })

  it('shows empty states and tolerates missing maps in the payload', async () => {
    const { wrapper } = mountWithData({})
    await flushPromises()
    expect(wrapper.text()).toContain('No hosts registered yet')
    expect(wrapper.text()).toContain('Nothing in the brig')
  })

  it('shows an error with retry when the initial load fails', async () => {
    const { wrapper, handlers } = mountWithData(data, {
      '/booty.json': () => jsonResponse({ error: 'db locked' }, 500)
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="error-message"]').text()).toBe('db locked')

    handlers['/booty.json'] = () => jsonResponse(data)
    await wrapper.find('.alert button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="error-message"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="hosts-table"] tbody tr[data-mac]')).toHaveLength(2)
  })

  it('keeps edit mode open and shows the server error when save fails', async () => {
    const { wrapper, handlers } = mountWithData()
    handlers['/register'] = () => jsonResponse({ error: 'invalid os "windows"' }, 400)
    await flushPromises()

    await wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-action="edit"]').trigger('click')
    const editRow = wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:01"]')
    expect(editRow.exists()).toBe(true)

    await editRow.find('input[id^="hostname-"]').setValue('alpha-renamed')
    await editRow.find('form').trigger('submit')
    await flushPromises()

    const stillEditing = wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:01"]')
    expect(stillEditing.exists()).toBe(true)
    expect(stillEditing.find('[data-testid="row-error"]').text()).toBe('invalid os "windows"')
    expect((stillEditing.find('input[id^="hostname-"]').element as HTMLInputElement).value).toBe(
      'alpha-renamed'
    )
    expect(wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"]').text()).toContain('alpha')
    expect(wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"]').text()).not.toContain('alpha-renamed')
  })

  it('cancel discards the draft and leaves the row untouched', async () => {
    const { wrapper } = mountWithData()
    await flushPromises()

    await wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-action="edit"]').trigger('click')
    const editRow = wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:01"]')
    await editRow.find('input[id^="hostname-"]').setValue('scratch')
    await editRow.find('button[type="button"]').trigger('click')

    expect(wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:01"]').exists()).toBe(false)
    expect(wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"]').text()).toContain('alpha')
    expect(wrapper.text()).not.toContain('scratch')
  })

  it('replaces the row key with the normalized MAC returned by the server on save', async () => {
    const payload: BootyData = {
      hosts: { 'AA-BB-CC-DD-EE-01': { ...hostA, mac: 'AA-BB-CC-DD-EE-01' } },
      unknownHosts: {}
    }
    const { wrapper, handlers, calls } = mountWithData(payload)
    handlers['/register'] = (init) => {
      const body = requestBody<Host>(init)
      return jsonResponse({
        status: 'ok',
        host: { ...body, mac: 'aa:bb:cc:dd:ee:01', hostname: body.hostname }
      })
    }
    await flushPromises()

    await wrapper.find('tr[data-mac="AA-BB-CC-DD-EE-01"] [data-action="edit"]').trigger('click')
    const editRow = wrapper.find('tr[data-mac-edit="AA-BB-CC-DD-EE-01"]')
    await editRow.find('input[id^="hostname-"]').setValue('alpha-2')
    await editRow.find('form').trigger('submit')
    await flushPromises()

    const register = calls.find((c) => c.url === '/register')
    expect(register).toBeDefined()
    expect(requestBody<Host>(register!.init)).toMatchObject({
      mac: 'AA-BB-CC-DD-EE-01',
      hostname: 'alpha-2'
    })

    expect(wrapper.find('tr[data-mac="AA-BB-CC-DD-EE-01"]').exists()).toBe(false)
    const normalized = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"]')
    expect(normalized.exists()).toBe(true)
    expect(normalized.text()).toContain('alpha-2')
    expect(wrapper.find('tr[data-mac-edit]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="hosts-table"] tbody tr[data-mac]')).toHaveLength(1)
  })

  it('only removes a row after /unregister returns 200', async () => {
    const { wrapper, handlers } = mountWithData()
    handlers['/unregister'] = () => jsonResponse({ error: 'host not found' }, 404)
    await flushPromises()

    const selector = 'tr[data-mac="aa:bb:cc:dd:ee:02"]'
    await wrapper.find(`${selector} [data-action="delete"]`).trigger('click')
    await wrapper.find(`${selector} [data-action="delete-confirm"]`).trigger('click')
    await flushPromises()

    expect(wrapper.find(selector).exists()).toBe(true)
    expect(wrapper.find('tr[data-mac-error="aa:bb:cc:dd:ee:02"]').text()).toContain(
      'host not found'
    )
    expect(wrapper.findAll('[data-testid="hosts-table"] tbody tr[data-mac]')).toHaveLength(2)

    handlers['/unregister'] = (init) => {
      expect(requestBody(init)).toEqual({ mac: 'aa:bb:cc:dd:ee:02' })
      return jsonResponse({ status: 'ok' })
    }
    await wrapper.find(`${selector} [data-action="delete"]`).trigger('click')
    await wrapper.find(`${selector} [data-action="delete-confirm"]`).trigger('click')
    await flushPromises()

    expect(wrapper.find(selector).exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="hosts-table"] tbody tr[data-mac]')).toHaveLength(1)
  })

  it('registers an unknown host pre-filled with its MAC and IP and moves it to the hosts table', async () => {
    const { wrapper, handlers } = mountWithData()
    handlers['/register'] = (init) => {
      const body = requestBody<Host>(init)
      return jsonResponse({ status: 'ok', host: { ...body, booted: '' } })
    }
    await flushPromises()

    const unknownSelector = 'tr[data-mac="aa:bb:cc:dd:ee:99"]'
    await wrapper.find(`${unknownSelector} [data-action="register"]`).trigger('click')
    const form = wrapper.find('tr[data-mac-edit="aa:bb:cc:dd:ee:99"]')
    expect(form.exists()).toBe(true)
    expect((form.find('input[id^="ip-"]').element as HTMLInputElement).value).toBe('10.0.0.99')

    await form.find('input[id^="hostname-"]').setValue('charlie')
    await form.find('select').setValue('coreos')
    await form.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('[data-testid="unknown-table"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Nothing in the brig')
    const registered = wrapper.find(`[data-testid="hosts-table"] ${unknownSelector}`)
    expect(registered.exists()).toBe(true)
    expect(registered.text()).toContain('charlie')
    expect(registered.text()).toContain('coreos')
  })

  it('shows the power badge, enables the power buttons from /power and updates the row on 202', async () => {
    const up = { ...hostA, power: { state: 'up', since: '2026-09-24T10:00:00Z', reason: 'answers on tcp/22' } }
    const off = { ...hostB, power: { state: 'off', since: '2026-09-24T09:00:00Z' } }
    const { wrapper, calls, handlers } = mountWithData(
      { hosts: { [up.mac]: up, [off.mac]: off }, unknownHosts: {} },
      {
        '/power': () =>
          jsonResponse({
            hosts: {},
            events: [],
            capabilities: { wol: true, actuator: 'ssh' },
            summary: { up: 1, off: 1, unreachable: 0, inFlight: 0 }
          })
      }
    )
    await flushPromises()
    expect(calls.some((c) => c.url === '/power')).toBe(true)

    const alpha = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"]')
    const badge = alpha.find('[data-testid="host-power"]')
    expect(badge.attributes('data-state')).toBe('up')
    expect(badge.text()).toContain('Up')
    expect(alpha.find('[data-action="power-reboot"]').attributes('disabled')).toBeUndefined()
    expect(alpha.find('[data-action="power-shutdown"]').attributes('disabled')).toBeUndefined()
    expect(alpha.find('[data-action="power-on"]').attributes('disabled')).toBeDefined()

    const bravo = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"]')
    expect(bravo.find('[data-testid="host-power"]').attributes('data-state')).toBe('off')
    expect(bravo.find('[data-action="power-on"]').attributes('disabled')).toBeUndefined()
    expect(bravo.find('[data-action="power-reboot"]').attributes('disabled')).toBeDefined()

    handlers['/power/aa%3Abb%3Acc%3Add%3Aee%3A02/on'] = () =>
      jsonResponse({ status: 'ok', mac: off.mac, power: { state: 'powering-on', request: 'on' } }, 202)
    await bravo.find('[data-action="power-on"]').trigger('click')
    expect(bravo.find('[data-testid="power-confirm"]').exists()).toBe(true)
    expect(bravo.find('[data-action="edit"]').attributes('disabled')).toBeDefined()
    await bravo.find('[data-action="power-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-testid="host-power"]').attributes('data-state')).toBe(
      'powering-on'
    )
    expect(wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:02"] [data-action="power-on"]').attributes('disabled')).toBeDefined()
  })

  it('disables reboot and shutdown with the no-actuator reason when /power says none or fails', async () => {
    const up = { ...hostA, power: { state: 'up' } }
    const { wrapper } = mountWithData(
      { hosts: { [up.mac]: up }, unknownHosts: {} },
      { '/power': () => jsonResponse({ capabilities: { wol: true, actuator: 'none' } }) }
    )
    await flushPromises()
    const reboot = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-action="power-reboot"]')
    expect(reboot.attributes('disabled')).toBeDefined()
    expect(reboot.attributes('title')).toContain('no actuator')

    const failing = mountWithData({ hosts: { [up.mac]: up }, unknownHosts: {} })
    await flushPromises()
    expect(
      failing.wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"] [data-action="power-shutdown"]').attributes('disabled')
    ).toBeDefined()
  })

  it('offers force only for the last up worker on shutdown', async () => {
    const up = { ...hostA, power: { state: 'up' } }
    const { wrapper } = mountWithData(
      { hosts: { [up.mac]: up, [hostC.mac]: { ...hostC, power: { state: 'off' } } }, unknownHosts: {} },
      { '/power': () => jsonResponse({ capabilities: { wol: true, actuator: 'api' } }) }
    )
    await flushPromises()
    const row = wrapper.find('tr[data-mac="aa:bb:cc:dd:ee:01"]')
    await row.find('[data-action="power-shutdown"]').trigger('click')
    expect(row.find('[data-testid="power-force"]').text()).toContain('last worker')
  })
})
