<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { apiGet, apiGetText, apiPost, errorMessage } from '@/api'
import {
  AUTOPILOT_HOST_LABEL,
  OS_OPTIONS,
  RELEASE_STATE_LABEL,
  hostOS,
  isHistoryState,
  normalizeAutopilotStatus,
  normalizeBootyData,
  normalizePowerStatus,
  type AutopilotHost,
  type AutopilotRelease,
  type AutopilotReport,
  type AutopilotStatus,
  type HostOS,
  type PowerEvent,
  type RawAutopilotStatus,
  type RawBootyData,
  type RawPowerStatus,
  type StatusResponse
} from '@/types'
import {
  formatAbsolute,
  formatDuration,
  formatElapsed,
  formatRelative,
  formatUntil
} from '@/utils/time'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'
import EmptyState from '@/components/EmptyState.vue'

const REFRESH_INTERVAL_MS = 30_000
const EVENTS_PAGE = 50

/** What a wave moves, taken from /booty.json: the OS's non-canary, netbooting hosts. */
interface WaveHost {
  mac: string
  os: HostOS
  canary: boolean
  installed: boolean
}

const status = ref<AutopilotStatus>(normalizeAutopilotStatus(null))
const hostnames = ref<Record<string, string>>({})
const waveHosts = ref<WaveHost[] | null>(null)
const powerEvents = ref<PowerEvent[]>([])
const loading = ref(true)
const error = ref('')
const clearing = reactive<Record<string, boolean>>({})
const clearErrors = reactive<Record<string, string>>({})
const hostConfirm = ref('')
const hostClearing = reactive<Record<string, boolean>>({})
const hostClearErrors = reactive<Record<string, string>>({})
const openReport = ref('')
const reportText = ref('')
const reportLoading = ref(false)
const reportError = ref('')
const goodExpanded = reactive<Record<string, boolean>>({})
const shownEvents = ref(EVENTS_PAGE)
const allEvents = ref(false)
const eventsLoading = ref(false)

const off = computed(() => status.value.mode === 'off')

const MODE_HINT: Record<string, string> = {
  off: 'Nothing is gated or changed; boots are served as configured.',
  guard:
    'Health gate, retry, rollback to lastGood and fleet hold for every OS; rollout order is unchanged.',
  full: 'Guard plus the Bluefin canary-serial rollout, TIMEOUT/retry, quarantine and skip-to-next.'
}

function hostLabel(mac: string): string {
  return hostnames.value[mac] || mac
}

const hostsWithEpisodes = computed(() =>
  [...status.value.hosts].sort((a, b) => hostLabel(a.mac).localeCompare(hostLabel(b.mac)))
)

/**
 * The autopilot's ring plus the power tracker's own (the latter mirrors
 * into the former while a controller runs, so duplicates are dropped),
 * newest first, each with a key that survives new events arriving.
 */
const events = computed(() => {
  const seen = new Set(status.value.events.map((e) => `${e.at}|${e.mac}|${e.text}`))
  const extra = powerEvents.value
    .filter((e) => !seen.has(`${e.at}|${e.mac}|${e.text}`))
    .map((e) => ({ at: e.at, kind: e.kind, os: '', release: '', mac: e.mac, text: e.text }))
  const keys = new Map<string, number>()
  return [...status.value.events, ...extra]
    .sort((a, b) => a.at.localeCompare(b.at))
    .reverse()
    .map((e) => {
      const base = `${e.at}|${e.kind}|${e.mac}|${e.text}`
      const n = keys.get(base) ?? 0
      keys.set(base, n + 1)
      return { ...e, key: n ? `${base}#${n}` : base }
    })
})

const visibleEvents = computed(() => events.value.slice(0, shownEvents.value))

/** Events still in the server's ring that the default answer left out. */
const unfetchedEvents = computed(() =>
  allEvents.value ? 0 : Math.max(0, status.value.eventCount - status.value.events.length)
)

const eventTotal = computed(() => events.value.length + unfetchedEvents.value)

const moreEvents = computed(
  () => events.value.length > shownEvents.value || unfetchedEvents.value > 0
)

