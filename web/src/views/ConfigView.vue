<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ApiError, apiGet, apiPost, apiPut, errorMessage } from '@/api'
import {
  normalizeBootyData,
  normalizeEffectiveConfig,
  normalizeTemplateDocument,
  normalizeTemplateValidation,
  type BootyData,
  type EffectiveConfig,
  type RawBootyData,
  type RawEffectiveConfig,
  type RawTemplateValidation,
  type SettingSource,
  type TemplateDocument,
  type TemplateValidation,
  type ValidationEntry
} from '@/types'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'
import EmptyState from '@/components/EmptyState.vue'

const SOURCE_BADGE: Record<SettingSource, string> = {
  flag: 'text-bg-dark',
  env: 'text-bg-secondary',
  default: 'text-bg-light border'
}

const INDENT = '  '

type Busy = '' | 'validate' | 'preview' | 'save'

const config = ref<EffectiveConfig>(normalizeEffectiveConfig(null))
const hostData = ref<BootyData>(normalizeBootyData(null))
const loading = ref(true)
const error = ref('')

const filter = ref('')

const selectedTemplate = ref('')
const template = ref<TemplateDocument | null>(null)
const templateLoading = ref(false)
const templateError = ref('')

const content = ref('')
const editor = ref<HTMLTextAreaElement | null>(null)
const cursor = ref({ line: 1, col: 1 })

const previewMac = ref('')
const previewOpen = ref(false)
const validation = ref<TemplateValidation | null>(null)
const validatedContent = ref('')

const busy = ref<Busy>('')
const actionError = ref('')
const actionEntries = ref<ValidationEntry[]>([])
const saveStatus = ref('')

const filteredSettings = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return config.value.settings
  return config.value.settings.filter(
    (s) => s.key.toLowerCase().includes(needle) || s.value.toLowerCase().includes(needle)
  )
})

const templateOptions = computed(() => {
  const { default: def, hosts } = config.value.templates
  const seen = new Set<string>([def.name])
  const options = [{ value: '', label: `Default — ${def.name || 'built-in template'}` }]
  for (const host of hosts) {
    if (seen.has(host.name)) continue
    seen.add(host.name)
    options.push({
      value: host.name,
      label: `${host.hostname || host.mac} (${host.mac}) — ${host.name}`
    })
  }
  return options
})

const previewHosts = computed(() =>
  Object.values(hostData.value.hosts).sort((a, b) =>
    (a.hostname || a.mac).localeCompare(b.hostname || b.mac)
  )
)

const dirty = computed(() => template.value !== null && content.value !== template.value.content)
const writable = computed(() => template.value?.writable ?? false)
const readOnlyReason = computed(
  () => template.value?.readOnlyReason || config.value.readOnlyReason || ''
)
const embedded = computed(() => template.value?.source === 'embedded')

const validationStale = computed(
  () => validation.value !== null && validatedContent.value !== content.value
)
const saveBlockedByValidation = computed(
  () => validation.value !== null && !validation.value.ok && !validationStale.value
)
const canSave = computed(
  () =>
    template.value !== null &&
    writable.value &&
    dirty.value &&
    busy.value === '' &&
    !saveBlockedByValidation.value
)

const lineCount = computed(() => content.value.split('\n').length)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [rawConfig, rawHosts] = await Promise.all([
      apiGet<RawEffectiveConfig>('/config'),
      apiGet<RawBootyData>('/booty.json').catch(() => null)
    ])
    config.value = normalizeEffectiveConfig(rawConfig)
    hostData.value = normalizeBootyData(rawHosts)
    if (!templateOptions.value.some((o) => o.value === selectedTemplate.value)) {
      selectedTemplate.value = ''
    }
    await loadTemplate()
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function templatePath(name: string): string {
  return name ? `/config/template?name=${encodeURIComponent(name)}` : '/config/template'
}

async function loadTemplate() {
  templateLoading.value = true
  templateError.value = ''
  resetFeedback()
  validation.value = null
  previewOpen.value = false
  try {
    const doc = normalizeTemplateDocument(
      await apiGet<Partial<TemplateDocument>>(templatePath(selectedTemplate.value))
    )
    template.value = doc
    content.value = doc.content
    cursor.value = { line: 1, col: 1 }
  } catch (err) {
    template.value = null
    content.value = ''
    templateError.value = errorMessage(err)
  } finally {
    templateLoading.value = false
  }
}

function resetFeedback() {
  actionError.value = ''
  actionEntries.value = []
  saveStatus.value = ''
}

