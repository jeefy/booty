<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { apiGet, errorMessage } from '@/api'
import type { Health, Info } from '@/types'
import { formatAbsolute, formatRelative } from '@/utils/time'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'

const info = ref<Info>({})
const health = ref<Health | null>(null)
const healthError = ref('')
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  healthError.value = ''
  const [infoResult, healthResult] = await Promise.allSettled([
    apiGet<Info>('/info'),
    apiGet<Health>('/healthz')
  ])
  if (infoResult.status === 'fulfilled') {
    info.value = infoResult.value ?? {}
  } else {
    error.value = errorMessage(infoResult.reason)
  }
  if (healthResult.status === 'fulfilled') {
    health.value = healthResult.value
  } else {
    health.value = null
    healthError.value = errorMessage(healthResult.reason)
  }
  loading.value = false
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>About</h2>
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
    <LoadingState v-if="loading" label="Loading build info…" />

    <template v-else>
      <div class="stat-grid fade-in">
        <div class="panel stat">
          <div class="stat-label">Health</div>
          <div class="stat-value">
            <span v-if="health?.status === 'ok'" class="badge text-bg-success">Healthy</span>
            <span v-else class="badge text-bg-danger">Unhealthy</span>
          </div>
          <div class="stat-hint">
            {{ healthError || (health ? `/healthz reports "${health.status}"` : 'No response') }}
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Booty version</div>
          <div class="stat-value">{{ info.booty?.version || '—' }}</div>
          <div class="stat-hint">
            <template v-if="info.booty?.timestamp">
              Built {{ formatRelative(info.booty.timestamp) }}
              <span class="text-nowrap">({{ formatAbsolute(info.booty.timestamp) }})</span>
            </template>
            <template v-else>Build time unknown</template>
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">Flatcar</div>
          <div class="stat-value">{{ info.flatcar?.version || '—' }}</div>
          <div class="stat-hint">
            <template v-if="info.flatcar?.pinnedVersion">
              Pinned to <span class="mono">{{ info.flatcar.pinnedVersion }}</span>
            </template>
            <template v-else-if="info.flatcar?.version">Tracking latest</template>
            <template v-else>Not polled yet</template>
          </div>
        </div>
        <div class="panel stat">
          <div class="stat-label">CoreOS</div>
          <div class="stat-value">{{ info.coreos?.version || '—' }}</div>
          <div class="stat-hint">
            {{ info.coreos?.version ? 'Tracking latest' : 'Not polled yet' }}
          </div>
        </div>
      </div>

      <p v-if="info.fleet" class="small text-secondary mt-3 mb-0" data-testid="about-fleet">
        Fleet: <span class="mono">{{ info.fleet.hosts ?? 0 }}</span> hosts,
        <span class="mono">{{ info.fleet.pendingReboots ?? 0 }}</span> pending reboots.
      </p>

      <div class="section-title">Project</div>
      <div class="panel p-3 fade-in">
        <p class="mb-2">
          Booty is a small (i)PXE boot server for Flatcar Linux, Fedora CoreOS and Universal Blue
          images. It serves boot files over TFTP/HTTP, renders Butane/Ignition configs per MAC
          address, and keeps a local cache of upstream releases and OCI images.
        </p>
        <p class="mb-0 small text-secondary">
          Issues and pull requests are welcome at
          <a href="https://github.com/jeefy/booty/" target="_blank" rel="noopener noreferrer">
            github.com/jeefy/booty</a
          >.
        </p>
      </div>
    </template>
  </div>
</template>
