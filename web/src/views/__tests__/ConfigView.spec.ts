import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ConfigView from '@/views/ConfigView.vue'
import { jsonResponse, mockFetch, requestBody } from '@/__tests__/helpers'

type Handlers = Record<string, (init?: RequestInit) => Response>

const TEMPLATE = 'variant: flatcar\nversion: 1.0.0\nstorage:\n  files: []\n'
const IGNITION = '{\n  "ignition": {\n    "version": "3.4.0"\n  }\n}'

const settings = [
  { key: 'serverIP', value: '192.168.1.10', source: 'flag', redacted: false },
  { key: 'httpPort', value: '8080', source: 'default', redacted: false },
  { key: 'githubToken', value: '', source: 'env', redacted: true },
  { key: 'joinString', value: '', source: 'flag', redacted: true }
]

const configBody = {
  settings,
  templates: {
    default: { name: 'config/ignition.yaml', source: 'file', writable: true },
    hosts: [{ mac: 'aa:bb:cc:dd:ee:01', hostname: 'alpha', name: 'config/alpha.yaml' }]
  }
}

const bootyHosts = {
  hosts: {
    'aa:bb:cc:dd:ee:01': { mac: 'aa:bb:cc:dd:ee:01', hostname: 'alpha' },
    'aa:bb:cc:dd:ee:02': { mac: 'aa:bb:cc:dd:ee:02', hostname: 'bravo' }
  },
  unknownHosts: {}
}

function templateDoc(overrides: Record<string, unknown> = {}) {
  return {
    name: 'config/ignition.yaml',
    source: 'file',
    writable: true,
    content: TEMPLATE,
    ...overrides
  }
}

function mountConfig(overrides: Handlers = {}) {
  const handlers: Handlers = {
    '/config': () => jsonResponse(configBody),
    '/booty.json': () => jsonResponse(bootyHosts),
    '/config/template': (init) => {
      if (init?.method === 'PUT') {
        const { content } = requestBody<{ name: string; content: string }>(init)
        return jsonResponse(templateDoc({ content }))
      }
      return jsonResponse(templateDoc())
    },
    '/config/template?name=config%2Falpha.yaml': () =>
      jsonResponse(templateDoc({ name: 'config/alpha.yaml', content: 'variant: fcos\n' })),
    '/config/template/validate': () => jsonResponse({ ok: true, ignition: IGNITION, entries: [] }),
    ...overrides
  }
  const spy = mockFetch((url, init) => {
    const handler = handlers[url]
    if (!handler) return jsonResponse({ error: `unexpected ${url}` }, 500)
    return handler(init)
  })
  const wrapper = mount(ConfigView)
  return { wrapper, handlers, spy }
}

function editorOf(wrapper: ReturnType<typeof mount>) {
  return wrapper.find<HTMLTextAreaElement>('[data-testid="editor"]')
}

function button(wrapper: ReturnType<typeof mount>, action: string) {
  return wrapper.find<HTMLButtonElement>(`[data-action="${action}"]`)
}