function discard() {
  if (!template.value) return
  content.value = template.value.content
  resetFeedback()
}

async function runValidation(kind: 'validate' | 'preview') {
  if (!template.value || busy.value) return
  busy.value = kind
  resetFeedback()
  const snapshot = content.value
  try {
    const result = normalizeTemplateValidation(
      await apiPost<RawTemplateValidation>('/config/template/validate', {
        name: template.value.name || undefined,
        content: snapshot,
        mac: previewMac.value || undefined
      })
    )
    validation.value = result
    validatedContent.value = snapshot
    if (kind === 'preview') previewOpen.value = true
  } catch (err) {
    actionError.value = errorMessage(err)
  } finally {
    busy.value = ''
  }
}

async function save() {
  if (!canSave.value || !template.value) return
  busy.value = 'save'
  resetFeedback()
  const wasEmbedded = embedded.value
  try {
    const doc = normalizeTemplateDocument(
      await apiPut<Partial<TemplateDocument>>('/config/template', {
        name: template.value.name,
        content: content.value
      })
    )
    template.value = doc
    content.value = doc.content
    validation.value = null
    previewOpen.value = false
    saveStatus.value = `Saved ${doc.name}.`
    if (wasEmbedded && doc.source === 'file') await refreshConfigSummary()
  } catch (err) {
    actionError.value = describeSaveError(err)
    if (err instanceof ApiError && Array.isArray(err.details.entries)) {
      actionEntries.value = normalizeTemplateValidation({
        entries: err.details.entries as RawTemplateValidation['entries']
      }).entries
    }
  } finally {
    busy.value = ''
  }
}

function describeSaveError(err: unknown): string {
  const message = errorMessage(err)
  if (err instanceof ApiError && err.status === 409 && typeof err.details.reason === 'string') {
    return `${message}: ${err.details.reason}`
  }
  return message
}

async function refreshConfigSummary() {
  try {
    config.value = normalizeEffectiveConfig(await apiGet<RawEffectiveConfig>('/config'))
  } catch {
    // the template itself saved fine; the summary refreshes on the next full load
  }
}

function insertAtSelection(el: HTMLTextAreaElement, text: string) {
  const { selectionStart, selectionEnd } = el
  content.value = content.value.slice(0, selectionStart) + text + content.value.slice(selectionEnd)
  const next = selectionStart + text.length
  requestAnimationFrame(() => {
    el.setSelectionRange(next, next)
    updateCursor()
  })
}

function onEditorKeydown(event: KeyboardEvent) {
  const el = event.currentTarget as HTMLTextAreaElement
  if (event.key === 'Tab' && !event.shiftKey && !event.altKey) {
    event.preventDefault()
    insertAtSelection(el, INDENT)
    return
  }
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
    event.preventDefault()
    void save()
  }
}

function updateCursor() {
  const el = editor.value
  if (!el) return
  const before = content.value.slice(0, el.selectionStart)
  const lines = before.split('\n')
  cursor.value = { line: lines.length, col: (lines[lines.length - 1]?.length ?? 0) + 1 }
}

function onBeforeUnload(event: BeforeUnloadEvent) {
  if (!dirty.value) return
  event.preventDefault()
}

watch(selectedTemplate, () => {
  if (!loading.value) void loadTemplate()
})

onMounted(() => {
  window.addEventListener('beforeunload', onBeforeUnload)
  void load()
})

