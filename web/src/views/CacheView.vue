<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { apiGet, errorMessage } from '@/api'
import type { CachedImage } from '@/types'
import ErrorAlert from '@/components/ErrorAlert.vue'
import LoadingState from '@/components/LoadingState.vue'
import EmptyState from '@/components/EmptyState.vue'

const images = ref<CachedImage[]>([])
const loading = ref(true)
const error = ref('')

const staleCount = computed(() => images.value.filter((img) => !img.upToDate).length)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await apiGet<CachedImage[] | null>('/registry')
    images.value = Array.isArray(result) ? result : []
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function imageRef(img: CachedImage) {
  const prefix = img.registry ? `${img.registry}/` : ''
  return `${prefix}${img.image}:${img.tag}`
}

function shortDigest(digest: string) {
  if (!digest) return '—'
  const hex = digest.replace(/^sha256:/, '')
  return hex.length > 12 ? `${hex.slice(0, 12)}…` : hex
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>OCI Cache</h2>
      <div class="d-flex align-items-center gap-3">
        <span v-if="!loading && images.length" class="small text-secondary">
          {{ images.length }} cached,
          <span :class="staleCount ? 'text-warning-emphasis' : ''">{{ staleCount }} stale</span>
        </span>
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
    <LoadingState v-if="loading" label="Checking registry…" />

    <div v-else class="panel table-panel fade-in">
      <table v-if="images.length" class="table table-hover align-middle">
        <thead>
          <tr>
            <th scope="col">Image</th>
            <th scope="col">Digest</th>
            <th scope="col" class="text-end">Status</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="img in images" :key="`${imageRef(img)}@${img.digest}`">
            <td class="mono">{{ imageRef(img) }}</td>
            <td>
              <span class="mono" :title="img.digest">{{ shortDigest(img.digest) }}</span>
            </td>
            <td class="text-end">
              <span v-if="img.upToDate" class="badge text-bg-success">Up to date</span>
              <span v-else class="badge text-bg-warning">Stale</span>
            </td>
          </tr>
        </tbody>
      </table>
      <EmptyState
        v-else
        title="No cached images"
        hint="Set an OSTree image on a host and Booty will cache it here."
      />
    </div>
  </div>
</template>
