<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ApiError, apiGet, apiPost, errorMessage } from '@/api'
import {
  normalizeBootyData,
  normalizeClusterInfo,
  normalizeSecureBootInfo,
  type BootyData,
  type ClusterInfo,
  type Info,
  type PinState,
  type RawBootyData,
  type RawClusterInfo
} from '@/types'
import {
  bluefinVersion,
  fleetSummary,
  pendingHosts,
  splitRunning,
  targetVersion
} from '@/utils/fleet'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'

const REFRESH_INTERVAL_MS = 30_000
const COPIED_FEEDBACK_MS = 1_500

const hostData = ref<BootyData>(normalizeBootyData(null))
const info = ref<Info>({})
const pin = ref<PinState>({ pinned: false, version: '', current: '' })
const cluster = ref<ClusterInfo | null>(null)
const clusterError = ref('')
const fingerprintCopied = ref(false)
const caCopied = ref(false)

const loading = ref(true)
const error = ref('')

const pinInput = ref('')
const pinBusy = ref(false)
const pinStatus = ref('')
const pinError = ref('')

const hostCount = computed(() => Object.keys(hostData.value.hosts).length)
const unknownCount = computed(() => Object.keys(hostData.value.unknownHosts).length)
const flatcarVersion = computed(() => info.value.flatcar?.version || '')
const coreosVersion = computed(() => info.value.coreos?.version || '')
const bluefin = computed(() => ({
  version: bluefinVersion(info.value),
  pinnedVersion: info.value.bluefin?.pinnedVersion || ''
}))
const bootyVersion = computed(() => info.value.booty?.version || '')
const fleet = computed(() => fleetSummary(hostData.value, info.value))
const pending = computed(() =>
  pendingHosts(hostData.value).map((host) => ({
    mac: host.mac,
    hostname: host.hostname || host.mac,
    running: splitRunning(host.running),
    target: targetVersion(host, info.value)
  }))
)
const secureBoot = computed(() => normalizeSecureBootInfo(info.value.secureBoot))
const secureBootState = computed(() => {
  if (!secureBoot.value.enabled) return 'off'
  return secureBoot.value.ready ? 'ready' : 'syncing artefacts'
})
const clusterRoles = computed(() => {
  const hosts = cluster.value?.hosts ?? []
  const controlPlane = hosts.filter((h) => h.role === 'control-plane').length
  return { controlPlane, workers: hosts.length - controlPlane }
})
const clusterState = computed(() => {
  const c = cluster.value
  if (!c) return ''
  if (c.source === 'external') return c.connected ? 'connected' : 'unreachable'
  return c.ready ? 'bootstrapped' : 'not ready yet'
})

async function loadCluster() {
  try {
    cluster.value = normalizeClusterInfo(await apiGet<RawClusterInfo>('/cluster'))
    clusterError.value = ''
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      cluster.value = null
      clusterError.value = ''
      return
    }
    clusterError.value = errorMessage(err)
  }
}

async function load(showSpinner = true) {
  if (showSpinner) loading.value = true
  error.value = ''
  const clusterRequest = loadCluster()
  try {
    const [data, infoData, pinData] = await Promise.all([
      apiGet<RawBootyData>('/booty.json'),
      apiGet<Info>('/info'),
      apiGet<PinState>('/flatcar/pin')
    ])
    hostData.value = normalizeBootyData(data)
    info.value = infoData ?? {}
    pin.value = pinData
    if (!pinBusy.value) pinInput.value = pinData.version || ''
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
  await clusterRequest
}

let copiedTimer: ReturnType<typeof setTimeout> | undefined
let caCopiedTimer: ReturnType<typeof setTimeout> | undefined

async function copyToClipboard(value: string): Promise<boolean> {
  if (!value || !navigator.clipboard) return false
  try {
    await navigator.clipboard.writeText(value)
  } catch {
    return false
  }
  return true
}

async function copyFingerprint() {
  if (!(await copyToClipboard(cluster.value?.caFingerprint ?? ''))) return
  fingerprintCopied.value = true
  if (copiedTimer) clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => {
    fingerprintCopied.value = false
  }, COPIED_FEEDBACK_MS)
}

async function copyFlatcarCA() {
  if (!(await copyToClipboard(secureBoot.value.flatcarCA?.sha256 ?? ''))) return
  caCopied.value = true
  if (caCopiedTimer) clearTimeout(caCopiedTimer)
  caCopiedTimer = setTimeout(() => {
    caCopied.value = false
  }, COPIED_FEEDBACK_MS)
}

