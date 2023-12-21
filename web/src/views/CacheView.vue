<script>
export default {
  data() {
    return {
        registryData: {},
    }
  },
  methods: {
    getRegistryData() {
        fetch('/registry')
            .then(response => response.json())
            .then(data => (this.registryData = data));
    },
  },
  created() {
    this.getRegistryData();
  }
}
</script>


<template>
    <h3 v-if="Object.keys(registryData).length">Cached OCI Artifacts:</h3>
    <div class="container">
      <div class="row header mb-3">
        <div class="col-4">
          <span>Image</span>
        </div>
        <div class="col-7">
          <span>Digest</span>
        </div>
        <div class="col-1">
          <span>Synced?</span>
        </div>
      </div>
    </div>
    <div class="container" v-for="(image, index) of registryData">
      <div class="row row-striped mb-3">
        <div class="col-4">
          {{ image.image }}:{{ image.tag }}
        </div>
        <div class="col-7">
          {{ image.digest }}
        </div>
        <div class="col-1">
          {{ image.upToDate  }}
        </div>
      </div>
    </div>
</template>