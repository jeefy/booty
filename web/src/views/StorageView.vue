<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { apiGet, errorMessage } from '@/api'
import {
  OS_OPTIONS,
  RELEASE_STATE_LABEL,
  formatBytes,
  normalizeAutopilotStatus,
  normalizeBootyData,
  normalizeStorage,
  type AutopilotRelease,
  type AutopilotStatus,
  type BootyData,
  type HostOS,
  type RawAutopilotStatus,
  type RawBootyData,
  type RawStorage,
  type ReleaseLink,
  type Storage,
  type StorageRelease
} from '@/types'
import { formatAbsolute, formatRelative } from '@/utils/time'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'
import EmptyState from '@/components/EmptyState.vue'

const storage = ref<Storage>(normalizeStorage(null))
const autopilot = ref<AutopilotStatus>(normalizeAutopilotStatus(null))
const hostData = ref<BootyData>(normalizeBootyData(null))
const loading = ref(true)
const error = ref('')

const OS_LABEL: Record<HostOS, string> = {
  flatcar: 'Flatcar',
  coreos: 'Fedora CoreOS',
  bluefin: 'Bluefin Server'
}

const LINK_HINT: Record<ReleaseLink, string> = {
  current: 'The fleet target: what hosts without a targetVersion boot',
  previous: 'The release current replaced; kept so hosts mid-boot can finish',
  lastGood: 'The newest release the whole fleet was healthy on; the autopilot rolls back to it'
}

const RETAINED_HINT: Record<string, string> = {
  current: 'Kept as the current release',
  previous: 'Kept as the previous release',
  lastGood: 'Kept as lastGood',
  pinned: "Kept because a host's targetVersion names it",
  '-': 'Nothing references it; the next sync of this OS prunes it',
  untracked: 'This OS is not tracked: nothing is retained or pruned, the directory is left as it is'
}

const UNTRACKED_LINK_HINT = 'links are kept but ignored while the OS is untracked'

function retainedLabel(os: HostOS, release: StorageRelease): string {
  if (!storage.value.os[os].tracked) return 'untracked'
  return release.retained === '-' ? '—' : release.retained
}

function retainedHint(os: HostOS, release: StorageRelease): string {
  return RETAINED_HINT[storage.value.os[os].tracked ? release.retained : 'untracked'] ?? ''
}

/** A row of an OS card: a release on disk, an autopilot record, or both. */
interface ReleaseRow {
  version: string
  release: StorageRelease | null
  record: AutopilotRelease | null
}

const hostCount = computed(() => Object.keys(hostData.value.hosts).length)

const bootyBytes = computed(() => storage.value.used)
const osBytes = computed(() => OS_OPTIONS.map((os) => storage.value.os[os].bytes))
const assetBytes = computed(
  () =>
    storage.value.assets.reduce((sum, a) => sum + a.bytes, 0) +
    storage.value.other.reduce((sum, e) => sum + e.bytes, 0) +
    storage.value.autopilot.stateBytes +
    storage.value.autopilot.reportsBytes
)
const elsewhereBytes = computed(() =>
  Math.max(0, storage.value.total - storage.value.free - bootyBytes.value)
)

function percent(bytes: number): string {
  if (!storage.value.total) return '0%'
  return `${((bytes / storage.value.total) * 100).toFixed(2)}%`
}

const segments = computed(() => [
  { key: 'flatcar', label: 'Flatcar', bytes: osBytes.value[0] },
  { key: 'coreos', label: 'CoreOS', bytes: osBytes.value[1] },
  { key: 'bluefin', label: 'Bluefin', bytes: osBytes.value[2] },
  { key: 'assets', label: 'Boot files & state', bytes: assetBytes.value },
  { key: 'elsewhere', label: 'Other data on this filesystem', bytes: elsewhereBytes.value },
  { key: 'free', label: 'Free', bytes: storage.value.free }
])

function hostLabel(mac: string): string {
  return hostData.value.hosts[mac]?.hostname || mac
}

function hostTitle(macs: string[]): string {
  return macs.map((mac) => `${hostLabel(mac)} (${mac})`).join('\n')
}

function compareVersions(a: string, b: string): number {
  const pa = a.split('.').map(Number)
  const pb = b.split('.').map(Number)
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pb[i] ?? 0) - (pa[i] ?? 0)
    if (d !== 0 && !Number.isNaN(d)) return d
  }
  return b.localeCompare(a)
}