onUnmounted(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>Config</h2>
      <button
        type="button"
        class="btn btn-sm btn-outline-secondary"
        :disabled="loading || dirty"
        :title="dirty ? 'Save or discard your template changes first' : undefined"
        @click="load"
      >
        Refresh
      </button>
    </div>

    <ErrorAlert v-if="error" :message="error" @retry="load" />
    <LoadingState v-if="loading" label="Loading configuration…" />

    <template v-else>
      <div class="section-title section-title--row">
        <span>Effective settings</span>
        <label class="settings-filter">
          <span class="visually-hidden">Filter settings</span>
          <input
            v-model="filter"
            type="search"
            class="form-control form-control-sm"
            placeholder="Filter by key or value"
            data-testid="settings-filter"
          />
        </label>
      </div>
      <div class="panel table-panel fade-in">
        <table
          v-if="filteredSettings.length"
          class="table table-sm table-hover align-middle settings-table"
          data-testid="settings-table"
        >
          <thead>
            <tr>
              <th scope="col">Key</th>
              <th scope="col">Value</th>
              <th scope="col" class="text-end">Source</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="setting in filteredSettings" :key="setting.key" :data-key="setting.key">
              <td class="mono setting-key">{{ setting.key }}</td>
              <td class="setting-value">
                <span
                  v-if="setting.redacted"
                  class="redacted"
                  title="redacted"
                  aria-label="redacted"
                  data-testid="redacted"
                >
                  <svg
                    class="redacted-icon"
                    viewBox="0 0 16 16"
                    width="12"
                    height="12"
                    aria-hidden="true"
                    focusable="false"
                  >
                    <path
                      fill="currentColor"
                      d="M8 1a3.5 3.5 0 0 0-3.5 3.5V6H4a1.5 1.5 0 0 0-1.5 1.5v6A1.5 1.5 0 0 0 4 15h8a1.5 1.5 0 0 0 1.5-1.5v-6A1.5 1.5 0 0 0 12 6h-.5V4.5A3.5 3.5 0 0 0 8 1zm2 5H6V4.5a2 2 0 1 1 4 0V6z"
                    />
                  </svg>
                  <span class="mono">••••</span>
                </span>
                <span v-else-if="setting.value" class="mono setting-text">{{ setting.value }}</span>
                <span v-else class="text-secondary">—</span>
              </td>
              <td class="text-end">
                <span
                  class="badge source-badge"
                  :class="SOURCE_BADGE[setting.source]"
                  :data-source="setting.source"
                >
                  {{ setting.source }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
        <EmptyState
          v-else-if="config.settings.length"
          title="No settings match"
          hint="Try a shorter filter; keys are matched as substrings."
        />
        <EmptyState
          v-else
          title="No settings reported"
          hint="This Booty build does not expose its effective configuration."
        />
      </div>

      <div class="section-title">Ignition template</div>
      <div class="panel editor-panel fade-in">
        <div class="editor-toolbar">
          <label class="toolbar-field">
            <span class="toolbar-label">Template</span>
            <select
              v-model="selectedTemplate"
              class="form-select form-select-sm"
              :disabled="templateLoading || dirty || busy !== ''"
              :title="dirty ? 'Save or discard your changes to switch templates' : undefined"
              data-testid="template-select"
            >
              <option v-for="option in templateOptions" :key="option.value" :value="option.value">
                {{ option.label }}
              </option>
            </select>
          </label>
          <label class="toolbar-field">
            <span class="toolbar-label">Preview as</span>
            <select
              v-model="previewMac"
              class="form-select form-select-sm"
              :disabled="busy !== ''"
              data-testid="preview-host-select"
            >
              <option value="">Dummy host</option>
              <option v-for="host in previewHosts" :key="host.mac" :value="host.mac">
                {{ host.hostname || host.mac }} ({{ host.mac }})
              </option>
            </select>
          </label>
          <div class="toolbar-actions">
            <button
              type="button"
              class="btn btn-sm btn-outline-secondary"
              :disabled="!template || busy !== ''"
              data-action="validate"
              @click="runValidation('validate')"
            >
              <span
                v-if="busy === 'validate'"
                class="spinner-border spinner-border-sm me-1"
                aria-hidden="true"
              ></span>
              Validate
            </button>
            <button
              type="button"
              class="btn btn-sm btn-outline-secondary"
              :disabled="!template || busy !== ''"
              data-action="preview"
              @click="runValidation('preview')"
            >
              <span
                v-if="busy === 'preview'"
                class="spinner-border spinner-border-sm me-1"
                aria-hidden="true"
              ></span>
              Preview
            </button>
            <button
              type="button"
              class="btn btn-sm btn-outline-secondary"
              :disabled="!dirty || busy !== ''"
              data-action="discard"
              @click="discard"
            >
              Discard
            </button>
            <button
              type="button"
              class="btn btn-sm btn-primary"
              :disabled="!canSave"
              :title="
                !writable
                  ? 'This template is read-only'
                  : saveBlockedByValidation
                    ? 'Fix the validation errors first'
                    : undefined
              "
              data-action="save"
              @click="save"
            >
              <span
                v-if="busy === 'save'"
                class="spinner-border spinner-border-sm me-1"
                aria-hidden="true"
              ></span>
              Save
            </button>
          </div>
        </div>

        <LoadingState v-if="templateLoading" label="Loading template…" />
        <div v-else-if="templateError" class="editor-body">
          <ErrorAlert :message="templateError" @retry="loadTemplate" />
        </div>

        <template v-else-if="template">
          <div
            v-if="!writable"
            class="notice notice--warning"
            role="status"
            data-testid="readonly-banner"
          >
            <strong>This template is managed outside Booty</strong>
            <span v-if="readOnlyReason"> ({{ readOnlyReason }})</span>. You can validate and
            preview, but not save.
          </div>
          <div v-if="embedded" class="notice notice--info" role="note" data-testid="embedded-note">
            No <span class="mono">config/ignition.yaml</span> yet — this is Booty's built-in
            template; saving creates the file.
          </div>

          <div class="editor-grid" :class="{ 'editor-grid--preview': previewOpen }">
            <div class="editor-column">
              <div class="editor-meta">
                <span class="mono editor-name" :title="template.name">{{
                  template.name || 'built-in template'
                }}</span>
                <span v-if="dirty" class="badge text-bg-warning" data-testid="dirty-indicator"
                  >Unsaved changes</span
                >
                <span v-else class="badge text-bg-light border">Saved</span>
                <span class="editor-cursor small text-secondary" data-testid="cursor">
                  Ln {{ cursor.line }}, Col {{ cursor.col }} · {{ lineCount }} lines
                </span>
              </div>
              <label class="visually-hidden" for="template-editor">Butane template</label>
              <textarea
                id="template-editor"
                ref="editor"
                v-model="content"
                class="form-control mono editor"
                spellcheck="false"
                autocapitalize="off"
                autocomplete="off"
                wrap="off"
                :disabled="busy === 'save'"
                data-testid="editor"
                @keydown="onEditorKeydown"
                @keyup="updateCursor"
                @click="updateCursor"
                @input="updateCursor"
              ></textarea>
              <div class="editor-hint small text-secondary">
                Tab inserts two spaces · Ctrl/Cmd+S saves · Variables:
                <span class="mono">.Hostname .ServerIP .JoinString .OSTreeImage</span>
              </div>
            </div>

            <div v-if="previewOpen" class="preview-column" data-testid="ignition-preview">
              <div class="editor-meta">
                <span class="toolbar-label">Rendered Ignition</span>
                <span class="small text-secondary">
                  {{ previewMac ? `as ${previewMac}` : 'as dummy host' }}
                </span>
                <button
                  type="button"
                  class="btn btn-sm btn-link ms-auto p-0"
                  data-action="close-preview"
                  @click="previewOpen = false"
                >
                  Hide
                </button>
              </div>
              <pre
                v-if="validation?.ignition"
                class="preview mono"
                tabindex="0"
                aria-label="Rendered Ignition JSON"
                >{{ validation.ignition }}</pre>
              <div v-else class="preview preview--empty small text-secondary">
                Nothing rendered — the template did not translate to Ignition.
              </div>
            </div>
          </div>

          <div
            v-if="actionError || actionEntries.length || saveStatus || validation"
            class="editor-feedback"
          >
            <div v-if="actionError" class="row-error" data-testid="action-error">
              {{ actionError }}
            </div>
            <div v-if="saveStatus" class="small text-success" data-testid="save-status">
              {{ saveStatus }}
            </div>
            <div v-if="validation" class="validation-summary" data-testid="validation-summary">
              <span
                class="badge"
                :class="validation.ok ? 'text-bg-success' : 'text-bg-danger'"
                :data-ok="validation.ok"
              >
                {{ validation.ok ? 'Valid' : 'Invalid' }}
              </span>
              <span class="small text-secondary">
                {{ validation.entries.length }}
                {{ validation.entries.length === 1 ? 'entry' : 'entries' }}
                <template v-if="validationStale"> · edited since — re-run Validate</template>
              </span>
            </div>
            <ul
              v-if="validation?.entries.length || actionEntries.length"
              class="entries"
              data-testid="validation-entries"
            >
              <li
                v-for="(entry, i) in [...(validation?.entries ?? []), ...actionEntries]"
                :key="`${entry.kind}-${i}`"
                class="entry"
                :class="`entry--${entry.kind}`"
                :data-kind="entry.kind"
              >
                <span class="entry-kind">{{ entry.kind }}</span>
                <span class="entry-message">{{ entry.message }}</span>
              </li>
            </ul>
          </div>
        </template>
      </div>
    </template>
  </div>
</template>

<style scoped>
.section-title--row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--booty-space-3);
}

