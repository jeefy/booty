<script setup lang="ts">
import { computed } from 'vue'
import {
  BLUEFIN_EXTENSIONS,
  BLUEFIN_MODES,
  NVIDIA_DRIVER_FLAVOURS,
  OS_OPTIONS,
  ROLE_OPTIONS,
  acceptsInstallDisk,
  hostRole,
  type BluefinExtension,
  type BluefinMode,
  type Host,
  type HostRole
} from '@/types'
import PowerBadge from '@/components/PowerBadge.vue'

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

const showInstallDisk = computed(() => acceptsInstallDisk(draft.value.os))

const ROLE_LABEL: Record<HostRole, string> = {
  worker: 'Worker',
  'control-plane': 'Control plane'
}

const role = computed<HostRole>({
  get: () => hostRole(draft.value.role),
  set: (value) => {
    draft.value.role = value
  }
})

const isBluefin = computed(() => draft.value.os === 'bluefin')

const MODE_LABEL: Record<BluefinMode, string> = {
  diskless: 'Diskless',
  installed: 'Installed'
}

const mode = computed<BluefinMode>({
  get: () => (draft.value.mode === 'installed' ? 'installed' : 'diskless'),
  set: (value) => {
    draft.value.mode = value
  }
})

function hasExtension(name: BluefinExtension): boolean {
  return (draft.value.extensions ?? []).includes(name)
}

function toggleExtension(name: BluefinExtension, on: boolean) {
  const rest = (draft.value.extensions ?? []).filter((e) => e !== name)
  draft.value.extensions = on ? [...rest, name] : rest
}

function isFixedExtension(name: string): boolean {
  return (BLUEFIN_EXTENSIONS as readonly string[]).includes(name)
}

// The driver slot is any name that is not a fixed extension, so a
// mistyped flavour stays visible until the server's 400 explains it.
const nvidiaDriver = computed<string>({
  get: () => (draft.value.extensions ?? []).find((e) => !isFixedExtension(e)) ?? '',
  set: (value) => {
    const rest = (draft.value.extensions ?? []).filter(isFixedExtension)
    const flavour = value.trim().toLowerCase()
    draft.value.extensions = flavour ? [...rest, flavour as BluefinExtension] : rest
  }
})
</script>