function osReleases(os: HostOS): AutopilotRelease[] {
  return status.value.os[os]?.releases ?? []
}

function hasReleases(os: HostOS): boolean {
  return Boolean(status.value.os[os]?.current) || osReleases(os).length > 0
}

function canClear(r: AutopilotRelease): boolean {
  return r.state === 'quarantined' || r.state === 'timeout'
}

/** A pruned release stays prominent while its verdict still keeps hosts off it. */
function muted(r: AutopilotRelease): boolean {
  return !r.cached && !canClear(r)
}

/** Releases that need an eye on them are always listed; good and skipped ones fold away. */
function needsAttention(r: AutopilotRelease): boolean {
  return !isHistoryState(r.state) || r.failing
}

function goodReleases(os: HostOS): AutopilotRelease[] {
  return osReleases(os).filter((r) => !needsAttention(r))
}

function visibleReleases(os: HostOS): AutopilotRelease[] {
  const attention = osReleases(os).filter(needsAttention)
  return goodExpanded[os] ? [...attention, ...goodReleases(os)] : attention
}

/** Records the default answer left out; Storage lists them all. */
function historyCount(os: HostOS): number {
  return Math.max(0, (status.value.os[os]?.releaseCount ?? 0) - osReleases(os).length)
}

const clocks = computed(() => {
  const parts: string[] = []
  if (status.value.healthWindow) parts.push(`health window ${formatDuration(status.value.healthWindow)}`)
  if (status.value.retryAfter) parts.push(`retry after ${formatDuration(status.value.retryAfter)}`)
  if (status.value.soak) parts.push(`soak ${formatDuration(status.value.soak)}`)
  if (status.value.cooldown) parts.push(`cooldown ${formatDuration(status.value.cooldown)}`)
  return parts.join(' · ')
})

interface SoakBadge {
  kind: 'soaking' | 'soaked'
  text: string
  badge: string
  title: string
}

/** The soak clock on a rolling release, shown only when a soak is configured. */
function soakBadge(r: AutopilotRelease): SoakBadge | null {
  if (r.state !== 'rolling' || !status.value.soak) return null
  const soak = formatDuration(status.value.soak)
  if (r.soaked) {
    return {
      kind: 'soaked',
      text: 'soaked',
      badge: 'text-bg-success',
      title: `Healthy on a canary for ${soak}; eligible for the next wave`
    }
  }
  if (!r.firstHealthyAt) return null
  return {
    kind: 'soaking',
    text: `soaking ${formatElapsed(r.firstHealthyAt)} / ${soak}`,
    badge: 'text-bg-light border',
    title: `First healthy ${formatAbsolute(r.firstHealthyAt)}; the fleet waits until it has soaked for ${soak}`
  }
}

/**
 * `<done>/<total>` of the wave: the OS's non-canary netbooting hosts, and
 * how many of them are healthy on the wave's release. Null until /booty.json
 * answered, since /autopilot alone cannot tell a canary apart.
 */
function waveProgress(os: HostOS, release: string): { done: number; total: number } | null {
  if (waveHosts.value === null) return null
  const movers = waveHosts.value.filter((h) => h.os === os && !h.canary && !h.installed)
  if (!movers.length) return null
  const healthy = new Set(
    status.value.hosts.filter((h) => h.healthyOn === release).map((h) => h.mac)
  )
  return { done: movers.filter((h) => healthy.has(h.mac)).length, total: movers.length }
}

function waveLine(os: HostOS): string {
  const block = status.value.os[os]
  const wave = block?.wave ?? null
  const next = formatUntil(block?.nextWaveAt)
  if (!wave) return next ? `next wave in ${next}` : ''
  if (wave.outcome === 'rolling') {
    const progress = waveProgress(os, wave.release)
    const count = progress ? ` · ${progress.done}/${progress.total} hosts` : ''
    return `wave into ${wave.release}${count} · started ${formatRelative(wave.startedAt)}`
  }
  if (wave.outcome === 'done') {
    const done = `last wave done ${formatRelative(wave.endedAt || wave.startedAt)}`
    return next ? `${done} · next wave in ${next}` : done
  }
  const verdict = osReleases(os).find((r) => r.version === wave.release)
  const state = verdict && verdict.state !== 'rolling' ? verdict.state : 'bad'
  return `wave aborted: ${wave.release} ${state}`
}

