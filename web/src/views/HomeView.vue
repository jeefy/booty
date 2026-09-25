<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { apiGet, apiPost, errorMessage } from '@/api'
import { normalizeBootyData, type BootyData, type Info, type PinState } from '@/types'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'

const REFRESH_INTERVAL_MS = 30_000

const hostData = ref<BootyData>(normalizeBootyData(null))
const info = ref<Info>({})
const pin = ref<PinState>({ pinned: false, version: '', current: '' })

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
const bootyVersion = computed(() => info.value.booty?.version || '')

async function load(showSpinner = true) {
  if (showSpinner) loading.value = true
  error.value = ''
  try {
    const [data, infoData, pinData] = await Promise.all([
      apiGet<Partial<BootyData>>('/booty.json'),
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
</style>