function rows(os: HostOS): ReleaseRow[] {
  const byVersion = new Map<string, ReleaseRow>()
  for (const release of storage.value.os[os].releases) {
    byVersion.set(release.version, { version: release.version, release, record: null })
  }
  for (const record of autopilot.value.os[os]?.releases ?? []) {
    const row = byVersion.get(record.version)
    if (row) {
      row.record = record
    } else {
      byVersion.set(record.version, { version: record.version, release: null, record })
    }
  }
  return [...byVersion.values()].sort((a, b) => compareVersions(a.version, b.version))
}

function hasRecords(os: HostOS): boolean {
  return rows(os).some((row) => row.record !== null)
}

function osSummary(os: HostOS): string {
  const block = storage.value.os[os]
  const parts: string[] = []
  if (!block.tracked) parts.push('not tracked')
  if (block.channel) parts.push(`channel ${block.channel}`)
  if (block.source) parts.push(block.source)
  if (block.pin) parts.push(`pinned ${block.pin}`)
  return parts.join(' · ')
}

function prominent(record: AutopilotRelease | null): boolean {
  return record !== null && (record.state === 'quarantined' || record.state === 'timeout')
}

function age(modified: string): string {
  return modified ? formatRelative(modified) : '—'
}

async function load() {
  loading.value = true
  error.value = ''
  const [storageResult, autopilotResult, hostsResult] = await Promise.allSettled([
    apiGet<RawStorage>('/storage'),
    apiGet<RawAutopilotStatus>('/autopilot'),
    apiGet<RawBootyData>('/booty.json')
  ])
  if (storageResult.status === 'fulfilled') {
    storage.value = normalizeStorage(storageResult.value)
  } else {
    error.value = errorMessage(storageResult.reason)
  }
  autopilot.value =
    autopilotResult.status === 'fulfilled'
      ? normalizeAutopilotStatus(autopilotResult.value)
      : normalizeAutopilotStatus(null)
  hostData.value =
    hostsResult.status === 'fulfilled'
      ? normalizeBootyData(hostsResult.value)
      : normalizeBootyData(null)
  loading.value = false
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>Storage</h2>
      <div class="d-flex align-items-center gap-3">
        <span v-if="!loading && storage.dataDir" class="small text-secondary mono">{{
          storage.dataDir
        }}</span>
        <button
          type="button"
          class="btn btn-sm btn-outline-secondary"
          :disabled="loading"
          @click="load"
        >
          Refresh
        </button>
      </div>
    </div>

    <ErrorAlert v-if="error" :message="error" @retry="load" />
    <LoadingState v-if="loading" label="Measuring the data directory…" />

    <template v-else>
      <div class="stat-grid fade-in" data-testid="storage-summary">
        <div class="panel stat">
          <div class="stat-label">Booty uses</div>
          <div class="stat-value" data-testid="storage-used">{{ formatBytes(bootyBytes) }}</div>
          <div class="stat-hint">{{ percent(bootyBytes) }} of the filesystem</div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Free</div>
          <div class="stat-value" data-testid="storage-free">{{ formatBytes(storage.free) }}</div>
          <div class="stat-hint">of {{ formatBytes(storage.total) }} total</div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Releases</div>
          <div class="stat-value">
            {{ OS_OPTIONS.reduce((n, os) => n + storage.os[os].releases.length, 0) }}
          </div>
          <div class="stat-hint">
            {{ formatBytes(osBytes.reduce((a, b) => a + b, 0)) }} across
            {{ OS_OPTIONS.filter((os) => storage.os[os].releases.length).length }} OS
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Measured</div>
          <div class="stat-value">{{ formatRelative(storage.computedAt) }}</div>
          <div class="stat-hint">Sizes are re-walked at most once a minute</div>
        </div>
      </div>

      <div class="panel usage fade-in" data-testid="storage-bar">
        <div class="usage-bar" role="img" aria-label="Filesystem usage">
          <span
            v-for="seg in segments"
            :key="seg.key"
            class="usage-seg"
            :class="`usage-${seg.key}`"
            :style="{ width: percent(seg.bytes) }"
            :title="`${seg.label}: ${formatBytes(seg.bytes)}`"
          ></span>
        </div>
        <ul class="usage-legend">
          <li v-for="seg in segments" :key="seg.key">
            <span class="swatch" :class="`usage-${seg.key}`"></span>
            <span>{{ seg.label }}</span>
            <span class="mono text-secondary">{{ formatBytes(seg.bytes) }}</span>
          </li>
        </ul>
      </div>

      <template v-for="os in OS_OPTIONS" :key="os">
        <div class="section-title">
          {{ OS_LABEL[os] }}
          <span class="section-meta mono">{{ osSummary(os) }}</span>
          <span class="section-meta mono">{{ formatBytes(storage.os[os].bytes) }}</span>
        </div>
        <div class="panel table-panel fade-in" :data-testid="`storage-os-${os}`">
          <table v-if="rows(os).length" class="table align-middle releases">
            <colgroup>
              <col class="col-version" />
              <col class="col-size" />
              <col class="col-age" />
              <col class="col-links" />
              <col class="col-retained" />
              <col />
              <col v-if="hasRecords(os)" class="col-autopilot" />
            </colgroup>
            <thead>
              <tr>
                <th scope="col">Version</th>
                <th scope="col">Size</th>
                <th scope="col">Age</th>
                <th scope="col">Links</th>
                <th scope="col">Retained by</th>
                <th scope="col">Hosts</th>
                <th v-if="hasRecords(os)" scope="col">Autopilot</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in rows(os)"
                :key="row.version"
                :data-release="row.version"
                :class="{ pruned: !row.release, prominent: !row.release && prominent(row.record) }"
              >
                <td>
                  <span class="version-cell">
                    <span class="mono">{{ row.version }}</span>
                    <span
                      v-if="!row.release"
                      class="badge text-bg-light border"
                      title="The autopilot remembers this release; its files were pruned"
                      data-testid="pruned-badge"
                      >files pruned</span
                    >
                    <span
                      v-else-if="!row.release.cached"
                      class="badge text-bg-light border"
                      title="On disk but ignored: this OS is not tracked"
                      data-testid="ignored-badge"
                      >ignored</span
                    >
                  </span>
                </td>
                <td class="mono size-cell">
                  <template v-if="row.release">
                    {{ formatBytes(row.release.bytes) }}
                    <span class="small text-secondary"
                      >· {{ row.release.files }} file{{ row.release.files === 1 ? '' : 's' }}</span
                    >
                  </template>
                  <span v-else class="text-secondary">—</span>
                </td>
                <td>
                  <span v-if="row.release" :title="formatAbsolute(row.release.modified)">{{
                    age(row.release.modified)
                  }}</span>
                  <span v-else class="text-secondary">—</span>
                </td>
                <td>
                  <span class="d-inline-flex flex-wrap gap-1">
                    <span
                      v-for="link in row.release?.links ?? []"
                      :key="link"
                      class="badge"
                      :class="[
                        link === 'current' && storage.os[os].tracked
                          ? 'text-bg-success'
                          : 'text-bg-light border',
                        { 'link-ignored': !storage.os[os].tracked }
                      ]"
                      :title="storage.os[os].tracked ? LINK_HINT[link] : UNTRACKED_LINK_HINT"
                      :data-link="link"
                      >{{ link }}</span
                    >
                    <span v-if="!row.release?.links.length" class="text-secondary">—</span>
                  </span>
                </td>
                <td>
                  <span
                    v-if="row.release"
                    :class="{
                      mono: retainedLabel(os, row.release) !== '—',
                      'text-secondary': row.release.retained === '-' || !storage.os[os].tracked
                    }"
                    :title="retainedHint(os, row.release)"
                    data-testid="retained"
                    >{{ retainedLabel(os, row.release) }}</span
                  >
                  <span v-else class="text-secondary">—</span>
                </td>
                <td>
                  <span v-if="row.release" class="d-inline-flex flex-wrap gap-1">
                    <span
                      v-if="row.release.hosts.running.length"
                      class="badge text-bg-light border"
                      :title="hostTitle(row.release.hosts.running)"
                      data-testid="hosts-running"
                      >{{ row.release.hosts.running.length }} running</span
                    >
                    <span
                      v-if="row.release.hosts.pinned.length"
                      class="badge text-bg-light border"
                      :title="hostTitle(row.release.hosts.pinned)"
                      data-testid="hosts-pinned"
                      >{{ row.release.hosts.pinned.length }} pinned</span
                    >
                    <span
                      v-if="!row.release.hosts.running.length && !row.release.hosts.pinned.length"
                      class="text-secondary"
                      >—</span
                    >
                  </span>
                  <span v-else class="text-secondary">—</span>
                </td>
                <td v-if="hasRecords(os)" data-testid="autopilot-cell">
                  <span
                    v-if="row.record"
                    class="badge"
                    :class="RELEASE_STATE_LABEL[row.record.state].badge"
                    :data-state="row.record.state"
                    :title="`since ${formatRelative(row.record.since)} (${formatAbsolute(row.record.since)})`"
                    >{{ RELEASE_STATE_LABEL[row.record.state].text }}</span
                  >
                  <span v-else class="text-secondary">—</span>
                </td>
              </tr>
            </tbody>
          </table>
          <EmptyState
            v-else
            :title="storage.os[os].tracked ? 'No releases cached' : 'Not tracked'"
            :hint="
              storage.os[os].tracked
                ? 'The next version check downloads the current release.'
                : 'Start Booty with a channel for this OS to track it again.'
            "
          />
        </div>
      </template>

      <div class="section-title">
        Boot files and state
        <span class="section-meta mono">{{ formatBytes(assetBytes) }}</span>
      </div>
      <div class="panel table-panel fade-in" data-testid="storage-assets">
        <table v-if="storage.assets.length" class="table align-middle">
          <thead>
            <tr>
              <th scope="col">File</th>
              <th scope="col">Kind</th>
              <th scope="col">Size</th>
              <th scope="col">Modified</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in storage.assets" :key="a.name" :data-asset="a.name">
              <td class="mono">{{ a.name }}</td>
              <td>
                <span class="badge text-bg-light border" :data-kind="a.kind">{{ a.kind }}</span>
              </td>
              <td class="mono">{{ formatBytes(a.bytes) }}</td>
              <td>
                <span :title="formatAbsolute(a.modified)">{{ age(a.modified) }}</span>
              </td>
            </tr>
          </tbody>
        </table>
        <EmptyState v-else title="No files" hint="The iPXE binaries appear here on first start." />
        <div class="asset-footer small text-secondary" data-testid="storage-autopilot">
          <span>
            Autopilot state <span class="mono">{{ formatBytes(storage.autopilot.stateBytes) }}</span
            >, reports <span class="mono">{{ formatBytes(storage.autopilot.reportsBytes) }}</span>
          </span>
          <span v-for="e in storage.other" :key="e.name">
            {{ e.name }}/ <span class="mono">{{ formatBytes(e.bytes) }}</span>
          </span>
          <span v-if="hostCount"
            >{{ hostCount }} registered host{{ hostCount === 1 ? '' : 's' }}</span
          >
        </div>
      </div>

      <p class="small text-secondary mt-3">
        A release is kept while a link (<span class="mono">current</span>,
        <span class="mono">previous</span>, <span class="mono">lastGood</span>) or a host's
        <span class="mono">targetVersion</span> names it; everything else is pruned on the next sync
        of that OS. To free space, pin fewer versions or stop tracking an OS with
        <span class="mono">--coreOSChannel=none</span>.
      </p>
    </template>
  </div>
