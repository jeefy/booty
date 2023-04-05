<script>
export default {
  data() {
    return {
        hostData: {},
        version: {},
    }
  },
  methods: {
    getBootyData() {
        fetch('/booty.json')
            .then(response => response.json())
            .then(data => (this.hostData = data));
        fetch('/version.json')
            .then(response => response.json())
            .then(data => (this.version = data));
    }
  },
  created() {
    this.getBootyData();
  }
}
</script>
<template>
    <div>
        <h3>Hello friend!</h3>
        <div>
            <div>Flatcar version {{ version.version || "not polled yet" }}</div>
            <div>
                <router-link to="/hosts">{{ hostData.hosts ? Object.keys(hostData.hosts).length : 0}} Hosts registered</router-link>
            </div>
            <div>{{ hostData.unknownHosts ? Object.keys(hostData.unknownHosts).length : 0}} Hosts pending</div>
        </div>
    </div>
</template>