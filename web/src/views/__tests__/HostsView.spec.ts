import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import HostsView from '@/views/HostsView.vue'
import type { BootyData, Host } from '@/types'
import { jsonResponse, mockFetch, requestBody } from '@/__tests__/helpers'

const hostA: Host = {
  mac: 'aa:bb:cc:dd:ee:01',
  hostname: 'alpha',
  ip: '10.0.0.1',
  booted: '2026-09-24T10:00:00Z',
  ignitionFile: 'alpha.yaml',
  os: 'flatcar',
  ostreeImage: '',
  doInstall: false
}

const hostB: Host = {
  mac: 'aa:bb:cc:dd:ee:02',
  hostname: 'bravo',
  ip: '10.0.0.2',
  booted: '',
  ignitionFile: '',
  os: 'ublue',
  ostreeImage: 'ghcr.io/ublue-os/bazzite:stable',
  doInstall: true
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
    expect(link.attributes('href')).toBe('/ignition.json?mac=aa%3Abb%3Acc%3Add%3Aee%3A01')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener noreferrer')

    expect(rows[0]!.text()).toContain('ago')
    expect(rows[1]!.text()).toContain('never')

    const unknownRows = wrapper.findAll('[data-testid="unknown-table"] tbody tr[data-mac]')
    expect(unknownRows).toHaveLength(1)
    expect(unknownRows[0]!.text()).toContain('10.0.0.99')
    expect(unknownRows[0]!.text()).toContain('7')
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
})