const HOST_CLEARABLE = new Set(['needs-hands', 'retrying', 'rolled-back'])

function canClearHost(h: AutopilotHost): boolean {
  return h.episode !== null && HOST_CLEARABLE.has(h.episode.state)
}

const HOST_CLEAR_HINT: Record<string, string> = {
  'needs-hands': 'Acknowledge the alert: the episode ends, the host is idle again.',
  retrying: 'Stop the second attempt: the host keeps what it runs, the fleet hold goes.',
  'rolled-back':
    'End the rollback episode: the host stays on lastGood, the release keeps its verdict.'
}

function hostClearHint(h: AutopilotHost): string {
  return HOST_CLEAR_HINT[h.episode?.state ?? ''] ?? ''
}

function episodeState(h: AutopilotHost) {
  return AUTOPILOT_HOST_LABEL[h.episode?.state ?? 'idle']
}

function statusURL(): string {
  return allEvents.value ? '/autopilot?events=all' : '/autopilot'
}

/**
 * A poll that answers with the revision it answered last time, and the
 * same live facts, changes nothing: the state is left alone so the lists
 * are not re-rendered every 30 s.
 */
function unchanged(next: AutopilotStatus, prev: AutopilotStatus): boolean {
  return (
    next.revision > 0 &&
    next.revision === prev.revision &&
    next.events.length === prev.events.length &&
    next.mode === prev.mode &&
    next.actuator === prev.actuator &&
    next.cluster.reachable === prev.cluster.reachable &&
    next.cluster.kured === prev.cluster.kured &&
    next.cluster.nodes === prev.cluster.nodes &&
    next.cluster.apiServer === prev.cluster.apiServer &&
    next.cluster.error === prev.cluster.error
  )
}

function sameJSON(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}

async function load(showSpinner = true) {
  if (showSpinner) loading.value = true
  error.value = ''
  const [statusResult, hostsResult, powerResult] = await Promise.allSettled([
    apiGet<RawAutopilotStatus>(statusURL()),
    apiGet<RawBootyData>('/booty.json'),
    apiGet<RawPowerStatus>('/power')
  ])
  if (statusResult.status === 'fulfilled') {
    const next = normalizeAutopilotStatus(statusResult.value)
    if (!unchanged(next, status.value)) status.value = next
  } else {
    error.value = errorMessage(statusResult.reason)
  }
  if (hostsResult.status === 'fulfilled') {
    const fleet = Object.values(normalizeBootyData(hostsResult.value).hosts)
    const names = Object.fromEntries(fleet.map((h) => [h.mac, h.hostname]))
    if (!sameJSON(names, hostnames.value)) hostnames.value = names
    const movers: WaveHost[] = fleet.map((h) => ({
      mac: h.mac,
      os: hostOS(h.os),
      canary: h.canary ?? false,
      installed: h.os === 'bluefin' && h.mode === 'installed'
    }))
    if (!sameJSON(movers, waveHosts.value)) waveHosts.value = movers
  }
  const power =
    powerResult.status === 'fulfilled' ? normalizePowerStatus(powerResult.value).events : []
  if (!sameJSON(power, powerEvents.value)) powerEvents.value = power
  loading.value = false
}

/** 50 events, then 100, then the whole ring, fetched only when asked for. */
async function showMoreEvents() {
  if (shownEvents.value < 2 * EVENTS_PAGE && events.value.length >= 2 * EVENTS_PAGE) {
    shownEvents.value = 2 * EVENTS_PAGE
    return
  }
  if (unfetchedEvents.value > 0) {
    allEvents.value = true
    eventsLoading.value = true
    await load(false)
    eventsLoading.value = false
  }
  shownEvents.value = Number.MAX_SAFE_INTEGER
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
    await load(false)
  } catch (err) {
    clearErrors[key] = errorMessage(err)
  } finally {
    clearing[key] = false
  }
}

