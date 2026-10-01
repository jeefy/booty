import { describe, expect, it } from 'vitest'
import type { Host, PowerCapabilities, PowerState } from '@/types'
import {
  AUTOPILOT_DRIVING_REASON,
  NO_ACTUATOR_REASON,
  isLastUpWorker,
  powerButton,
  powerButtons,
  powerSummaryText,
  summarizePower
} from '@/utils/power'

const ssh: PowerCapabilities = { wol: true, actuator: 'ssh' }
const none: PowerCapabilities = { wol: true, actuator: 'none' }

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
    power: { state },
    ...extra
  }
}

describe('powerButton enablement matrix', () => {
  const ctx = { capabilities: ssh, lastUpWorker: false }
  const cases: Array<[PowerState, boolean, boolean, boolean]> = [
    ['unknown', true, false, false],
    ['off', true, false, false],
    ['unreachable', true, false, false],
    ['up', false, true, true],
    ['booting', false, false, false],
    ['powering-on', false, false, false],
    ['draining', false, false, false],
    ['rebooting', false, false, false],
    ['shutting-down', false, false, false]
  ]
  for (const [state, on, reboot, shutdown] of cases) {
    it(`${state}: on=${on} reboot=${reboot} shutdown=${shutdown}`, () => {
      const [b1, b2, b3] = powerButtons(host(state), ctx)
      expect(b1!.enabled).toBe(on)
      expect(b2!.enabled).toBe(reboot)
      expect(b3!.enabled).toBe(shutdown)
      for (const b of [b1!, b2!, b3!]) {
        if (!b.enabled) expect(b.title).toContain('host is')
      }
    })
  }

  it('names the autopilot and the missing actuator in the tooltip', () => {
    const driving = host('up', { autopilot: { state: 'gating' } })
    expect(powerButton('reboot', driving, ctx)).toMatchObject({
      enabled: false,
      title: AUTOPILOT_DRIVING_REASON
    })
    expect(powerButton('shutdown', driving, ctx).title).toBe(AUTOPILOT_DRIVING_REASON)
    expect(powerButton('on', host('off', { autopilot: { state: 'gating' } }), ctx).enabled).toBe(
      true
    )

    const noAct = { capabilities: none, lastUpWorker: false }
    expect(powerButton('reboot', host('up'), noAct)).toMatchObject({
      enabled: false,
      title: NO_ACTUATOR_REASON
    })
    expect(powerButton('on', host('off'), noAct).enabled).toBe(true)
    expect(
      powerButton('reboot', host('up'), { capabilities: null, lastUpWorker: false }).enabled
    ).toBe(false)
  })

  it('asks for force on a control plane or the last worker', () => {
    const cp = host('up', { role: 'control-plane' })
    expect(powerButton('reboot', cp, ctx)).toMatchObject({
      enabled: true,
      needsForce: true,
      forceReason: 'control-plane host'
    })
    expect(powerButton('shutdown', cp, ctx).needsForce).toBe(true)
    expect(
      powerButton('reboot', host('up'), { capabilities: ssh, lastUpWorker: true }).needsForce
    ).toBe(false)
    expect(
      powerButton('shutdown', host('up'), { capabilities: ssh, lastUpWorker: true })
    ).toMatchObject({
      needsForce: true,
      forceReason: 'last worker that is up'
    })
    expect(powerButton('shutdown', host('up'), ctx).needsForce).toBe(false)
  })
})

describe('isLastUpWorker and summaries', () => {
  const a = host('up')
  const b = host('up', { mac: 'aa:bb:cc:dd:ee:02' })
  const cp = host('up', { mac: 'aa:bb:cc:dd:ee:03', role: 'control-plane' })
  const off = host('off', { mac: 'aa:bb:cc:dd:ee:04' })
  const busy = host('rebooting', { mac: 'aa:bb:cc:dd:ee:05' })
  const down = host('unreachable', { mac: 'aa:bb:cc:dd:ee:06' })

  it('counts only other up workers', () => {
    expect(isLastUpWorker(a, [a, cp, off])).toBe(true)
    expect(isLastUpWorker(a, [a, b])).toBe(false)
    expect(isLastUpWorker(cp, [a, cp])).toBe(false)
    expect(isLastUpWorker(off, [a, off])).toBe(false)
  })

  it('summarizes the fleet', () => {
    const s = summarizePower([a, b, cp, off, busy, down])
    expect(s).toEqual({ up: 3, off: 1, unreachable: 1, inFlight: 1 })
    expect(powerSummaryText(s)).toBe('3 up · 1 off · 1 unreachable')
  })
})