</template>

<style scoped>
.usage {
  margin-top: var(--booty-space-3);
  padding: var(--booty-space-3);
}

.usage-bar {
  display: flex;
  height: 0.875rem;
  overflow: hidden;
  border-radius: 999px;
  background: var(--booty-surface-alt);
  border: 1px solid var(--booty-border);
}

.usage-seg {
  display: block;
  min-width: 0;
  transition: width 240ms ease-out;
}

.usage-legend {
  display: flex;
  flex-wrap: wrap;
  gap: var(--booty-space-2) var(--booty-space-4);
  list-style: none;
  margin: var(--booty-space-2) 0 0;
  padding: 0;
  font-size: 0.8125rem;
}

.usage-legend li {
  display: inline-flex;
  align-items: center;
  gap: var(--booty-space-1);
}

.swatch {
  width: 0.625rem;
  height: 0.625rem;
  border-radius: 2px;
}

.usage-flatcar {
  background: #2f6fed;
}

.usage-coreos {
  background: #1f9d8a;
}

.usage-bluefin {
  background: var(--booty-accent);
}

.usage-assets {
  background: #8a93a5;
}

.usage-elsewhere {
  background: repeating-linear-gradient(135deg, #d7dbe2 0 4px, #e9ecf1 4px 8px);
}

.usage-free {
  background: transparent;
}

.section-title {
  display: flex;
  align-items: baseline;
  gap: var(--booty-space-3);
}

.section-meta {
  text-transform: none;
  letter-spacing: 0;
  font-weight: 500;
}

.section-meta:last-child {
  margin-left: auto;
}

.releases {
  table-layout: fixed;
}

.col-version {
  width: 15rem;
}

.col-size {
  width: 12rem;
}

.col-age {
  width: 6rem;
}

.col-links {
  width: 13rem;
}

.col-retained {
  width: 7.5rem;
}

.col-autopilot {
  width: 8rem;
}

.releases .size-cell {
  white-space: nowrap;
}

.version-cell {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-1);
}

.link-ignored {
  opacity: 0.55;
}

tr.pruned td {
  color: var(--booty-muted);
}

tr.pruned.prominent td:first-child {
  color: var(--booty-ink);
}

.asset-footer {
  display: flex;
  flex-wrap: wrap;
  gap: var(--booty-space-2) var(--booty-space-4);
  padding: var(--booty-space-2) var(--booty-space-3);
  border-top: 1px solid var(--booty-border);
}
</style>
