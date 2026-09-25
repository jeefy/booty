<script setup lang="ts">
defineProps<{
  label?: string
}>()
</script>

<template>
  <div class="d-flex align-items-center gap-2 text-secondary py-4" role="status" aria-live="polite">
    <span class="spinner-border spinner-border-sm" aria-hidden="true"></span>
    <span>{{ label ?? 'Loading…' }}</span>
  </div>
</template>
