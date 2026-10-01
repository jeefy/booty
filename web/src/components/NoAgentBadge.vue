<script setup lang="ts">
const NO_AGENT_TITLE =
  "Booted before Booty's update/health units existed (or with --builtin=none). Reboot to pick up the current Ignition; it then reports every 10 min."
</script>

<template>
  <span
    class="badge text-bg-light border no-agent-badge"
    :title="NO_AGENT_TITLE"
    data-testid="host-no-agent"
  >
    no agent yet
  </span>
</template>

<style scoped>
.no-agent-badge {
  color: var(--booty-muted);
  font-weight: 500;
  white-space: nowrap;
}
</style>
