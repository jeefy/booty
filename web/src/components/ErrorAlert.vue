<script setup lang="ts">
defineProps<{
  message: string
  retryLabel?: string
}>()

const emit = defineEmits<{
  retry: []
}>()
</script>

<template>
  <div
    class="alert alert-danger d-flex align-items-center justify-content-between gap-3 fade-in"
    role="alert"
  >
    <div class="flex-grow-1">
      <strong class="me-1">Request failed.</strong>
      <span data-testid="error-message">{{ message }}</span>
    </div>
    <button
      type="button"
      class="btn btn-sm btn-outline-danger flex-shrink-0"
      @click="emit('retry')"
    >
      {{ retryLabel ?? 'Retry' }}
    </button>
  </div>
</template>
