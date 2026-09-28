import { describe, expect, it } from 'vitest'
import { reactive } from 'vue'
import { mount } from '@vue/test-utils'
import HostForm from '@/components/HostForm.vue'
import type { Host } from '@/types'

function draft(overrides: Partial<Host> = {}): Host {
  return reactive({
    mac: 'aa:bb:cc:dd:ee:01',
    hostname: 'alpha',
    ip: '10.0.0.1',
    booted: '',
    ignitionFile: '',
    os: '',
    ostreeImage: '',
    installDisk: '',
    doInstall: false,
    running: '',
    lastCheck: '',
    rebootPending: false,
    ...overrides
  })
}

function mountForm(modelValue: Host) {
  return mount(HostForm, {
    props: { modelValue, busy: false, error: '', submitLabel: 'Save' }
  })
}

const DISK_FIELD = '[data-testid="install-disk-field"]'
const ROLE_FIELD = '[data-testid="role-field"]'
const OS_SELECT = 'select[id^="os-"]'
const BLUEFIN_FIELDS = '[data-testid="bluefin-fields"]'

describe('HostForm', () => {
  it('offers (default), flatcar, coreos and bluefin as OS choices', () => {
    const wrapper = mountForm(draft())
    const options = wrapper.findAll(`${OS_SELECT} option`).map((o) => o.attributes('value'))
    expect(options).toEqual(['', 'flatcar', 'coreos', 'bluefin'])
  })

  it('hides the install disk field for the default and flatcar OS', async () => {
    const model = draft({ os: '' })
    const wrapper = mountForm(model)
    expect(wrapper.find(DISK_FIELD).exists()).toBe(false)

    await wrapper.find('select').setValue('flatcar')
    expect(wrapper.find(DISK_FIELD).exists()).toBe(false)
  })

  it('shows the install disk field for bluefin and coreos, right after OS', async () => {
    const model = draft({ os: 'bluefin' })
    const wrapper = mountForm(model)
    const field = wrapper.find(DISK_FIELD)
    expect(field.exists()).toBe(true)
    expect(field.text()).toContain('Install disk')
    expect(field.text()).toContain('systemd-sysinstall')
    expect(field.text()).not.toContain('first writable disk')
    expect(field.text()).toContain('Wiped on install')
    expect(field.find('input').attributes('placeholder')).toBe('/dev/sda')
    expect(field.find('input').classes()).toContain('mono')

    const ids = wrapper.findAll('input, select').map((el) => el.attributes('id'))
    expect(ids.indexOf('disk-aa:bb:cc:dd:ee:01')).toBe(ids.indexOf('os-aa:bb:cc:dd:ee:01') + 1)

    await wrapper.find('select').setValue('coreos')
    expect(wrapper.find(DISK_FIELD).exists()).toBe(true)
    expect(wrapper.find(DISK_FIELD).text()).toContain('empty = first writable disk')
  })

  it('writes the install disk into the draft', async () => {
    const model = draft({ os: 'bluefin' })
    const wrapper = mountForm(model)
    await wrapper.find(`${DISK_FIELD} input`).setValue('/dev/nvme0n1')
    expect(model.installDisk).toBe('/dev/nvme0n1')
  })

  it('pre-fills the install disk from the draft and submits it unchanged', async () => {
    const model = draft({ os: 'coreos', installDisk: '/dev/sdb' })
    const wrapper = mountForm(model)
    expect((wrapper.find(`${DISK_FIELD} input`).element as HTMLInputElement).value).toBe('/dev/sdb')
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('submit')).toHaveLength(1)
    expect(model.installDisk).toBe('/dev/sdb')
  })

  it('offers Worker (default) and Control plane as roles with the CA hint', () => {
    const wrapper = mountForm(draft())
    const field = wrapper.find(ROLE_FIELD)
    expect(field.text()).toContain('Role')
    expect(field.text()).toContain(
      'Control-plane hosts receive the cluster CA when the control plane is Booty-managed'
    )
    const options = field.findAll('option').map((o) => o.attributes('value'))
    expect(options).toEqual(['worker', 'control-plane'])
    expect(field.findAll('option').map((o) => o.text())).toEqual(['Worker', 'Control plane'])
    expect((field.find('select').element as HTMLSelectElement).value).toBe('worker')
  })

  it('leaves role out of an untouched draft so existing register payloads are unchanged', async () => {
    const model = draft()
    const wrapper = mountForm(model)
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('submit')).toHaveLength(1)
    expect(model).not.toHaveProperty('role')
  })

  it('shows Worker for a server-side "" role and keeps "" until the user changes it', () => {
    const model = draft({ role: '' })
    const wrapper = mountForm(model)
    expect((wrapper.find(`${ROLE_FIELD} select`).element as HTMLSelectElement).value).toBe('worker')
    expect(model.role).toBe('')
  })

  it('writes control-plane into the draft and an explicit worker when switched back', async () => {
    const model = draft()
    const wrapper = mountForm(model)
    const select = wrapper.find(`${ROLE_FIELD} select`)

    await select.setValue('control-plane')
    expect(model.role).toBe('control-plane')

    await select.setValue('worker')
    expect(model.role).toBe('worker')
  })

  it('pre-selects Control plane for a control-plane host', () => {
    const wrapper = mountForm(draft({ role: 'control-plane' }))
    expect((wrapper.find(`${ROLE_FIELD} select`).element as HTMLSelectElement).value).toBe(
      'control-plane'
    )
  })

  it('shows mode, state disk and extensions for bluefin only', async () => {
    const model = draft({ os: 'flatcar' })
    const wrapper = mountForm(model)
    expect(wrapper.find(BLUEFIN_FIELDS).exists()).toBe(false)
    await wrapper.find(OS_SELECT).setValue('bluefin')
    const fields = wrapper.find(BLUEFIN_FIELDS)
    expect(fields.exists()).toBe(true)
    expect(fields.findAll('select[id^="mode-"] option').map((o) => o.attributes('value'))).toEqual([
      'diskless',
      'installed'
    ])
    expect((fields.find('select[id^="mode-"]').element as HTMLSelectElement).value).toBe('diskless')
    expect(fields.findAll('input[type="checkbox"]').map((o) => o.attributes('id'))).toEqual([
      'ext-zfs-aa:bb:cc:dd:ee:01',
      'ext-kubestellar-aa:bb:cc:dd:ee:01',
      'ext-k0s-aa:bb:cc:dd:ee:01'
    ])
    expect(fields.text()).toContain('kubestellar needs k0s')
  })

  it('leaves the bluefin fields out of an untouched draft', async () => {
    const model = draft({ os: 'bluefin' })
    const wrapper = mountForm(model)
    await wrapper.find('form').trigger('submit')
    expect(model).not.toHaveProperty('mode')
    expect(model).not.toHaveProperty('extensions')
    expect(model).not.toHaveProperty('stateDisk')
  })

  it('writes mode, state disk and extensions into the draft', async () => {
    const model = draft({ os: 'bluefin', extensions: ['zfs'] })
    const wrapper = mountForm(model)
    const fields = wrapper.find(BLUEFIN_FIELDS)
    expect(
      (fields.find('#ext-zfs-aa\\:bb\\:cc\\:dd\\:ee\\:01').element as HTMLInputElement).checked
    ).toBe(true)

    await fields.find('select[id^="mode-"]').setValue('installed')
    await fields.find('input[id^="statedisk-"]').setValue('/dev/sdb')
    await fields.find('#ext-k0s-aa\\:bb\\:cc\\:dd\\:ee\\:01').setValue(true)
    await fields.find('#ext-kubestellar-aa\\:bb\\:cc\\:dd\\:ee\\:01').setValue(true)
    await fields.find('#ext-zfs-aa\\:bb\\:cc\\:dd\\:ee\\:01').setValue(false)

    expect(model.mode).toBe('installed')
    expect(model.stateDisk).toBe('/dev/sdb')
    expect(model.extensions).toEqual(['k0s', 'kubestellar'])
  })
})