async function clearHost(h: AutopilotHost) {
  const mac = h.mac
  hostConfirm.value = ''
  hostClearing[mac] = true
  hostClearErrors[mac] = ''
  try {
    await apiPost<StatusResponse>(`/autopilot/host/${encodeURIComponent(mac)}/clear`, {})
    await load(false)
  } catch (err) {
    hostClearErrors[mac] = errorMessage(err)
  } finally {
    hostClearing[mac] = false
  }
}

async function toggleReport(r: AutopilotReport) {
  if (openReport.value === r.key) {
    openReport.value = ''
    reportText.value = ''
    return
  }
  openReport.value = r.key
  reportText.value = ''
  reportError.value = ''
  reportLoading.value = true
  try {
    reportText.value = await apiGetText(r.path)
  } catch (err) {
    reportError.value = errorMessage(err)
  } finally {
    reportLoading.value = false
  }
}

function issueLabel(r: AutopilotReport): string {
  const m = /\/issues\/(\d+)/.exec(r.postedURL)
  return m ? `#${m[1]}` : 'issue'
}

let timer: ReturnType<typeof setInterval> | undefined

onMounted(() => {
  void load()
  timer = setInterval(() => void load(false), REFRESH_INTERVAL_MS)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>Autopilot</h2>
      <div class="d-flex align-items-center gap-3">
        <span
          v-if="!loading && !off && clocks"
          class="small text-secondary mono"
          data-testid="autopilot-clocks"
          >{{ clocks }}</span
        >
        <span class="small text-secondary">Refreshes every 30s</span>
        <button
          type="button"
          class="btn btn-sm btn-outline-secondary"
          :disabled="loading"
          @click="load()"
        >
          Refresh
        </button>
      </div>
    </div>

    <ErrorAlert v-if="error" :message="error" @retry="load()" />
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
            <span class="d-inline-flex flex-wrap gap-1" data-testid="autopilot-attention">
              <span class="badge" :class="status.needsHands ? 'text-bg-danger' : 'text-bg-success'">
                {{ status.needsHands }} needs hands
              </span>
              <span
                class="badge"
                :class="status.quarantined ? 'text-bg-danger' : 'text-bg-success'"
              >
                {{ status.quarantined }} quarantined
              </span>
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
        <div class="section-title">
          Fleet targets
          <RouterLink class="section-link" to="/storage" data-testid="storage-link"
            >what is on disk →</RouterLink
          >
        </div>
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
                        v-if="waveLine(os)"
                        class="wave small"
                        :data-wave="status.os[os].wave?.outcome ?? 'pending'"
                        :title="
                          status.os[os].nextWaveAt ? formatAbsolute(status.os[os].nextWaveAt) : ''
                        "
                      >
                        <span class="wave-label">Wave</span>
                        {{ waveLine(os) }}
                      </div>
                      <div
                        v-for="r in visibleReleases(os)"
                        :key="r.version"
                        class="release"
                        :class="{ muted: muted(r) }"
                        :data-release="r.version"
                        :data-cached="r.cached"
                      >
                        <span class="mono">{{ r.version }}</span>
                        <span
                          class="badge"
                          :class="RELEASE_STATE_LABEL[r.state].badge"
                          :data-state="r.state"
                        >
                          {{ RELEASE_STATE_LABEL[r.state].text }}
                        </span>
                        <span
                          v-if="soakBadge(r)"
                          class="badge"
                          :class="soakBadge(r)?.badge"
                          :data-soak="soakBadge(r)?.kind"
                          :title="soakBadge(r)?.title"
                          >{{ soakBadge(r)?.text }}</span
                        >
                        <RouterLink
                          v-if="!r.cached"
                          class="badge text-bg-light border text-decoration-none"
                          to="/storage"
                          title="The release's files were pruned; the record is kept as history. Storage shows what is on disk."
                          data-testid="pruned-hint"
                          >files pruned</RouterLink
                        >
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
                      <div v-if="goodReleases(os).length || historyCount(os)" class="release-more">
                        <button
                          v-if="goodReleases(os).length"
                          type="button"
                          class="btn btn-link btn-sm p-0 align-baseline"
                          :aria-expanded="goodExpanded[os] ? 'true' : 'false'"
                          data-action="toggle-good"
                          @click="goodExpanded[os] = !goodExpanded[os]"
                        >
                          {{ goodExpanded[os] ? 'Hide' : 'Show' }}
                          {{ goodReleases(os).length }} good release{{
                            goodReleases(os).length === 1 ? '' : 's'
                          }}
                        </button>
                        <RouterLink
                          v-if="historyCount(os)"
                          class="small"
                          to="/storage"
                          title="Older good releases whose files were pruned; the record is kept as history and Storage lists all of it"
                          data-testid="history-link"
                          >{{ historyCount(os) }} more in history → Storage</RouterLink
                        >
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>

        <div class="section-title">Episodes</div>
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
                <th scope="col" class="text-end">Actions</th>
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
                <td class="text-end">
                  <template v-if="canClearHost(h)">
                    <button
                      v-if="hostConfirm !== h.mac"
                      type="button"
                      class="btn btn-sm btn-outline-danger"
                      :disabled="hostClearing[h.mac]"
                      :title="hostClearHint(h)"
                      data-action="clear-host"
                      @click="hostConfirm = h.mac"
                    >
                      <span
                        v-if="hostClearing[h.mac]"
                        class="spinner-border spinner-border-sm me-1"
                        aria-hidden="true"
                      ></span>
                      Clear
                    </button>
                    <span v-else class="host-confirm" data-testid="clear-host-confirm">
                      <span class="small text-secondary">
                        End the episode on {{ hostLabel(h.mac) }}? {{ hostClearHint(h) }}
                      </span>
                      <button
                        type="button"
                        class="btn btn-sm btn-outline-secondary"
                        data-action="clear-host-cancel"
                        @click="hostConfirm = ''"
                      >
                        Keep
                      </button>
                      <button
                        type="button"
                        class="btn btn-sm btn-danger"
                        data-action="clear-host-confirm"
                        @click="clearHost(h)"
                      >
                        Clear episode
                      </button>
                    </span>
                  </template>
                  <div
                    v-if="hostClearErrors[h.mac]"
                    class="row-error"
                    data-testid="clear-host-error"
                  >
                    {{ hostClearErrors[h.mac] }}
                  </div>
                </td>
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
                <th scope="col">Updated</th>
                <th scope="col">Report</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="r in status.reports" :key="r.key">
                <tr :data-report="r.key">
                  <td class="mono">
                    {{ r.os }} {{ r.version }}
                    <span v-if="r.draft" class="badge text-bg-light border ms-1">draft</span>
                    <span
                      v-else
                      class="badge text-bg-danger ms-1"
                      title="The release is quarantined; the report is final"
                      >final</span
                    >
                  </td>
                  <td class="mono">{{ r.lastGood || '—' }}</td>
                  <td class="mono">{{ r.class || '—' }}</td>
                  <td>{{ r.attempts }}</td>
                  <td class="small">{{ r.rollbackResult || '—' }}</td>
                  <td>
                    <span :title="formatAbsolute(r.updatedAt || r.createdAt)">{{
                      formatRelative(r.updatedAt || r.createdAt)
                    }}</span>
                  </td>
                  <td>
                    <div class="report-links">
                      <button
                        v-if="r.path"
                        type="button"
                        class="btn btn-sm btn-outline-secondary"
                        data-action="view-report"
                        :aria-expanded="openReport === r.key"
                        @click="toggleReport(r)"
                      >
                        {{ openReport === r.key ? 'Hide' : 'View' }}
                      </button>
                      <a
                        v-if="r.path"
                        :href="r.path"
                        target="_blank"
                        rel="noopener noreferrer"
                        class="small"
                        data-action="download-report"
                        title="Open the redacted Markdown report"
                        >.md</a
                      >
                      <a
                        v-if="r.postedURL"
                        :href="r.postedURL"
                        target="_blank"
                        rel="noopener noreferrer"
                        class="badge text-bg-primary text-decoration-none"
                        data-action="issue-link"
                        :title="`Filed ${r.postAction || 'as an issue'} ${formatAbsolute(r.postedAt)}`"
                        >GitHub {{ issueLabel(r) }}</a
                      >
                      <span
                        v-else-if="r.postError"
                        class="badge text-bg-warning"
                        data-testid="report-post-error"
                        :title="r.postError"
                        >post failed</span
                      >
                    </div>
                  </td>
                </tr>
                <tr v-if="openReport === r.key" class="report-row" :data-report-body="r.key">
                  <td colspan="7">
                    <LoadingState v-if="reportLoading" label="Loading report…" />
                    <div v-else-if="reportError" class="row-error" data-testid="report-error">
                      {{ reportError }}
                    </div>
                    <pre
                      v-else
                      class="report-preview mono"
                      tabindex="0"
                      aria-label="Redacted autopilot report"
                      data-testid="report-preview"
                      >{{ reportText }}</pre>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </template>

      <template v-if="!off || events.length">
        <div class="section-title">Timeline</div>
        <div class="panel fade-in">
          <ul v-if="events.length" class="timeline" data-testid="autopilot-timeline">
            <li v-for="e in visibleEvents" :key="e.key" :data-kind="e.kind">
              <span class="when" :title="formatAbsolute(e.at)">{{ formatRelative(e.at) }}</span>
              <span
                class="badge kind"
                :class="e.kind === 'power' ? 'text-bg-info' : 'text-bg-light border'"
                data-testid="event-kind"
                >{{ e.kind }}</span
              >
              <span v-if="e.os" class="badge text-bg-light border">{{ e.os }}</span>
              <span v-if="e.release" class="mono small">{{ e.release }}</span>
              <span v-if="e.mac" class="small text-secondary">{{ hostLabel(e.mac) }}</span>
              <span class="text" :class="{ 'text-danger': e.kind === 'alert' }">{{ e.text }}</span>
            </li>
          </ul>
          <EmptyState
            v-else
            title="Nothing happened yet"
            hint="Episodes, holds, releases, actuator calls and power actions show up here."
          />
          <div v-if="moreEvents" class="timeline-more">
            <button
              type="button"
              class="btn btn-sm btn-outline-secondary"
              :disabled="eventsLoading"
              data-action="show-more-events"
              @click="showMoreEvents"
            >
              <span
                v-if="eventsLoading"
                class="spinner-border spinner-border-sm me-1"
                aria-hidden="true"
              ></span>
              Show more
            </button>
            <span class="small text-secondary" data-testid="events-shown"
              >{{ visibleEvents.length }} of {{ eventTotal }} events</span
            >
          </div>
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

.release.muted {
  opacity: 0.6;
}

.wave {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--booty-space-2);
  color: var(--booty-muted);
}

.wave-label {
  font-size: 0.75rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--booty-muted);
}

