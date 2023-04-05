<script>
export default {
  data() {
    return {
        data: {},
    }
  },
  created() {
    fetch('/info')
      .then(response => response.json())
      .then(data => (this.data = data));
  }
}
</script>


<template>
    <h3>About Booty</h3>
    <div v-if="Object.keys(data).length">
        <p>Version: {{ data.booty.version }}</p>
        <p>Build date: {{ data.booty.timestamp }}</p>
        <p>Flatcar Version: {{ data.flatcar.version || "not yet polled" }}</p>
    </div>
    <p></p>
    <p>
        <a href="https://github.com/jeefy/booty/">PRs welcome!</a>
    </p>
</template>