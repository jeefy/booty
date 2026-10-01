<script setup lang="ts">
import { computed } from 'vue'
import { POWER_STATE_LABEL, normalizeHostPower, type RawHostPower } from '@/types'
import { formatAbsolute, formatRelative } from '@/utils/time'

const props = defineProps<{
  power: RawHostPower | null | undefined
  /** Hide the "since" part (compact rows). */
  compact?: boolean
}>()

const power = computed(() => normalizeHostPower(props.power))
const label = computed(() => POWER_STATE_LABEL[power.value.state])
const title = computed(() => {
  const parts = [`Power: ${label.value.text}`]
  if (power.value.since) parts.push(`since ${formatAbsolute(power.value.since)}`)
  if (power.value.reason) parts.push(power.value.reason)
  if (power.value.request) parts.push(`request: ${power.value.request}`)
  if (power.value.cordoned) parts.push('node cordoned by Booty')
  if (power.value.probe.at) {
    parts.push(`probe ${power.value.probe.ok ? 'ok' : 'failed'} (${power.value.probe.method})`)
  }
  return parts.join(' · ')
})
</script>

<template>
  <span
    class="badge power-badge"
    :class="label.badge"
    :title="title"
    :data-state="power.state"
    data-testid="host-power"
  >
    <span class="power-dot" :class="label.dot" aria-hidden="true"></span>
    {{ label.text
    }}<span v-if="!compact && power.since" class="power-since">
      {{ formatRelative(power.since).replace(' ago', '') }}</span
    >
  </span>
</template>

<style scoped>
.power-badge {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  white-space: nowrap;
}

.power-dot {
  width: 0.5rem;
  height: 0.5rem;
  border-radius: 50%;
  background: currentColor;
  opacity: 0.85;
}

.power-dot--up {
  box-shadow: 0 0 0 2px rgba(255, 255, 255, 0.35);
}

.power-dot--busy {
  animation: power-pulse 1.2s ease-in-out infinite;
}

.power-dot--down,
.power-dot--off,
.power-dot--unknown {
  opacity: 0.6;
}

.power-since {
  opacity: 0.8;
  font-weight: 400;
}

@keyframes power-pulse {
  0%,
  100% {
    opacity: 0.35;
  }
  50% {
    opacity: 1;
  }
}
</style>