.settings-filter {
  margin: 0;
  width: min(100%, 18rem);
  text-transform: none;
  letter-spacing: normal;
}

.settings-table th,
.settings-table td {
  padding-left: var(--booty-space-3);
  padding-right: var(--booty-space-3);
}

.setting-key {
  white-space: nowrap;
  width: 1%;
}

.setting-value {
  max-width: 0;
}

.setting-text {
  display: block;
  overflow-wrap: anywhere;
}

.redacted {
  display: inline-flex;
  align-items: center;
  gap: var(--booty-space-1);
  color: var(--booty-muted);
}

.redacted-icon {
  flex-shrink: 0;
}

.source-badge {
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-size: 0.6875rem;
}

.editor-panel {
  overflow: hidden;
}

.editor-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--booty-space-2) var(--booty-space-3);
  padding: var(--booty-space-3);
  border-bottom: 1px solid var(--booty-border);
  background: var(--booty-surface-alt);
}

.toolbar-field {
  display: flex;
  flex-direction: column;
  gap: var(--booty-space-1);
  margin: 0;
  flex: 1 1 14rem;
  min-width: 0;
}

.toolbar-label {
  font-size: 0.75rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--booty-muted);
}

.toolbar-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--booty-space-2);
  margin-left: auto;
}

.editor-body {
  padding: var(--booty-space-3) var(--booty-space-3) 0;
}

