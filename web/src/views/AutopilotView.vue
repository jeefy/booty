<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { apiGet, apiPost, errorMessage } from '@/api'
import {
  AUTOPILOT_HOST_LABEL,
  OS_OPTIONS,
  RELEASE_STATE_LABEL,
  normalizeAutopilotStatus,
  normalizeBootyData,
  type AutopilotHost,
  type AutopilotRelease,
  type AutopilotStatus,
  type BootyData,
  type HostOS,
  type RawAutopilotStatus,
  type RawBootyData,
  type StatusResponse
} from '@/types'
import { formatAbsolute, formatRelative } from '@/utils/time'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'
import EmptyState from '@/components/EmptyState.vue'

const status = ref<AutopilotStatus>(normalizeAutopilotStatus(null))
const hostData = ref<BootyData>(normalizeBootyData(null))
const loading = ref(true)
const error = ref('')
const clearing = reactive<Record<string, boolean>>({})
const clearErrors = reactive<Record<string, string>>({})

const off = computed(() => status.value.mode === 'off')

const MODE_HINT: Record<string, string> = {
  off: 'Nothing is gated or changed; boots are served as configured.',
  guard:
    'Health gate, retry, rollback to lastGood and fleet hold for every OS; rollout order is unchanged.',
  full: 'Guard plus the Bluefin canary-serial rollout, TIMEOUT/retry, quarantine and skip-to-next.'
}

function hostLabel(mac: string): string {
  const host = hostData.value.hosts[mac]
  return host?.hostname || mac
}

const hostsWithEpisodes = computed(() =>
  [...status.value.hosts].sort((a, b) => hostLabel(a.mac).localeCompare(hostLabel(b.mac)))
)

const events = computed(() => [...status.value.events].reverse())

function osReleases(os: HostOS): AutopilotRelease[] {
  return status.value.os[os]?.releases ?? []
}

function hasReleases(os: HostOS): boolean {
  return Boolean(status.value.os[os]?.current) || osReleases(os).length > 0
}

function canClear(r: AutopilotRelease): boolean {
  return r.state === 'quarantined' || r.state === 'timeout'
}

function episodeState(h: AutopilotHost) {
  return AUTOPILOT_HOST_LABEL[h.episode?.state ?? 'idle']
}

async function load() {
  loading.value = true
  error.value = ''
  const [statusResult, hostsResult] = await Promise.allSettled([
    apiGet<RawAutopilotStatus>('/autopilot'),
    apiGet<RawBootyData>('/booty.json')
  ])
  if (statusResult.status === 'fulfilled') {
    status.value = normalizeAutopilotStatus(statusResult.value)
  } else {
    error.value = errorMessage(statusResult.reason)
  }
  if (hostsResult.status === 'fulfilled') {
    hostData.value = normalizeBootyData(hostsResult.value)
  }
  loading.value = false
}