async function savePin(version: string) {
  pinBusy.value = true
  pinStatus.value = ''
  pinError.value = ''
  try {
    const result = await apiPost<PinState>('/flatcar/pin', { version })
    pin.value = result
    pinInput.value = result.version || ''
    pinStatus.value = result.pinned
      ? `Pinned to ${result.version}. Downloading in the background.`
      : 'Pin cleared. Tracking the latest channel release.'
    await load(false)
  } catch (err) {
    pinError.value = errorMessage(err)
  } finally {
    pinBusy.value = false
  }
}

function setPin() {
  const version = pinInput.value.trim()
  if (!version) return
  void savePin(version)
}

function clearPin() {
  void savePin('')
}

let timer: ReturnType<typeof setInterval> | undefined

onMounted(() => {
  void load()
  timer = setInterval(() => void load(false), REFRESH_INTERVAL_MS)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
  if (copiedTimer) clearTimeout(copiedTimer)
  if (caCopiedTimer) clearTimeout(caCopiedTimer)
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>Overview</h2>
      <span class="small text-secondary">Refreshes every 30s</span>
    </div>

    <ErrorAlert v-if="error" :message="error" @retry="load()" />
    <LoadingState v-if="loading" label="Loading status…" />

    <template v-else>
      <div class="stat-grid fade-in">
        <div class="panel stat">
          <div class="stat-label">Flatcar</div>
          <div class="stat-value">{{ flatcarVersion || '—' }}</div>
          <div class="stat-hint">
            <span v-if="pin.pinned" class="badge text-bg-warning">Pinned</span>
            <span v-else-if="flatcarVersion" class="badge text-bg-success">Tracking latest</span>
            <span v-else>Not polled yet</span>
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">CoreOS</div>
          <div class="stat-value">{{ coreosVersion || '—' }}</div>
          <div class="stat-hint">{{ coreosVersion ? 'Tracking latest' : 'Not polled yet' }}</div>
        </div>
        <div class="panel stat" data-testid="bluefin-stat">
          <div class="stat-label">Bluefin</div>
          <div class="stat-value" :title="bluefin.version ? undefined : 'not downloaded yet'">
            {{ bluefin.version || '—' }}
          </div>
          <div class="stat-hint">
            <template v-if="bluefin.pinnedVersion">
              <span class="badge text-bg-warning">Pinned</span>
              <span class="mono ms-1">{{ bluefin.pinnedVersion }}</span>
            </template>
            <span v-else-if="bluefin.version" class="badge text-bg-success">Tracking latest</span>
            <span v-else>Not downloaded yet</span>
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Hosts</div>
          <div class="stat-value">
            <RouterLink to="/hosts">{{ hostCount }}</RouterLink>
          </div>
          <div class="stat-hint">
            {{ unknownCount }} unregistered
            <RouterLink v-if="unknownCount" to="/hosts">(review)</RouterLink>
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Booty</div>
          <div class="stat-value">{{ bootyVersion || '—' }}</div>
          <div class="stat-hint">
            <RouterLink to="/about">Build details</RouterLink>
          </div>
        </div>
      </div>

      <div class="section-title">Fleet</div>
      <div
        class="panel fleet-panel fade-in"
        :class="{ 'fleet-panel--alert': fleet.pendingReboots > 0 }"
        data-testid="fleet-card"
      >
        <div class="fleet-stats">
          <div class="fleet-stat">
            <div class="stat-label">Hosts</div>
            <div class="stat-value" data-testid="fleet-hosts">{{ fleet.hosts }}</div>
          </div>
          <div class="fleet-stat">
            <div class="stat-label">Pending reboots</div>
            <div
              class="stat-value"
              :class="{ 'fleet-pending': fleet.pendingReboots > 0 }"
              data-testid="fleet-pending"
            >
              {{ fleet.pendingReboots }}
            </div>
          </div>
          <div class="fleet-stat fleet-source stat-hint">
            <template v-if="fleet.fromServer">Reported by /info</template>
            <template v-else>Derived from host records</template>
          </div>
        </div>
        <table v-if="pending.length" class="table table-sm fleet-table mb-0" data-testid="fleet-list">
          <thead>
            <tr>
              <th scope="col">Host</th>
              <th scope="col">Running</th>
              <th scope="col">Target</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in pending" :key="row.mac" :data-mac="row.mac">
              <td>
                <RouterLink to="/hosts">{{ row.hostname }}</RouterLink>
              </td>
              <td>
                <span v-if="row.running" class="mono truncate fleet-version" :title="row.running.full">{{
                  row.running.short
                }}</span>
                <span v-else class="text-secondary">—</span>
              </td>
              <td>
                <span v-if="row.target" class="mono truncate fleet-version" :title="row.target">{{
                  row.target
                }}</span>
                <span v-else class="text-secondary">—</span>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else class="fleet-empty small text-secondary" data-testid="fleet-empty">
          Every host is on its target version.
        </div>
      </div>

      <template v-if="cluster || clusterError">
        <div class="section-title">Cluster</div>
        <div
          v-if="cluster"
          class="panel cluster-panel fade-in"
          :class="{ 'cluster-panel--ready': cluster.ready }"
          data-testid="cluster-card"
        >
          <div class="cluster-head">
            <span
              class="status-dot"
              :class="{ 'status-dot--ready': cluster.ready }"
              aria-hidden="true"
            ></span>
            <span class="cluster-state" data-testid="cluster-state">
              {{ clusterState }}
            </span>
            <span class="cluster-roles stat-hint" data-testid="cluster-roles">
              {{ clusterRoles.controlPlane }} control-plane ·
              {{ clusterRoles.workers }} {{ clusterRoles.workers === 1 ? 'worker' : 'workers' }}
            </span>
          </div>
          <dl class="cluster-facts" data-testid="cluster-facts">
            <dt class="stat-label">Distribution</dt>
            <dd class="mono">{{ cluster.distribution }}</dd>
            <dt class="stat-label">Control plane</dt>
            <dd class="mono">{{ cluster.controlPlane }}</dd>
            <dt class="stat-label">Endpoint</dt>
            <dd>
              <span v-if="cluster.endpoint" class="mono truncate cluster-endpoint" :title="cluster.endpoint">{{
                cluster.endpoint
              }}</span>
              <span v-else class="text-secondary">—</span>
            </dd>
            <template v-if="cluster.apiServer">
              <dt class="stat-label">API server</dt>
              <dd>
                <span class="mono truncate cluster-endpoint" :title="cluster.apiServer" data-testid="cluster-api-server">{{
                  cluster.apiServer
                }}</span>
              </dd>
            </template>
            <dt class="stat-label">CNI</dt>
            <dd class="mono">{{ cluster.cni }}</dd>
            <template v-if="cluster.nodes > 0">
              <dt class="stat-label">Nodes</dt>
              <dd class="mono" data-testid="cluster-nodes">{{ cluster.nodes }}</dd>
            </template>
            <dt class="stat-label">CA fingerprint</dt>
            <dd class="cluster-fingerprint">
              <template v-if="cluster.caFingerprint">
                <span class="mono truncate cluster-fingerprint-value" :title="cluster.caFingerprint" data-testid="cluster-fingerprint">{{
                  cluster.caFingerprint
                }}</span>
                <button
                  type="button"
                  class="btn btn-sm btn-outline-secondary cluster-copy"
                  :title="fingerprintCopied ? 'Copied' : 'Copy CA fingerprint'"
                  data-testid="cluster-copy"
                  @click="copyFingerprint"
                >
                  {{ fingerprintCopied ? 'Copied' : 'Copy' }}
                </button>
              </template>
              <span v-else class="text-secondary">—</span>
            </dd>
          </dl>
          <div
            v-if="cluster.warnings.length"
            class="alert alert-warning cluster-warnings mb-0"
            role="status"
            data-testid="cluster-warnings"
          >
            <strong class="me-1">Needs attention.</strong>
            <ul class="cluster-warning-list mb-0">
              <li v-for="(warning, index) in cluster.warnings" :key="index">{{ warning }}</li>
            </ul>
          </div>
        </div>
        <div
          v-else
          class="alert alert-warning fade-in"
          role="status"
          data-testid="cluster-unavailable"
        >
          <strong class="me-1">Cluster status unavailable.</strong>
          <span>{{ clusterError }}</span>
        </div>
      </template>

      <div class="section-title">Secure Boot</div>
      <div
        class="panel secure-boot-panel fade-in"
        :class="{
          'secure-boot-panel--ready': secureBoot.enabled && secureBoot.ready,
          'secure-boot-panel--alert': secureBoot.warnings.length > 0
        }"
        data-testid="secure-boot-card"
      >
        <div class="secure-boot-head">
          <span
            class="status-dot"
            :class="{ 'status-dot--ready': secureBoot.enabled && secureBoot.ready }"
            aria-hidden="true"
          ></span>
          <span class="secure-boot-state" data-testid="secure-boot-state">
            Secure Boot: {{ secureBootState }}
          </span>
          <span v-if="!secureBoot.enabled" class="stat-hint secure-boot-hint" data-testid="secure-boot-off-hint">
            UEFI HTTP Boot clients are ignored. Start Booty with
            <code>--secureBoot --proxyDHCP</code> to serve Secure Boot machines.
          </span>
          <span v-else class="stat-hint secure-boot-hint">
            Trusted CAs:
            <span class="mono" data-testid="secure-boot-trusted">{{ secureBoot.trusted.join(', ') }}</span>
          </span>
        </div>
        <template v-if="secureBoot.enabled">
          <dl class="secure-boot-facts" data-testid="secure-boot-facts">
            <dt class="stat-label">Bundle</dt>
            <dd>
              <span v-if="secureBoot.bundleVersion" class="mono truncate secure-boot-value" :title="secureBoot.bundleVersion">{{
                secureBoot.bundleVersion
              }}</span>
              <span v-else class="text-secondary">—</span>
            </dd>
            <dt class="stat-label">Boot URL</dt>
            <dd>
              <span v-if="secureBoot.bootURL" class="mono truncate secure-boot-value" :title="secureBoot.bootURL">{{
                secureBoot.bootURL
              }}</span>
              <span v-else class="text-secondary">—</span>
            </dd>
            <dt class="stat-label">Flatcar CA</dt>
            <dd class="secure-boot-ca">
              <template v-if="secureBoot.flatcarCA">
                <span
                  class="mono truncate secure-boot-value"
                  :title="`${secureBoot.flatcarCA.subject} (Flatcar ${secureBoot.flatcarCA.flatcarVersion}, valid until ${secureBoot.flatcarCA.notAfter})`"
                  data-testid="secure-boot-ca"
                  >{{ secureBoot.flatcarCA.sha256 }}</span
                >
                <button
                  type="button"
                  class="btn btn-sm btn-outline-secondary secure-boot-copy"
                  :title="caCopied ? 'Copied' : 'Copy the SHA256 fingerprint'"
                  data-testid="secure-boot-copy"
                  @click="copyFlatcarCA"
                >
                  {{ caCopied ? 'Copied' : 'Copy' }}
                </button>
                <a
                  v-if="secureBoot.flatcarCA.url"
                  class="btn btn-sm btn-outline-secondary secure-boot-copy"
                  :href="secureBoot.flatcarCA.url"
                  download="flatcar-ca.der"
                  title="Download the DER certificate to enroll in the firmware db"
                  data-testid="secure-boot-download"
                  >Download .der</a
                >
              </template>
              <span v-else class="text-secondary" data-testid="secure-boot-ca-missing">
                not extracted yet (needs a synced Flatcar release)
              </span>
            </dd>
          </dl>
          <p class="small text-secondary secure-boot-note mb-0">
            Fedora CoreOS boots with stock Microsoft keys. Flatcar needs this CA enrolled in the
            machine's <code>db</code> and <code>--secureBootTrusted=flatcar</code>; Bluefin Server's
            installed image is unsigned and cannot boot with Secure Boot on.
          </p>
        </template>
        <div
          v-if="secureBoot.warnings.length"
          class="alert alert-warning secure-boot-warnings mb-0"
          role="status"
          data-testid="secure-boot-warnings"
        >
          <strong class="me-1">Needs attention.</strong>
          <ul class="secure-boot-warning-list mb-0">
            <li v-for="(warning, index) in secureBoot.warnings" :key="index">{{ warning }}</li>
          </ul>
        </div>
      </div>

      <div class="section-title">Flatcar version pin</div>
      <div class="panel p-3 pin-panel fade-in">
        <p v-if="pin.pinned" class="mb-3">
          Pinned to <strong class="mono">{{ pin.version }}</strong
          >. Booty stays on this version instead of tracking the latest channel release.
        </p>
        <p v-else class="mb-3 text-secondary">
          No version pinned. Booty tracks the latest release on the configured channel.
        </p>
        <form class="input-group" @submit.prevent="setPin">
          <input
            v-model="pinInput"
            type="text"
            class="form-control mono"
            placeholder="e.g. 3815.2.0"
            aria-label="Flatcar version to pin"
            :disabled="pinBusy"
          />
          <button class="btn btn-primary" type="submit" :disabled="pinBusy || !pinInput.trim()">
            Pin version
          </button>
          <button
            class="btn btn-outline-secondary"
            type="button"
            :disabled="pinBusy || !pin.pinned"
            @click="clearPin"
          >
            Clear pin
          </button>
        </form>
        <div v-if="pinError" class="row-error" data-testid="pin-error">{{ pinError }}</div>
        <div v-else-if="pinStatus" class="small text-secondary mt-1" data-testid="pin-status">
          {{ pinStatus }}
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.pin-panel {
  max-width: 40rem;
}

.fleet-panel {
  position: relative;
  overflow: hidden;
}

.fleet-panel::before {
  content: '';
  position: absolute;
  inset: 0 auto 0 0;
  width: 3px;
  background: var(--booty-border);
}

.fleet-panel--alert::before {
  background: var(--bs-warning);
}

.fleet-stats {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--booty-space-3) var(--booty-space-5);
  padding: var(--booty-space-3);
}

