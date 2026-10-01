import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import PowerActions from '@/components/PowerActions.vue'
import PowerBadge from '@/components/PowerBadge.vue'
import type { Host, PowerCapabilities, PowerState } from '@/types'
import { jsonResponse, mockFetch, requestBody } from '@/__tests__/helpers'

const ssh: PowerCapabilities = { wol: true, actuator: 'ssh' }

function host(state: PowerState, extra: Partial<Host> = {}): Host {
  return {
    mac: 'aa:bb:cc:dd:ee:01',
    hostname: 'alpha',
    ip: '10.0.0.1',
    booted: '',
    installDisk: '',
    running: '',
    lastCheck: '',
    rebootPending: false,
    power: { state, since: '2026-09-30T10:00:00Z', reason: 'answers on tcp/22' },
    ...extra
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('PowerBadge', () => {
  it('shows the state with a coloured dot and the reason in the tooltip', () => {
    const w = mount(PowerBadge, { props: { power: host('up').power } })
    const badge = w.get('[data-testid="host-power"]')
    expect(badge.attributes('data-state')).toBe('up')
    expect(badge.classes()).toContain('text-bg-success')
    expect(badge.text()).toContain('Up')
    expect(badge.attributes('title')).toContain('answers on tcp/22')
    expect(w.find('.power-dot').classes()).toContain('power-dot--up')
  })

  it('falls back to unknown without a power block', () => {
    const w = mount(PowerBadge, { props: { power: undefined } })
    expect(w.get('[data-testid="host-power"]').attributes('data-state')).toBe('unknown')
    expect(w.text()).toContain('Unknown')
  })

  it('pulses while a transition is in flight', () => {
    const w = mount(PowerBadge, {
      props: { power: { state: 'rebooting', request: 'reboot', cordoned: true } }
    })
    expect(w.find('.power-dot').classes()).toContain('power-dot--busy')
    expect(w.get('[data-testid="host-power"]').attributes('title')).toContain('node cordoned')
  })
})

describe('PowerActions', () => {
  it('enables the buttons by state and explains the disabled ones', () => {
    const w = mount(PowerActions, {
      props: { host: host('up'), capabilities: ssh, lastUpWorker: false }
    })
    expect(w.get('[data-action="power-on"]').attributes('disabled')).toBeDefined()
    expect(w.get('[data-action="power-on"]').attributes('title')).toContain('host is up')
    expect(w.get('[data-action="power-reboot"]').attributes('disabled')).toBeUndefined()
    expect(w.get('[data-action="power-shutdown"]').attributes('disabled')).toBeUndefined()

    const off = mount(PowerActions, {
      props: { host: host('off'), capabilities: ssh, lastUpWorker: false }
    })
    expect(off.get('[data-action="power-on"]').attributes('disabled')).toBeUndefined()
    expect(off.get('[data-action="power-reboot"]').attributes('disabled')).toBeDefined()

    const none = mount(PowerActions, {
      props: {
        host: host('up'),
        capabilities: { wol: true, actuator: 'none' },
        lastUpWorker: false
      }
    })
    expect(none.get('[data-action="power-reboot"]').attributes('title')).toContain('no actuator')

    const driving = mount(PowerActions, {
      props: {
        host: host('up', { autopilot: { state: 'rolling' } }),
        capabilities: ssh,
        lastUpWorker: false
      }
    })
    expect(driving.get('[data-action="power-shutdown"]').attributes('title')).toBe(
      'autopilot is driving this host'
    )
  })

  it('confirms with a reason and posts the action', async () => {
    const fetch = mockFetch((url, init) => {
      expect(url).toBe('/power/aa%3Abb%3Acc%3Add%3Aee%3A01/reboot')
      expect(requestBody(init)).toEqual({ reason: 'kernel update' })
      return jsonResponse(
        { status: 'ok', mac: 'aa:bb:cc:dd:ee:01', power: { state: 'draining', request: 'reboot' } },
        202
      )
    })
    const w = mount(PowerActions, {
      props: { host: host('up'), capabilities: ssh, lastUpWorker: false }
    })
    await w.get('[data-action="power-reboot"]').trigger('click')
    expect(w.find('[data-testid="power-confirm"]').exists()).toBe(true)
    expect(w.find('[data-testid="power-force"]').exists()).toBe(false)
    expect(w.emitted('confirming')?.[0]).toEqual([true])
    await w.get('[data-testid="power-reason"]').setValue('kernel update')
    await w.get('[data-action="power-confirm"]').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(1)
    const updated = w.emitted('updated')
    expect(updated).toHaveLength(1)
    expect(updated![0]![0]).toMatchObject({ state: 'draining', request: 'reboot' })
    expect(w.find('[data-testid="power-confirm"]').exists()).toBe(false)
    expect(w.emitted('confirming')?.at(-1)).toEqual([false])
  })

  it('shows the force checkbox only when a guard applies and sends force', async () => {
    const fetch = mockFetch((_url, init) => {
      expect(requestBody(init)).toEqual({ force: true })
      return jsonResponse(
        { status: 'ok', mac: 'x', power: { state: 'draining', request: 'shutdown' } },
        202
      )
    })
    const w = mount(PowerActions, {
      props: { host: host('up', { role: 'control-plane' }), capabilities: ssh, lastUpWorker: false }
    })
    await w.get('[data-action="power-shutdown"]').trigger('click')
    const force = w.get('[data-testid="power-force"]')
    expect(force.text()).toContain('control-plane host')
    const confirm = w.get('[data-action="power-confirm"]')
    expect(confirm.attributes('disabled')).toBeDefined()
    expect(confirm.text()).toContain('needs force')
    await force.get('input').setValue(true)
    expect(w.get('[data-action="power-confirm"]').attributes('disabled')).toBeUndefined()
    await w.get('[data-action="power-confirm"]').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(1)

    const last = mount(PowerActions, {
      props: { host: host('up'), capabilities: ssh, lastUpWorker: true }
    })
    await last.get('[data-action="power-shutdown"]').trigger('click')
    expect(last.get('[data-testid="power-force"]').text()).toContain('last worker')
    await last.get('[data-action="power-cancel"]').trigger('click')
    expect(last.find('[data-testid="power-confirm"]').exists()).toBe(false)
    await last.get('[data-action="power-reboot"]').trigger('click')
    expect(last.find('[data-testid="power-force"]').exists()).toBe(false)
  })

  it('shows a 409 on the row and keeps the confirm open', async () => {
    mockFetch(() =>
      jsonResponse({ error: 'autopilot is driving this host', power: { state: 'up' } }, 409)
    )
    const w = mount(PowerActions, {
      props: { host: host('up'), capabilities: ssh, lastUpWorker: false }
    })
    await w.get('[data-action="power-reboot"]').trigger('click')
    await w.get('[data-action="power-confirm"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid="power-error"]').text()).toBe('autopilot is driving this host')
    expect(w.find('[data-testid="power-confirm"]').exists()).toBe(true)
    expect(w.emitted('updated')![0]![0]).toMatchObject({ state: 'up' })
  })
})
