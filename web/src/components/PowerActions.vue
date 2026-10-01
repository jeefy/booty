<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ApiError, apiPost, errorMessage } from '@/api'
import {
  normalizeHostPower,
  type Host,
  type HostPower,
  type PowerAction,
  type PowerActionRequest,
  type PowerActionResponse,
  type PowerCapabilities
} from '@/types'
import { powerButtons, type PowerButton } from '@/utils/power'

const props = defineProps<{
  host: Host
  capabilities: PowerCapabilities | null
  lastUpWorker: boolean
  disabled?: boolean
}>()

const emit = defineEmits<{
  /** The server accepted the action and answered with the new power block. */
  updated: [power: HostPower]
  /** An inline confirm opened or closed (so the row can hide its other buttons). */
  confirming: [open: boolean]
}>()

const pending = ref<PowerButton | null>(null)
const reason = ref('')
const force = ref(false)
const busy = ref(false)
const error = ref('')

const buttons = computed(() =>
  powerButtons(props.host, { capabilities: props.capabilities, lastUpWorker: props.lastUpWorker })
)

watch(pending, (open) => emit('confirming', open !== null))

function open(button: PowerButton) {
  if (!button.enabled) return
  pending.value = button
  reason.value = ''
  force.value = false
  error.value = ''
}

function cancel() {
  pending.value = null
  error.value = ''
}

async function confirm() {
  const button = pending.value
  if (!button) return
  busy.value = true
  error.value = ''
  const body: PowerActionRequest = {}
  if (reason.value.trim()) body.reason = reason.value.trim()
  if (button.needsForce && force.value) body.force = true
  try {
    const result = await apiPost<PowerActionResponse>(
      `/power/${encodeURIComponent(props.host.mac)}/${button.action as PowerAction}`,
      body
    )
    emit('updated', normalizeHostPower(result.power))
    pending.value = null
  } catch (err) {
    error.value = errorMessage(err)
    if (err instanceof ApiError && err.details.power) {
      emit('updated', normalizeHostPower(err.details.power as HostPower))
    }
  } finally {
    busy.value = false
  }
}

const confirmLabel = computed(() => {
  const b = pending.value
  if (!b) return ''
  if (b.needsForce && !force.value) return `${b.label} (needs force)`
  return b.label
})

const canConfirm = computed(() => {
  const b = pending.value
  return Boolean(b) && !busy.value && (!b!.needsForce || force.value)
})
</script>

<template>
  <span class="power-actions" data-testid="power-actions">
    <template v-if="!pending">
      <button
        v-for="b in buttons"
        :key="b.action"
        type="button"
        class="btn btn-sm me-1"
        :class="b.action === 'shutdown' ? 'btn-outline-danger' : 'btn-outline-secondary'"
        :disabled="!b.enabled || disabled"
        :title="b.title"
        :data-action="`power-${b.action}`"
        @click="open(b)"
      >
        {{ b.label }}
      </button>
    </template>
    <span v-else class="power-confirm" data-testid="power-confirm">
      <span class="small text-secondary">
        {{ pending.label }} {{ host.hostname || host.mac }}?
      </span>
      <input
        v-model="reason"
        type="text"
        class="form-control form-control-sm power-reason"
        placeholder="Reason (optional)"
        :disabled="busy"
        data-testid="power-reason"
        @keydown.enter.prevent="canConfirm && confirm()"
        @keydown.esc.prevent="cancel"
      />
      <label
        v-if="pending.needsForce"
        class="form-check form-check-inline small mb-0"
        data-testid="power-force"
      >
        <input v-model="force" class="form-check-input" type="checkbox" :disabled="busy" />
        <span class="form-check-label">Force ({{ pending.forceReason }})</span>
      </label>
      <button
        type="button"
        class="btn btn-sm btn-outline-secondary"
        :disabled="busy"
        data-action="power-cancel"
        @click="cancel"
      >
        Keep
      </button>
      <button
        type="button"
        class="btn btn-sm"
        :class="pending.action === 'on' ? 'btn-primary' : 'btn-danger'"
        :disabled="!canConfirm"
        data-action="power-confirm"
        @click="confirm"
      >
        <span v-if="busy" class="spinner-border spinner-border-sm me-1" aria-hidden="true"></span>
        {{ confirmLabel }}
      </button>
      <span v-if="error" class="row-error" data-testid="power-error">{{ error }}</span>
    </span>
  </span>
</template>

<style scoped>
.power-actions {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-1);
}

.power-confirm {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--booty-space-2);
}

.power-reason {
  width: 12rem;
}
</style>