.fleet-source {
  margin-left: auto;
  margin-top: 0;
}

.fleet-pending {
  color: var(--booty-accent-ink);
}

.fleet-table {
  border-top: 1px solid var(--booty-border);
}

.fleet-table th {
  font-size: 0.75rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--booty-muted);
  background: var(--booty-surface-alt);
}

.fleet-table td,
.fleet-table th {
  padding-left: var(--booty-space-3);
  padding-right: var(--booty-space-3);
}

.fleet-version {
  max-width: 22rem;
}

.fleet-empty {
  padding: 0 var(--booty-space-3) var(--booty-space-3);
}

.cluster-panel {
  position: relative;
  overflow: hidden;
  padding: var(--booty-space-3);
}

.cluster-panel::before {
  content: '';
  position: absolute;
  inset: 0 auto 0 0;
  width: 3px;
  background: var(--booty-border);
}

.cluster-panel--ready::before {
  background: var(--bs-success);
}

.cluster-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-2);
  margin-bottom: var(--booty-space-3);
}

.status-dot {
  width: 0.625rem;
  height: 0.625rem;
  border-radius: 50%;
  background: var(--booty-muted);
  box-shadow: 0 0 0 3px var(--booty-surface-alt);
}

.status-dot--ready {
  background: var(--bs-success);
  box-shadow: 0 0 0 3px var(--bs-success-bg-subtle);
}