<template>
  <form class="host-form" @submit.prevent="emit('submit')">
    <div v-if="draft.power" class="host-form-power small text-secondary" data-testid="host-form-power">
      <span class="me-1">Power</span>
      <PowerBadge :power="draft.power" />
      <span v-if="draft.power.reason" class="ms-2">{{ draft.power.reason }}</span>
    </div>
    <div class="row g-2 align-items-start">
      <div class="col-12 col-md-2">
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
      <div v-if="showInstallDisk" class="col-12 col-md-2" data-testid="install-disk-field">
        <label class="form-label small mb-1" :for="`disk-${draft.mac}`">Install disk</label>
        <input
          :id="`disk-${draft.mac}`"
          v-model="draft.installDisk"
          type="text"
          class="form-control form-control-sm mono"
          placeholder="/dev/sda"
          :disabled="busy"
          :aria-describedby="`disk-help-${draft.mac}`"
        />
        <div :id="`disk-help-${draft.mac}`" class="form-text field-help">
          <template v-if="isBluefin">
            Disk systemd-sysinstall installs to on the next boot with Install set. Wiped on install.
          </template>
          <template v-else>
            Target disk for the installer; empty = first writable disk. Wiped on install.
          </template>
        </div>
      </div>
      <div class="col-12 col-md-2" data-testid="role-field">
        <label class="form-label small mb-1" :for="`role-${draft.mac}`">Role</label>
        <select
          :id="`role-${draft.mac}`"
          v-model="role"
          class="form-select form-select-sm"
          :disabled="busy"
          :aria-describedby="`role-help-${draft.mac}`"
        >
          <option v-for="option in ROLE_OPTIONS" :key="option" :value="option">
            {{ ROLE_LABEL[option] }}
          </option>
        </select>
        <div :id="`role-help-${draft.mac}`" class="form-text field-help">
          Control-plane hosts receive the cluster CA when the control plane is Booty-managed.
        </div>
      </div>
      <div class="col-12 col-md">
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
    </div>
    <div v-if="isBluefin" class="row g-2 align-items-start" data-testid="bluefin-fields">
      <div class="col-12 col-md-2">
        <label class="form-label small mb-1" :for="`mode-${draft.mac}`">Mode</label>
        <select
          :id="`mode-${draft.mac}`"
          v-model="mode"
          class="form-select form-select-sm"
          :disabled="busy"
          :aria-describedby="`mode-help-${draft.mac}`"
        >
          <option v-for="option in BLUEFIN_MODES" :key="option" :value="option">
            {{ MODE_LABEL[option] }}
          </option>
        </select>
        <div :id="`mode-help-${draft.mac}`" class="form-text field-help">
          Installed hosts boot their disk; Booty stops answering their UEFI HTTP Boot.
        </div>
      </div>
      <div class="col-12 col-md-2">
        <label class="form-label small mb-1" :for="`statedisk-${draft.mac}`">State disk</label>
        <input
          :id="`statedisk-${draft.mac}`"
          v-model="draft.stateDisk"
          type="text"
          class="form-control form-control-sm mono"
          placeholder="/dev/sdb"
          :disabled="busy"
          :aria-describedby="`statedisk-help-${draft.mac}`"
        />
        <div :id="`statedisk-help-${draft.mac}`" class="form-text field-help">
          Keeps /var of a diskless host (created once, never wiped); empty = RAM.
        </div>
      </div>
      <div :id="`extensions-${draft.mac}`" class="col-12 col-md">
        <span class="form-label small mb-1 d-block">Extensions</span>
        <div v-for="name in BLUEFIN_EXTENSIONS" :key="name" class="form-check form-check-inline">
          <input
            :id="`ext-${name}-${draft.mac}`"
            class="form-check-input"
            type="checkbox"
            :checked="hasExtension(name)"
            :disabled="busy"
            @change="toggleExtension(name, ($event.target as HTMLInputElement).checked)"
          />
          <label class="form-check-label small mono" :for="`ext-${name}-${draft.mac}`">
            {{ name }}
          </label>
        </div>
        <div class="form-text field-help">Opt-in sysexts; kubestellar needs k0s.</div>
      </div>
      <div class="col-12 col-md-2">
        <label class="form-label small mb-1" :for="`nvidia-${draft.mac}`">NVIDIA driver</label>
        <input
          :id="`nvidia-${draft.mac}`"
          v-model.lazy="nvidiaDriver"
          type="text"
          class="form-control form-control-sm mono"
          placeholder="none"
          :list="`nvidia-flavours-${draft.mac}`"
          :disabled="busy"
          :aria-describedby="`nvidia-help-${draft.mac}`"
        />
        <datalist :id="`nvidia-flavours-${draft.mac}`">
          <option
            v-for="flavour in NVIDIA_DRIVER_FLAVOURS"
            :key="flavour"
            :value="flavour"
          ></option>
        </datalist>
        <div :id="`nvidia-help-${draft.mac}`" class="form-text field-help">
          One nvidia-open-&lt;branch&gt; per host (Turing or newer); can be combined with zfs.
        </div>
      </div>
    </div>
    <div class="row g-2 align-items-end">
      <div class="col-12 col-md-7">
        <label class="form-label small mb-1" :for="`ostree-${draft.mac}`">OSTree image</label>
        <input
          :id="`ostree-${draft.mac}`"
          v-model="draft.ostreeImage"
          type="text"
          class="form-control form-control-sm mono"
          placeholder="ghcr.io/projectbluefin/bluefin:stable"
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
        <div class="form-check mb-1" data-testid="canary-field">
          <input
            :id="`canary-${draft.mac}`"
            v-model="draft.canary"
            class="form-check-input"
            type="checkbox"
            :disabled="busy"
            :aria-describedby="`canary-help-${draft.mac}`"
          />
          <label class="form-check-label small" :for="`canary-${draft.mac}`">
            Autopilot canary
          </label>
          <div :id="`canary-help-${draft.mac}`" class="form-text field-help">
            Gets a new Bluefin release first under --autopilot=full.
          </div>
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

.host-form-power {
  display: flex;
  align-items: center;
  margin-bottom: var(--booty-space-2);
}

.host-form .row + .row {
  margin-top: var(--booty-space-2);
}

.field-help {
  font-size: 0.75rem;
  line-height: 1.3;
  color: var(--booty-muted);
  margin-top: var(--booty-space-1);
}
</style>