function callsTo(spy: ReturnType<typeof mockFetch>, url: string, method?: string) {
  return spy.mock.calls.filter(
    ([input, init]) => input === url && (method === undefined || (init?.method ?? 'GET') === method)
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ConfigView settings', () => {
  it('renders every effective setting with its source badge and redacts secrets', async () => {
    const { wrapper } = mountConfig()
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="settings-table"] tbody tr')
    expect(rows).toHaveLength(4)

    const server = wrapper.find('[data-key="serverIP"]')
    expect(server.text()).toContain('192.168.1.10')
    expect(server.find('.badge').attributes('data-source')).toBe('flag')
    expect(server.find('[data-testid="redacted"]').exists()).toBe(false)

    const token = wrapper.find('[data-key="githubToken"]')
    const redacted = token.find('[data-testid="redacted"]')
    expect(redacted.exists()).toBe(true)
    expect(redacted.attributes('title')).toBe('redacted')
    expect(redacted.text()).toContain('••••')
    expect(token.find('.badge').attributes('data-source')).toBe('env')
    expect(wrapper.find('[data-key="httpPort"] .badge').attributes('data-source')).toBe('default')
  })

  it('filters settings client-side by key or value', async () => {
    const { wrapper } = mountConfig()
    await flushPromises()

    await wrapper.find('[data-testid="settings-filter"]').setValue('token')
    let rows = wrapper.findAll('[data-testid="settings-table"] tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.attributes('data-key')).toBe('githubToken')

    await wrapper.find('[data-testid="settings-filter"]').setValue('192.168')
    rows = wrapper.findAll('[data-testid="settings-table"] tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.attributes('data-key')).toBe('serverIP')

    await wrapper.find('[data-testid="settings-filter"]').setValue('nope')
    expect(wrapper.find('[data-testid="settings-table"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('No settings match')
  })

  it('shows an error alert when GET /config fails and retries', async () => {
    const { wrapper, handlers } = mountConfig({
      '/config': () => jsonResponse({ error: 'config unavailable' }, 500)
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="error-message"]').text()).toBe('config unavailable')

    handlers['/config'] = () => jsonResponse(configBody)
    await wrapper.find('.alert button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="error-message"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="settings-table"]').exists()).toBe(true)
  })
})

describe('ConfigView template editor', () => {
  it('loads the default template into the editor and lists per-host templates', async () => {
    const { wrapper, spy } = mountConfig()
    await flushPromises()

    expect(callsTo(spy, '/config/template', 'GET')).toHaveLength(1)
    expect(editorOf(wrapper).element.value).toBe(TEMPLATE)
    expect(wrapper.find('[data-testid="dirty-indicator"]').exists()).toBe(false)

    const options = wrapper.findAll('[data-testid="template-select"] option')
    expect(options.map((o) => o.text())).toEqual([
      'Default — config/ignition.yaml',
      'alpha (aa:bb:cc:dd:ee:01) — config/alpha.yaml'
    ])

    const hosts = wrapper.findAll('[data-testid="preview-host-select"] option')
    expect(hosts.map((o) => o.text())).toEqual([
      'Dummy host',
      'alpha (aa:bb:cc:dd:ee:01)',
      'bravo (aa:bb:cc:dd:ee:02)'
    ])
    expect(wrapper.find('[data-testid="readonly-banner"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="embedded-note"]').exists()).toBe(false)
  })

  it('fetches a per-host template by name when selected', async () => {
    const { wrapper, spy } = mountConfig()
    await flushPromises()

    await wrapper.find('[data-testid="template-select"]').setValue('config/alpha.yaml')
    await flushPromises()

    expect(callsTo(spy, '/config/template?name=config%2Falpha.yaml', 'GET')).toHaveLength(1)
    expect(editorOf(wrapper).element.value).toBe('variant: fcos\n')
  })

  it('marks the editor dirty on edit, blocks template switching, and discards back', async () => {
    const { wrapper } = mountConfig()
    await flushPromises()

    expect(button(wrapper, 'save').element.disabled).toBe(true)
    expect(button(wrapper, 'discard').element.disabled).toBe(true)

    await editorOf(wrapper).setValue(TEMPLATE + 'extra: true\n')
    expect(wrapper.find('[data-testid="dirty-indicator"]').exists()).toBe(true)
    expect(button(wrapper, 'save').element.disabled).toBe(false)
    expect(
      wrapper.find<HTMLSelectElement>('[data-testid="template-select"]').element.disabled
    ).toBe(true)

    await button(wrapper, 'discard').trigger('click')
    expect(editorOf(wrapper).element.value).toBe(TEMPLATE)
    expect(wrapper.find('[data-testid="dirty-indicator"]').exists()).toBe(false)
  })

  it('validates through POST /config/template/validate and lists the entries', async () => {
    const { wrapper, spy } = mountConfig({
      '/config/template/validate': () =>
        jsonResponse({
          ok: false,
          ignition: '',
          entries: [
            { kind: 'warning', message: 'storage.files: unused key' },
            { kind: 'error', message: 'line 3: yaml: did not find expected key' }
          ]
        })
    })
    await flushPromises()

    await editorOf(wrapper).setValue('variant: flatcar\nversion: 1.0.0\nbroken')
    await button(wrapper, 'validate').trigger('click')
    await flushPromises()

    const [call] = callsTo(spy, '/config/template/validate', 'POST')
    expect(call).toBeDefined()
    expect(requestBody(call![1])).toEqual({
      name: 'config/ignition.yaml',
      content: 'variant: flatcar\nversion: 1.0.0\nbroken'
    })

    const summary = wrapper.find('[data-testid="validation-summary"]')
    expect(summary.text()).toContain('Invalid')
    expect(summary.text()).toContain('2 entries')

    const entries = wrapper.findAll('[data-testid="validation-entries"] li')
    expect(entries).toHaveLength(2)
    expect(entries[0]!.attributes('data-kind')).toBe('warning')
    expect(entries[0]!.classes()).toContain('entry--warning')
    expect(entries[0]!.text()).toContain('storage.files: unused key')
    expect(entries[1]!.attributes('data-kind')).toBe('error')
    expect(entries[1]!.classes()).toContain('entry--error')

    expect(button(wrapper, 'save').element.disabled).toBe(true)
    expect(wrapper.find('[data-testid="ignition-preview"]').exists()).toBe(false)

    await editorOf(wrapper).setValue('variant: flatcar\nversion: 1.0.0\n')
    expect(summary.text()).toContain('re-run Validate')
    expect(button(wrapper, 'save').element.disabled).toBe(false)
  })

  it('previews the rendered Ignition for the selected host', async () => {
    const { wrapper, spy } = mountConfig()
    await flushPromises()

    await wrapper.find('[data-testid="preview-host-select"]').setValue('aa:bb:cc:dd:ee:02')
    await button(wrapper, 'preview').trigger('click')
    await flushPromises()

    const [call] = callsTo(spy, '/config/template/validate', 'POST')
    expect(requestBody(call![1])).toEqual({
      name: 'config/ignition.yaml',
      content: TEMPLATE,
      mac: 'aa:bb:cc:dd:ee:02'
    })

    const preview = wrapper.find('[data-testid="ignition-preview"]')
    expect(preview.exists()).toBe(true)
    expect(preview.find('pre').text()).toBe(IGNITION)
    expect(preview.text()).toContain('as aa:bb:cc:dd:ee:02')
    expect(wrapper.find('[data-testid="validation-summary"]').text()).toContain('Valid')

    await button(wrapper, 'close-preview').trigger('click')
    expect(wrapper.find('[data-testid="ignition-preview"]').exists()).toBe(false)
  })

  it('saves via PUT /config/template with the edited content and refreshes from the response', async () => {
    const { wrapper, spy } = mountConfig()
    await flushPromises()

    const edited = TEMPLATE + 'passwd: {}\n'
    await editorOf(wrapper).setValue(edited)
    await button(wrapper, 'save').trigger('click')
    await flushPromises()

    const puts = callsTo(spy, '/config/template', 'PUT')
    expect(puts).toHaveLength(1)
    expect(requestBody(puts[0]![1])).toEqual({ name: 'config/ignition.yaml', content: edited })

    expect(wrapper.find('[data-testid="save-status"]').text()).toBe('Saved config/ignition.yaml.')
    expect(wrapper.find('[data-testid="dirty-indicator"]').exists()).toBe(false)
    expect(editorOf(wrapper).element.value).toBe(edited)
    expect(button(wrapper, 'save').element.disabled).toBe(true)
  })

  it('saves on Ctrl+S from the editor', async () => {
    const { wrapper, spy } = mountConfig()
    await flushPromises()

    await editorOf(wrapper).setValue(TEMPLATE + 'x: 1\n')
    await editorOf(wrapper).trigger('keydown', { key: 's', ctrlKey: true })
    await flushPromises()

    expect(callsTo(spy, '/config/template', 'PUT')).toHaveLength(1)
  })

  it('inserts two spaces on Tab instead of moving focus', async () => {
    const { wrapper } = mountConfig()
    await flushPromises()

    const editor = editorOf(wrapper)
    await editor.setValue('a\nb')
    editor.element.setSelectionRange(1, 1)
    await editor.trigger('keydown', { key: 'Tab' })
    expect(editor.element.value).toBe('a  \nb')
  })

  it('shows the 400 validation error and entries when the server rejects a save', async () => {
    const { wrapper } = mountConfig({
      '/config/template': (init) => {
        if (init?.method === 'PUT') {
          return jsonResponse(
            { error: 'template invalid', entries: [{ kind: 'error', message: 'line 5: bad' }] },
            400
          )
        }
        return jsonResponse(templateDoc())
      }
    })
    await flushPromises()

    await editorOf(wrapper).setValue(TEMPLATE + 'nope\n')
    await button(wrapper, 'save').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="action-error"]').text()).toBe('template invalid')
    const entries = wrapper.findAll('[data-testid="validation-entries"] li')
    expect(entries).toHaveLength(1)
    expect(entries[0]!.text()).toContain('line 5: bad')
    expect(wrapper.find('[data-testid="dirty-indicator"]').exists()).toBe(true)
    expect(editorOf(wrapper).element.value).toBe(TEMPLATE + 'nope\n')
  })

  it('shows the 409 read-only error with its reason', async () => {
    const { wrapper } = mountConfig({
      '/config/template': (init) => {
        if (init?.method === 'PUT') {
          return jsonResponse(
            { error: 'template is read-only', reason: 'mounted from a ConfigMap' },
            409
          )
        }
        return jsonResponse(templateDoc())
      }
    })
    await flushPromises()

    await editorOf(wrapper).setValue(TEMPLATE + 'x: 1\n')
    await button(wrapper, 'save').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="action-error"]').text()).toBe(
      'template is read-only: mounted from a ConfigMap'
    )
    expect(wrapper.find('[data-testid="dirty-indicator"]').exists()).toBe(true)
  })

  it('shows the read-only banner and disables Save but keeps validate/preview available', async () => {
    const { wrapper } = mountConfig({
      '/config/template': () =>
        jsonResponse(templateDoc({ writable: false, readOnlyReason: 'mounted from a ConfigMap' }))
    })
    await flushPromises()

    const banner = wrapper.find('[data-testid="readonly-banner"]')
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain('This template is managed outside Booty')
    expect(banner.text()).toContain('(mounted from a ConfigMap)')
    expect(banner.text()).toContain('You can validate and preview, but not save.')

    await editorOf(wrapper).setValue(TEMPLATE + 'x: 1\n')
    expect(button(wrapper, 'save').element.disabled).toBe(true)
    expect(button(wrapper, 'save').attributes('title')).toBe('This template is read-only')
    expect(button(wrapper, 'validate').element.disabled).toBe(false)
    expect(button(wrapper, 'preview').element.disabled).toBe(false)
    expect(editorOf(wrapper).element.disabled).toBe(false)
  })

  it('falls back to the top-level readOnlyReason from GET /config', async () => {
    const { wrapper } = mountConfig({
      '/config': () => jsonResponse({ ...configBody, readOnlyReason: 'data dir is read-only' }),
      '/config/template': () => jsonResponse(templateDoc({ writable: false }))
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="readonly-banner"]').text()).toContain(
      '(data dir is read-only)'
    )
  })

  it('shows the embedded-template note and refreshes /config after the first save creates the file', async () => {
    let saved = false
    const { wrapper, spy } = mountConfig({
      '/config/template': (init) => {
        if (init?.method === 'PUT') {
          saved = true
          const { content } = requestBody<{ content: string }>(init)
          return jsonResponse(templateDoc({ source: 'file', content }))
        }
        return jsonResponse(templateDoc({ source: saved ? 'file' : 'embedded' }))
      }
    })
    await flushPromises()

    const note = wrapper.find('[data-testid="embedded-note"]')
    expect(note.exists()).toBe(true)
    expect(note.text()).toContain('No config/ignition.yaml yet')
    expect(note.text()).toContain('saving creates the file')
    expect(wrapper.find('[data-testid="readonly-banner"]').exists()).toBe(false)

    await editorOf(wrapper).setValue(TEMPLATE + 'x: 1\n')
    await button(wrapper, 'save').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="embedded-note"]').exists()).toBe(false)
    expect(callsTo(spy, '/config', 'GET')).toHaveLength(2)
  })

  it('still renders settings when /booty.json is unavailable', async () => {
    const { wrapper } = mountConfig({
      '/booty.json': () => jsonResponse({ error: 'boom' }, 500)
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="settings-table"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="preview-host-select"] option')).toHaveLength(1)
  })

  it('shows a template-level error without hiding the settings table', async () => {
    const { wrapper } = mountConfig({
      '/config/template': () => jsonResponse({ error: 'template not found' }, 404)
    })
    await flushPromises()

    expect(wrapper.find('[data-testid="settings-table"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="error-message"]').text()).toBe('template not found')
    expect(wrapper.find('[data-testid="editor"]').exists()).toBe(false)
  })
})