.cluster-state {
  font-weight: 600;
}

.cluster-roles {
  margin-left: auto;
  margin-top: 0;
}

.cluster-facts {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: var(--booty-space-2) var(--booty-space-4);
  align-items: baseline;
  margin: 0;
}

.cluster-facts dt {
  margin: 0;
}

.cluster-facts dd {
  margin: 0;
  min-width: 0;
}

.cluster-endpoint {
  max-width: 100%;
}

.cluster-fingerprint {
  display: flex;
  align-items: center;
  gap: var(--booty-space-2);
}

.cluster-fingerprint-value {
  max-width: 28rem;
}

.cluster-copy {
  flex-shrink: 0;
}

.cluster-warnings {
  margin-top: var(--booty-space-3);
}

.cluster-warning-list {
  padding-left: var(--booty-space-3);
  margin-top: var(--booty-space-1);
}

.secure-boot-panel {
  position: relative;
  overflow: hidden;
  padding: var(--booty-space-3);
}

.secure-boot-panel::before {
  content: '';
  position: absolute;
  inset: 0 auto 0 0;
  width: 3px;
  background: var(--booty-border);
}

.secure-boot-panel--ready::before {
  background: var(--bs-success);
}

.secure-boot-panel--alert::before {
  background: var(--bs-warning);
}

.secure-boot-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-2);
}

.secure-boot-state {
  font-weight: 600;
}

.secure-boot-hint {
  margin-left: auto;
  margin-top: 0;
}

.secure-boot-facts {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: var(--booty-space-2) var(--booty-space-4);
  align-items: baseline;
  margin: var(--booty-space-3) 0 0;
}

.secure-boot-facts dt,
.secure-boot-facts dd {
  margin: 0;
  min-width: 0;
}

.secure-boot-value {
  max-width: 100%;
}

.secure-boot-ca {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-2);
}

.secure-boot-ca .secure-boot-value {
  max-width: 28rem;
}

.secure-boot-copy {
  flex-shrink: 0;
}

.secure-boot-note {
  margin-top: var(--booty-space-3);
}

.secure-boot-warnings {
  margin-top: var(--booty-space-3);
}

.secure-boot-warning-list {
  padding-left: var(--booty-space-3);
  margin-top: var(--booty-space-1);
}
</style>