.notice {
  padding: var(--booty-space-2) var(--booty-space-3);
  border-bottom: 1px solid var(--booty-border);
  border-left: 3px solid var(--booty-border);
  font-size: 0.875rem;
}

.notice--warning {
  border-left-color: var(--bs-warning);
  background: var(--bs-warning-bg-subtle);
  color: var(--bs-warning-text-emphasis);
}

.notice--info {
  border-left-color: var(--booty-accent);
  background: var(--booty-accent-soft);
}

.editor-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: var(--booty-space-3);
  padding: var(--booty-space-3);
}

@media (min-width: 992px) {
  .editor-grid--preview {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  }
}

.editor-column,
.preview-column {
  display: flex;
  flex-direction: column;
  gap: var(--booty-space-2);
  min-width: 0;
}

.editor-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-2);
}

.editor-name {
  color: var(--booty-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 24rem;
}

.editor-cursor {
  margin-left: auto;
  font-variant-numeric: tabular-nums;
}

.editor {
  min-height: 28rem;
  resize: vertical;
  tab-size: 2;
  white-space: pre;
  overflow: auto;
  line-height: 1.5;
  font-size: 0.8125rem;
  background: var(--booty-surface);
}

.editor:focus {
  border-color: var(--booty-accent);
  box-shadow: 0 0 0 0.25rem var(--booty-accent-soft);
}

.editor-hint {
  color: var(--booty-muted);
}

.preview {
  flex: 1 1 auto;
  min-height: 28rem;
  max-height: 40rem;
  margin: 0;
  padding: var(--booty-space-2) var(--booty-space-3);
  background: var(--booty-nav-bg);
  color: #e6e8eb;
  border-radius: var(--booty-radius);
  font-size: 0.8125rem;
  line-height: 1.5;
  overflow: auto;
}

.preview--empty {
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--booty-surface-alt);
  color: var(--booty-muted);
  border: 1px dashed var(--booty-border);
}

.editor-feedback {
  display: flex;
  flex-direction: column;
  gap: var(--booty-space-2);
  padding: 0 var(--booty-space-3) var(--booty-space-3);
}

.editor-feedback .row-error {
  margin-top: 0;
}

.validation-summary {
  display: flex;
  align-items: center;
  gap: var(--booty-space-2);
}

.entries {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--booty-space-1);
}

.entry {
  display: flex;
  gap: var(--booty-space-2);
  align-items: baseline;
  padding: var(--booty-space-1) var(--booty-space-2);
  border-left: 3px solid var(--booty-border);
  background: var(--booty-surface-alt);
  border-radius: 0 var(--booty-radius) var(--booty-radius) 0;
  font-size: 0.875rem;
}

.entry-kind {
  font-size: 0.6875rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  flex-shrink: 0;
  min-width: 4rem;
}

.entry--error {
  border-left-color: var(--bs-danger);
}

.entry--error .entry-kind {
  color: var(--bs-danger);
}

.entry--warning {
  border-left-color: var(--bs-warning);
}

.entry--warning .entry-kind {
  color: var(--bs-warning-text-emphasis);
}

.entry-message {
  font-family: var(--booty-mono);
  font-size: 0.8125rem;
  overflow-wrap: anywhere;
}
</style>
