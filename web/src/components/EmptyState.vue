<script setup lang="ts">
defineProps<{
  title: string
  hint?: string
}>()
</script>

<template>
  <div class="empty-state">
    <strong>{{ title }}</strong>
    <span v-if="hint">{{ hint }}</span>
  </div>
</template>
