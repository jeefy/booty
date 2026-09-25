<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { apiGet, apiPost, errorMessage } from '@/api'
import {
  normalizeBootyData,
  normalizeHost,
  type BootyData,
  type Host,
  type RawBootyData,
  type RegisterResponse,
  type StatusResponse,
  type UnknownHost
} from '@/types'
import { formatAbsolute, formatRelative } from '@/utils/time'
import {
  HOST_STATUS_LABEL,
  hostStatus,
  ignitionPreviewUrl,
  splitRunning,
  type RunningLabel
} from '@/utils/fleet'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'
import EmptyState from '@/components/EmptyState.vue'
import HostForm from '@/components/HostForm.vue'

const hostData = ref<BootyData>(normalizeBootyData(null))
const loading = ref(true)
const error = ref('')

const drafts = reactive<Record<string, Host>>({})
const rowErrors = reactive<Record<string, string>>({})
const busy = reactive<Record<string, boolean>>({})
const confirmingDelete = reactive<Record<string, boolean>>({})

const hosts = computed(() =>
  Object.entries(hostData.value.hosts).sort(([, a], [, b]) =>
    (a.hostname || a.mac).localeCompare(b.hostname || b.mac)
  )
)
const unknownHosts = computed(() =>
  Object.entries(hostData.value.unknownHosts).sort(
    ([, a], [, b]) => new Date(b.lastSeen).getTime() - new Date(a.lastSeen).getTime()
  )
)