async function clear(r: AutopilotRelease) {
  const key = `${r.os}-${r.version}`
  clearing[key] = true
  clearErrors[key] = ''
  try {
    await apiPost<StatusResponse>(
      `/autopilot/${encodeURIComponent(r.os)}/release/${encodeURIComponent(r.version)}/clear`,
      {}
    )
    await load()
  } catch (err) {
    clearErrors[key] = errorMessage(err)
  } finally {
    clearing[key] = false
  }
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>Autopilot</h2>
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
    <LoadingState v-if="loading" label="Loading autopilot state…" />

    <template v-else>
      <div class="stat-grid fade-in" data-testid="autopilot-summary">
        <div class="panel stat">
          <div class="stat-label">Mode</div>
          <div class="stat-value">
            <span class="badge" :class="off ? 'text-bg-secondary' : 'text-bg-success'">
              {{ status.mode }}
            </span>
          </div>
          <div class="stat-hint">{{ MODE_HINT[status.mode] ?? '' }}</div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Actuator</div>
          <div class="stat-value mono">{{ status.actuator }}</div>
          <div class="stat-hint">
            <template v-if="status.actuator === 'kured'">
              kured drains and reboots; /update-check asks for it.
            </template>
            <template v-else-if="status.actuator === 'api'">
              Cordon, evict, reboot Pod through the Kubernetes API.
            </template>
            <template v-else-if="status.actuator === 'ssh'">systemctl reboot over SSH.</template>
            <template v-else-if="!off">
              No actuator: gates, holds and downgrades still happen; hosts reboot on their own
              update timer.
            </template>
            <template v-else>—</template>
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Cluster</div>
          <div class="stat-value">
            <span
              class="badge"
              :class="status.cluster.reachable ? 'text-bg-success' : 'text-bg-secondary'"
            >
              {{ status.cluster.reachable ? `${status.cluster.nodes} nodes` : 'unreachable' }}
            </span>
          </div>
          <div class="stat-hint">
            {{ status.cluster.error || (status.cluster.kured ? 'kured present' : 'no kured') }}
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Attention</div>
          <div class="stat-value">
            <span
              class="badge"
              :class="
                status.needsHands || status.quarantined ? 'text-bg-danger' : 'text-bg-success'
              "
              data-testid="autopilot-attention"
            >
              {{ status.needsHands }} needs hands · {{ status.quarantined }} quarantined
            </span>
          </div>
          <div class="stat-hint">
            <template v-if="status.held.length">
              Fleet target held for {{ status.held.join(', ') }}
            </template>
            <template v-else>No fleet target held</template>
          </div>
        </div>
      </div>

      <template v-if="off">
        <div class="section-title">Releases</div>
        <div class="panel fade-in">
          <EmptyState
            title="Autopilot is off"
            hint="Start Booty with --autopilot=guard or --autopilot=full to gate upgrades, roll back and hold the fleet."
          />
        </div>
      </template>

      <template v-else>
        <div class="section-title">Fleet targets</div>
        <div class="panel table-panel fade-in">
          <table class="table align-middle" data-testid="autopilot-os-table">
            <thead>
              <tr>
                <th scope="col">OS</th>
                <th scope="col">Fleet target</th>
                <th scope="col">Current</th>
                <th scope="col">lastGood</th>
                <th scope="col">Releases</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="os in OS_OPTIONS" :key="os">
                <tr v-if="hasReleases(os)" :data-os="os">
                  <td>
                    <span class="badge text-bg-light border">{{ os }}</span>
                  </td>
                  <td class="mono">
                    {{ status.os[os].fleetTarget || '—' }}
                    <span
                      v-if="status.os[os].held"
                      class="badge text-bg-warning ms-1"
                      title="The controller holds the fleet target here instead of current"
                      data-testid="autopilot-held"
                      >held</span
                    >
                  </td>
                  <td class="mono">{{ status.os[os].current || '—' }}</td>
                  <td class="mono">{{ status.os[os].lastGood || '—' }}</td>
                  <td>
                    <div class="releases">
                      <div
                        v-for="r in osReleases(os)"
                        :key="r.version"
                        class="release"
                        :data-release="r.version"
                      >
                        <span class="mono">{{ r.version }}</span>
                        <span
                          class="badge"
                          :class="RELEASE_STATE_LABEL[r.state].badge"
                          :data-state="r.state"
                        >
                          {{ RELEASE_STATE_LABEL[r.state].text }}
                        </span>
                        <span class="small text-secondary" :title="formatAbsolute(r.since)">
                          since {{ formatRelative(r.since) }}
                        </span>
                        <span v-if="r.attempts" class="small text-secondary">
                          · {{ r.attempts }} attempt{{ r.attempts === 1 ? '' : 's' }}
                        </span>
                        <span v-if="r.class" class="small text-secondary mono"
                          >· {{ r.class }}</span
                        >
                        <span v-if="r.failing" class="badge text-bg-warning">failing</span>
                        <button
                          v-if="canClear(r)"
                          type="button"
                          class="btn btn-sm btn-outline-danger"
                          :disabled="clearing[`${r.os}-${r.version}`]"
                          data-action="clear"
                          @click="clear(r)"
                        >
                          <span
                            v-if="clearing[`${r.os}-${r.version}`]"
                            class="spinner-border spinner-border-sm me-1"
                            aria-hidden="true"
                          ></span>
                          Clear
                        </button>
                        <span
                          v-if="clearErrors[`${r.os}-${r.version}`]"
                          class="row-error"
                          data-testid="clear-error"
                        >
                          {{ clearErrors[`${r.os}-${r.version}`] }}
                        </span>
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>

        <div class="section-title">Hosts</div>
        <div class="panel table-panel fade-in">
          <table
            v-if="hostsWithEpisodes.length"
            class="table table-hover align-middle"
            data-testid="autopilot-hosts-table"
          >
            <thead>
              <tr>
                <th scope="col">Host</th>
                <th scope="col">State</th>
                <th scope="col">Release</th>
                <th scope="col">Attempt</th>
                <th scope="col">Class</th>
                <th scope="col">Since</th>
                <th scope="col">Note</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="h in hostsWithEpisodes" :key="h.mac" :data-mac="h.mac">
                <td>
                  <div>{{ hostLabel(h.mac) }}</div>
                  <div class="small text-secondary mono">{{ h.mac }}</div>
                  <div v-if="h.healthyOn" class="small text-secondary">
                    healthy on <span class="mono">{{ h.healthyOn }}</span>
                  </div>
                </td>
                <td>
                  <span
                    class="badge"
                    :class="episodeState(h).badge"
                    :data-state="h.episode?.state ?? 'idle'"
                  >
                    {{ episodeState(h).text }}
                  </span>
                  <span
                    v-if="h.pinned"
                    class="badge text-bg-light border ms-1"
                    title="targetVersion set by the autopilot"
                    >pinned</span
                  >
                  <span v-if="h.episode?.retry" class="badge text-bg-light border ms-1">retry</span>
                </td>
                <td class="mono">
                  <template v-if="h.episode">
                    {{ h.episode.release }}
                    <span
                      v-if="h.episode.target !== h.episode.release"
                      class="small text-secondary"
                    >
                      → {{ h.episode.target }}
                    </span>
                  </template>
                  <span v-else>—</span>
                </td>
                <td>{{ h.episode?.attempt || '—' }}</td>
                <td class="mono">{{ h.episode?.class || '—' }}</td>
                <td>
                  <span :title="formatAbsolute(h.episode?.since ?? '')">
                    {{ h.episode ? formatRelative(h.episode.since) : '—' }}
                  </span>
                </td>
                <td class="small text-secondary note">{{ h.episode?.note || '' }}</td>
              </tr>
            </tbody>
          </table>
          <EmptyState
            v-else
            title="No episodes yet"
            hint="A host that boots a release it has not passed the health gate on starts one."
          />
        </div>

        <div v-if="status.reports.length" class="section-title">Reports</div>
        <div v-if="status.reports.length" class="panel table-panel fade-in">
          <table class="table align-middle" data-testid="autopilot-reports-table">
            <thead>
              <tr>
                <th scope="col">Release</th>
                <th scope="col">lastGood</th>
                <th scope="col">Class</th>
                <th scope="col">Attempts</th>
                <th scope="col">Rollback</th>
                <th scope="col">Drafted</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in status.reports" :key="r.key" :data-report="r.key">
                <td class="mono">
                  {{ r.os }} {{ r.version }}
                  <span v-if="r.draft" class="badge text-bg-light border ms-1">draft</span>
                </td>
                <td class="mono">{{ r.lastGood || '—' }}</td>
                <td class="mono">{{ r.class || '—' }}</td>
                <td>{{ r.attempts }}</td>
                <td class="small">{{ r.rollbackResult || '—' }}</td>
                <td>
                  <span :title="formatAbsolute(r.createdAt)">{{
                    formatRelative(r.createdAt)
                  }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="section-title">Timeline</div>
        <div class="panel fade-in">
          <ul v-if="events.length" class="timeline" data-testid="autopilot-timeline">
            <li v-for="(e, i) in events" :key="i" :data-kind="e.kind">
              <span class="when" :title="formatAbsolute(e.at)">{{ formatRelative(e.at) }}</span>
              <span class="badge text-bg-light border kind">{{ e.kind }}</span>
              <span v-if="e.os" class="badge text-bg-light border">{{ e.os }}</span>
              <span v-if="e.release" class="mono small">{{ e.release }}</span>
              <span v-if="e.mac" class="small text-secondary">{{ hostLabel(e.mac) }}</span>
              <span class="text" :class="{ 'text-danger': e.kind === 'alert' }">{{ e.text }}</span>
            </li>
          </ul>
          <EmptyState
            v-else
            title="Nothing happened yet"
            hint="Episodes, holds, releases and actuator calls show up here."
          />
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.releases {
  display: flex;
  flex-direction: column;
  gap: var(--booty-space-1);
}

.release {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-2);
}

.note {
  max-width: 24rem;
}

.timeline {
  list-style: none;
  margin: 0;
  padding: var(--booty-space-2) var(--booty-space-3);
}

.timeline li {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--booty-space-2);
  padding: var(--booty-space-1) 0;
  border-bottom: 1px solid var(--booty-border);
  font-size: 0.875rem;
}

.timeline li:last-child {
  border-bottom: 0;
}

.timeline .when {
  min-width: 7rem;
  color: var(--booty-muted);
}

.timeline .kind {
  min-width: 4.5rem;
  text-align: center;
}

.timeline .text {
  flex: 1 1 20rem;
}
</style>