.wave[data-wave='aborted'] {
  color: var(--bs-danger);
}

.release-more {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--booty-space-3);
  font-size: 0.875rem;
}

.release-more .btn-link {
  font-size: inherit;
  text-decoration: none;
}

.release-more .btn-link:hover {
  text-decoration: underline;
}

.section-title {
  display: flex;
  align-items: baseline;
  gap: var(--booty-space-3);
}

.section-link {
  text-transform: none;
  letter-spacing: 0;
  font-weight: 500;
  text-decoration: none;
}

.section-link:hover {
  text-decoration: underline;
}

.note {
  max-width: 24rem;
}

.host-confirm {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: var(--booty-space-2);
  max-width: 28rem;
}

.report-links {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-2);
}

.report-row td {
  background: var(--booty-surface-alt);
}

.report-preview {
  max-height: 32rem;
  overflow: auto;
  margin: 0;
  padding: var(--booty-space-3);
  font-size: 0.8125rem;
  white-space: pre-wrap;
  background: var(--booty-surface);
  border: 1px solid var(--booty-border);
  border-radius: var(--booty-radius);
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

.timeline-more {
  display: flex;
  align-items: center;
  gap: var(--booty-space-3);
  padding: var(--booty-space-2) var(--booty-space-3);
  border-top: 1px solid var(--booty-border);
}
</style>