function clearRowState(mac: string) {
  delete drafts[mac]
  delete rowErrors[mac]
  delete busy[mac]
  delete confirmingDelete[mac]
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    hostData.value = normalizeBootyData(await apiGet<RawBootyData>('/booty.json'))
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function startEdit(mac: string, host: Host) {
  delete rowErrors[mac]
  delete confirmingDelete[mac]
  drafts[mac] = { ...host, mac }
}

function startRegister(mac: string, unknown: UnknownHost) {
  delete rowErrors[mac]
  drafts[mac] = {
    mac,
    hostname: '',
    ip: unknown.ip,
    booted: '',
    ignitionFile: '',
    os: '',
    ostreeImage: '',
    installDisk: '',
    doInstall: false,
    running: '',
    lastCheck: '',
    rebootPending: false
  }
}

function cancel(mac: string) {
  clearRowState(mac)
}

async function save(mac: string) {
  const draft = drafts[mac]
  if (!draft) return
  busy[mac] = true
  rowErrors[mac] = ''
  try {
    const { host } = await apiPost<RegisterResponse>('/register', draft)
    const saved = normalizeHost(host ?? draft)
    const nextHosts = { ...hostData.value.hosts }
    if (mac !== saved.mac) delete nextHosts[mac]
    nextHosts[saved.mac] = saved
    const nextUnknown = { ...hostData.value.unknownHosts }
    delete nextUnknown[mac]
    delete nextUnknown[saved.mac]
    hostData.value = { hosts: nextHosts, unknownHosts: nextUnknown }
    clearRowState(mac)
  } catch (err) {
    rowErrors[mac] = errorMessage(err)
    busy[mac] = false
  }
}

async function remove(mac: string) {
  busy[mac] = true
  rowErrors[mac] = ''
  try {
    await apiPost<StatusResponse>('/unregister', { mac })
    const nextHosts = { ...hostData.value.hosts }
    delete nextHosts[mac]
    hostData.value = { ...hostData.value, hosts: nextHosts }
    clearRowState(mac)
  } catch (err) {
    rowErrors[mac] = errorMessage(err)
    busy[mac] = false
    delete confirmingDelete[mac]
  }
}

const runningLabels = computed(() => {
  const labels: Record<string, RunningLabel> = {}
  for (const [mac, host] of hosts.value) {
    const label = splitRunning(host.running)
    if (label) labels[mac] = label
  }
  return labels
})

onMounted(() => {
  void load()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>Hosts</h2>
      <button
        type="button"
        class="btn btn-sm btn-outline-secondary"
        :disabled="loading"
        @click="load"
      >
        Refresh
      </button>
    </div>

    <ErrorAlert v-if="error" :message="error" @retry="load" />
    <LoadingState v-if="loading" label="Loading hosts…" />

    <template v-else>
      <div class="section-title">Registered</div>
      <div class="panel table-panel fade-in">
        <table v-if="hosts.length" class="table table-hover align-middle" data-testid="hosts-table">
          <thead>
            <tr>
              <th scope="col">MAC</th>
              <th scope="col">Host</th>
              <th scope="col">IP</th>
              <th scope="col">OS</th>
              <th scope="col">Running</th>
              <th scope="col">Last check</th>
              <th scope="col">Last boot</th>
              <th scope="col" class="text-end">Actions</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="[mac, host] in hosts" :key="mac">
              <tr :data-mac="mac" :class="{ 'table-active': drafts[mac] }">
                <td>
                  <a
                    class="mono"
                    :href="ignitionPreviewUrl(mac)"
                    target="_blank"
                    rel="noopener noreferrer"
                    title="Open merged Ignition preview (builtin + user config)"
                    data-preview="merged"
                  >
                    {{ mac }}
                  </a>
                  <div class="preview-parts small">
                    <a
                      :href="ignitionPreviewUrl(mac, 'user')"
                      target="_blank"
                      rel="noopener noreferrer"
                      title="Preview only the user's Ignition config"
                      data-preview="user"
                      >user config</a
                    >
                    <span aria-hidden="true">·</span>
                    <a
                      :href="ignitionPreviewUrl(mac, 'builtin')"
                      target="_blank"
                      rel="noopener noreferrer"
                      title="Preview only Booty's builtin Ignition fragment"
                      data-preview="builtin"
                      >builtin</a
                    >
                  </div>
                </td>
                <td>
                  <div class="host-name">
                    <span>{{ host.hostname || '—' }}</span>
                    <span
                      class="badge"
                      :class="HOST_STATUS_LABEL[hostStatus(host)].badge"
                      :data-status="hostStatus(host)"
                      data-testid="host-status"
                    >
                      {{ HOST_STATUS_LABEL[hostStatus(host)].text }}
                    </span>
                  </div>
                  <div class="host-config small text-secondary" data-testid="host-config">
                    <span class="mono" title="Ignition file">{{
                      host.ignitionFile || 'default ignition'
                    }}</span>
                    <span
                      v-if="host.ostreeImage"
                      class="mono truncate ostree"
                      :title="`OSTree image: ${host.ostreeImage}`"
                    >
                      {{ host.ostreeImage }}
                    </span>
                    <span
                      v-if="host.installDisk"
                      class="mono install-disk"
                      :title="`Install disk: ${host.installDisk} (wiped on install)`"
                      data-testid="host-install-disk"
                    >
                      disk {{ host.installDisk }}
                    </span>
                    <span v-if="host.doInstall" class="badge text-bg-warning">Install</span>
                  </div>
                </td>
                <td class="mono">{{ host.ip || '—' }}</td>
                <td>
                  <span v-if="host.os" class="badge text-bg-light border">{{ host.os }}</span>
                  <span v-else class="text-secondary">default</span>
                </td>
                <td data-testid="host-running">
                  <template v-if="runningLabels[mac]">
                    <div class="mono truncate running" :title="host.running">
                      {{ runningLabels[mac].image }}
                    </div>
                    <div
                      v-if="runningLabels[mac].digest"
                      class="mono small text-secondary"
                      :title="runningLabels[mac].digest"
                    >
                      @{{ runningLabels[mac].shortDigest }}
                    </div>
                  </template>
                  <span v-else class="text-secondary">—</span>
                </td>
                <td data-testid="host-last-check">
                  <span :title="formatAbsolute(host.lastCheck)">{{
                    formatRelative(host.lastCheck)
                  }}</span>
                </td>
                <td>
                  <span :title="formatAbsolute(host.booted)">{{
                    formatRelative(host.booted)
                  }}</span>
                </td>
                <td class="text-end text-nowrap">
                  <template v-if="!drafts[mac] && !confirmingDelete[mac]">
                    <button
                      type="button"
                      class="btn btn-sm btn-outline-secondary me-1"
                      :disabled="busy[mac]"
                      data-action="edit"
                      @click="startEdit(mac, host)"
                    >
                      Edit
                    </button>
                    <button
                      type="button"
                      class="btn btn-sm btn-outline-danger"
                      :disabled="busy[mac]"
                      data-action="delete"
                      @click="confirmingDelete[mac] = true"
                    >
                      Delete
                    </button>
                  </template>
                  <template v-else-if="confirmingDelete[mac]">
                    <span class="small text-secondary me-2"
                      >Unregister {{ host.hostname || mac }}?</span
                    >
                    <button
                      type="button"
                      class="btn btn-sm btn-outline-secondary me-1"
                      :disabled="busy[mac]"
                      data-action="delete-cancel"
                      @click="cancel(mac)"
                    >
                      Keep
                    </button>
                    <button
                      type="button"
                      class="btn btn-sm btn-danger"
                      :disabled="busy[mac]"
                      data-action="delete-confirm"
                      @click="remove(mac)"
                    >
                      <span
                        v-if="busy[mac]"
                        class="spinner-border spinner-border-sm me-1"
                        aria-hidden="true"
                      ></span>
                      Unregister
                    </button>
                  </template>
                  <span v-else class="small text-secondary">Editing…</span>
                </td>
              </tr>
              <tr v-if="rowErrors[mac] && !drafts[mac]" :data-mac-error="mac">
                <td colspan="8" class="pt-0 border-top-0">
                  <div class="row-error" data-testid="row-error">{{ rowErrors[mac] }}</div>
                </td>
              </tr>
              <tr v-if="drafts[mac]" class="table-active" :data-mac-edit="mac">
                <td colspan="8" class="pt-0 border-top-0">
                  <HostForm
                    v-model="drafts[mac]"
                    :busy="Boolean(busy[mac])"
                    :error="rowErrors[mac] ?? ''"
                    submit-label="Save"
                    @submit="save(mac)"
                    @cancel="cancel(mac)"
                  />
                </td>
              </tr>
            </template>
          </tbody>
        </table>
        <EmptyState
          v-else
          title="No hosts registered yet"
          hint="Machines that PXE boot against Booty show up below until you register them."
        />
      </div>

      <div class="section-title">Unregistered (in the brig)</div>
      <div class="panel table-panel fade-in">
        <table
          v-if="unknownHosts.length"
          class="table table-hover align-middle"
          data-testid="unknown-table"
        >
          <thead>
            <tr>
              <th scope="col">MAC</th>
              <th scope="col">IP</th>
              <th scope="col">First seen</th>
              <th scope="col">Last seen</th>
              <th scope="col">Attempts</th>
              <th scope="col" class="text-end">Actions</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="[mac, unknown] in unknownHosts" :key="mac">
              <tr :data-mac="mac" :class="{ 'table-active': drafts[mac] }">
                <td>
                  <a
                    class="mono"
                    :href="ignitionPreviewUrl(mac)"
                    target="_blank"
                    rel="noopener noreferrer"
                    title="Open merged Ignition preview"
                    data-preview="merged"
                  >
                    {{ mac }}
                  </a>
                </td>
                <td class="mono">{{ unknown.ip || '—' }}</td>
                <td>
                  <span :title="formatAbsolute(unknown.firstSeen)">
                    {{ formatRelative(unknown.firstSeen) }}
                  </span>
                </td>
                <td>
                  <span :title="formatAbsolute(unknown.lastSeen)">
                    {{ formatRelative(unknown.lastSeen) }}
                  </span>
                </td>
                <td>{{ unknown.count }}</td>
                <td class="text-end text-nowrap">
                  <button
                    v-if="!drafts[mac]"
                    type="button"
                    class="btn btn-sm btn-primary"
                    data-action="register"
                    @click="startRegister(mac, unknown)"
                  >
                    Register
                  </button>
                  <span v-else class="small text-secondary">Registering…</span>
                </td>
              </tr>
              <tr v-if="drafts[mac]" class="table-active" :data-mac-edit="mac">
                <td colspan="6" class="pt-0 border-top-0">
                  <HostForm
                    v-model="drafts[mac]"
                    :busy="Boolean(busy[mac])"
                    :error="rowErrors[mac] ?? ''"
                    submit-label="Register host"
                    @submit="save(mac)"
                    @cancel="cancel(mac)"
                  />
                </td>
              </tr>
            </template>
          </tbody>
        </table>
        <EmptyState
          v-else
          title="Nothing in the brig"
          hint="Unrecognised MACs that try to boot will be listed here."
        />
      </div>
    </template>
  </div>
</template>

<style scoped>
.preview-parts {
  display: flex;
  gap: var(--booty-space-1);
  margin-top: var(--booty-space-1);
  color: var(--booty-muted);
}

.preview-parts a {
  text-decoration: none;
}

.preview-parts a:hover {
  text-decoration: underline;
}

.host-name {
  display: flex;
  align-items: center;
  gap: var(--booty-space-2);
}

.host-config {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-1) var(--booty-space-2);
  margin-top: var(--booty-space-1);
}

.ostree {
  max-width: 12rem;
}

.install-disk {
  color: var(--booty-muted);
  white-space: nowrap;
}

.running {
  max-width: 12rem;
}
</style>
