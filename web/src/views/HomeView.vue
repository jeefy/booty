<script>
export default {
  data() {
    return {
        hostData: {},
        version: {},
        pin: {},
        pinInput: '',
        pinStatus: '',
        pinBusy: false,
    }
  },
  methods: {
    getBootyData() {
        fetch('/booty.json')
            .then(response => response.json())
            .then(data => (this.hostData = data));
        fetch('/info')
            .then(response => response.json())
            .then(data => (this.version = data));
    },
    getPin() {
        fetch('/flatcar/pin')
            .then(response => response.json())
            .then(data => {
                this.pin = data;
                this.pinInput = data.version || '';
            });
    },
    setPin() {
        this.savePin(this.pinInput.trim());
    },
    clearPin() {
        this.savePin('');
    },
    savePin(version) {
        this.pinBusy = true;
        this.pinStatus = '';
        fetch('/flatcar/pin', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ version: version }),
        })
            .then(response => response.json())
            .then(data => {
                if (data.error) {
                    this.pinStatus = 'Error: ' + data.error;
                } else {
                    this.pin = data;
                    this.pinInput = data.version || '';
                    this.pinStatus = data.pinned
                        ? 'Pinned to ' + data.version + '. Downloading in background...'
                        : 'Pin cleared. Tracking latest version.';
                    this.getBootyData();
                }
            })
            .catch(err => { this.pinStatus = 'Error: ' + err; })
            .finally(() => { this.pinBusy = false; });
    }
  },
  created() {
    this.getBootyData();
    this.getPin();
  }
}
</script>
<template>
    <div>
        <h3>Hello friend!</h3>
        <div>
            <div>Flatcar version {{ version.flatcar ? (version.flatcar.version || "not polled yet") : "not polled yet" }}</div>
            <div>CoreOS version {{ version.coreos ? (version.coreos.version || "not polled yet") : "not polled yet" }}</div>
            <div>
                <router-link to="/hosts">{{ hostData.hosts ? Object.keys(hostData.hosts).length : 0}} Hosts registered</router-link>
            </div>
            <div>{{ hostData.unknownHosts ? Object.keys(hostData.unknownHosts).length : 0}} Hosts pending</div>
        </div>

        <hr />

        <div class="card" style="max-width: 40rem;">
            <div class="card-body">
                <h5 class="card-title">Flatcar version pin</h5>
                <p class="card-text" v-if="pin.pinned">
                    Currently pinned to <strong>{{ pin.version }}</strong>.
                    Booty will stay on this version instead of tracking the latest channel release.
                </p>
                <p class="card-text" v-else>
                    No version pinned. Booty tracks the latest version on the configured channel.
                </p>
                <div class="input-group mb-2">
                    <input
                        type="text"
                        class="form-control"
                        placeholder="e.g. 3815.2.0"
                        v-model="pinInput"
                        :disabled="pinBusy"
                        @keyup.enter="setPin" />
                    <button class="btn btn-primary" type="button" @click="setPin" :disabled="pinBusy || !pinInput.trim()">
                        Pin version
                    </button>
                    <button class="btn btn-outline-secondary" type="button" @click="clearPin" :disabled="pinBusy || !pin.pinned">
                        Clear pin
                    </button>
                </div>
                <div v-if="pinStatus" class="small text-muted">{{ pinStatus }}</div>
            </div>
        </div>
    </div>
</template>
