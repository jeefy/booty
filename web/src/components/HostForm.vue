<script setup lang="ts">
import { OS_OPTIONS, type Host } from '@/types'

const draft = defineModel<Host>({ required: true })

defineProps<{
  busy: boolean
  error: string
  submitLabel: string
}>()

const emit = defineEmits<{
  submit: []
  cancel: []
}>()
</script>

<template>
  <form class="host-form" @submit.prevent="emit('submit')">
    <div class="row g-2 align-items-end">
      <div class="col-12 col-md-3">
        <label class="form-label small mb-1" :for="`hostname-${draft.mac}`">Hostname</label>
        <input
          :id="`hostname-${draft.mac}`"
          v-model="draft.hostname"
          type="text"
          class="form-control form-control-sm"
          placeholder="node-01"
          :disabled="busy"
        />
      </div>
      <div class="col-12 col-md-2">
        <label class="form-label small mb-1" :for="`ip-${draft.mac}`">IP</label>
        <input
          :id="`ip-${draft.mac}`"
          v-model="draft.ip"
          type="text"
          class="form-control form-control-sm mono"
          placeholder="192.168.1.20"
          :disabled="busy"
        />
      </div>
      <div class="col-12 col-md-2">
        <label class="form-label small mb-1" :for="`os-${draft.mac}`">OS</label>
        <select
          :id="`os-${draft.mac}`"
          v-model="draft.os"
          class="form-select form-select-sm"
          :disabled="busy"
        >
          <option value="">(default)</option>
          <option v-for="os in OS_OPTIONS" :key="os" :value="os">{{ os }}</option>
        </select>
      </div>
      <div class="col-12 col-md-5">
        <label class="form-label small mb-1" :for="`ignition-${draft.mac}`">Ignition file</label>
        <input
          :id="`ignition-${draft.mac}`"
          v-model="draft.ignitionFile"
          type="text"
          class="form-control form-control-sm mono"
          placeholder="config.yaml"
          :disabled="busy"
        />
      </div>
      <div class="col-12 col-md-7">
        <label class="form-label small mb-1" :for="`ostree-${draft.mac}`">OSTree image</label>
        <input
          :id="`ostree-${draft.mac}`"
          v-model="draft.ostreeImage"
          type="text"
          class="form-control form-control-sm mono"
          placeholder="ghcr.io/ublue-os/bazzite:stable"
          :disabled="busy"
        />
      </div>
      <div class="col-6 col-md-2">
        <div class="form-check mb-1">
          <input
            :id="`install-${draft.mac}`"
            v-model="draft.doInstall"
            class="form-check-input"
            type="checkbox"
            :disabled="busy"
          />
          <label class="form-check-label small" :for="`install-${draft.mac}`">
            Install on next boot
          </label>
        </div>
      </div>
      <div class="col-6 col-md-3 d-flex justify-content-end gap-2">
        <button
          type="button"
          class="btn btn-sm btn-outline-secondary"
          :disabled="busy"
          @click="emit('cancel')"
        >
          Cancel
        </button>
        <button type="submit" class="btn btn-sm btn-primary" :disabled="busy">
          <span v-if="busy" class="spinner-border spinner-border-sm me-1" aria-hidden="true"></span>
          {{ submitLabel }}
        </button>
      </div>
    </div>
    <div v-if="error" class="row-error" data-testid="row-error">{{ error }}</div>
  </form>
</template>

<style scoped>
.host-form {
  padding: var(--booty-space-2) 0;
}
</style>
