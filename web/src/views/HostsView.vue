<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { apiGet, apiPost, errorMessage } from '@/api'
import {
  normalizeBootyData,
  type BootyData,
  type Host,
  type RegisterResponse,
  type StatusResponse,
  type UnknownHost
} from '@/types'
import { formatAbsolute, formatRelative } from '@/utils/time'
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
    hostData.value = normalizeBootyData(await apiGet<Partial<BootyData>>('/booty.json'))
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
    doInstall: false
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
    const saved = host ?? draft
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

function ignitionUrl(mac: string) {
  return `/ignition.json?mac=${encodeURIComponent(mac)}&preview=1`
}

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
              <th scope="col">Hostname</th>
              <th scope="col">IP</th>
              <th scope="col">OS</th>
              <th scope="col">Ignition</th>
              <th scope="col">OSTree image</th>
              <th scope="col">Install</th>
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
                    :href="ignitionUrl(mac)"
                    target="_blank"
                    rel="noopener noreferrer"
                    title="Open rendered Ignition config"
                  >
                    {{ mac }}
                  </a>
                </td>
                <td>{{ host.hostname || '—' }}</td>
                <td class="mono">{{ host.ip || '—' }}</td>
                <td>
                  <span v-if="host.os" class="badge text-bg-light border">{{ host.os }}</span>
                  <span v-else class="text-secondary">default</span>
                </td>
                <td class="mono">{{ host.ignitionFile || '—' }}</td>
                <td>
                  <span
                    v-if="host.ostreeImage"
                    class="mono truncate ostree"
                    :title="host.ostreeImage"
                  >
                    {{ host.ostreeImage }}
                  </span>
                  <span v-else class="text-secondary">—</span>
                </td>
                <td>
                  <span v-if="host.doInstall" class="badge text-bg-warning">Yes</span>
                  <span v-else class="text-secondary">No</span>
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
                <td colspan="9" class="pt-0 border-top-0">
                  <div class="row-error" data-testid="row-error">{{ rowErrors[mac] }}</div>
                </td>
              </tr>
              <tr v-if="drafts[mac]" class="table-active" :data-mac-edit="mac">
                <td colspan="9" class="pt-0 border-top-0">
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
                    :href="ignitionUrl(mac)"
                    target="_blank"
                    rel="noopener noreferrer"
                    title="Open rendered Ignition config"
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
.ostree {
  max-width: 18rem;
}
</style>
