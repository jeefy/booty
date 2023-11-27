<script>
export default {
  data() {
    return {
        hostData: {},
        oldData: {},
    }
  },
  methods: {
    getBootyData() {
        fetch('/booty.json')
            .then(response => response.json())
            .then(data => (this.hostData = data));
    },
    editHost: function(mac, host){
      this._originalHost = Object.assign({}, host);
      host.edit = true;
    },
    saveHost: function(mac, host){
      host.mac = mac;
      const requestOptions = {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(host)
      };
      fetch("/register", requestOptions)
        .then(response => {
          if (response.statusText != "OK") {
            console.log("Error saving host " + mac);
            console.log(response);
            this.getBootyData();
          }
        })
      host.edit = false;
      console.log("saved host " + mac);
    },
    deleteHost: function(mac, host, index) {
      
      host.mac = mac;
      const requestOptions = {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(host)
      };
      fetch("/unregister", requestOptions)
        .then(response => {
          if (response.statusText != "OK") {
            console.log("Error deleting host " + mac);
            console.log(response);
            this.getBootyData();
          }
        })
      console.log("deleted host " + mac);
      delete this.hostData.hosts[mac];
    },
    cancelHost: function(mac, host){
      Object.assign(host, this._originalHost);
      host.edit = false;
      console.log("cancel host edit " + mac);
    }
  },
  created() {
    this.oldData = {};
    this.getBootyData();
  }
}
</script>


<template>
    <h3 v-if="Object.keys(hostData.hosts).length">Registered hosts:</h3>
    <div class="container">
      <div class="row header mb-3">
        <div class="col-2">
          <span>MAC</span>
        </div>
        <div class="col-1">
          <span>Hostname</span>
        </div>
        <div class="col-1">
          <span>OS</span>
        </div>
        <div class="col-2">
          <span>Ignition File</span>
        </div>
        <div class="col-4">
          <span>OSTree Image</span>
        </div>
        <div class="col-2">
          <span>Actions</span>
        </div>
      </div>
    </div>
    <div class="container" v-for="(host, mac, index) of hostData.hosts">
      <div class="row row-striped mb-3">
        <div class="col-2">
          <span><a :href="`/ignition.json?mac=${mac}`" target="_blank">{{mac}}</a></span>
        </div>
        <div class="col-1">
          <span v-show="!host.edit">{{host.hostname}}</span>
          <input class="form-control" type="text" placeholder="Hostname" v-model="host.hostname" v-show="host.edit">
        </div>
        <div class="col-1">
          <span v-show="!host.edit">{{host.os}}</span>
          <input class="form-control" type="text" placeholder="OS" v-model="host.os" v-show="host.edit">
        </div>
        <div class="col-2">
          <span v-show="!host.edit">{{host.ignitionFile}}</span>
          <input class="form-control" type="text" placeholder="Ignition File" v-model="host.ignitionFile" v-show="host.edit">
        </div>
        <div class="col-4">
          <span v-show="!host.edit">{{host.ostreeImage}}</span>
          <input class="form-control" type="text" placeholder="OSTree Image" v-model="host.ostreeImage" v-show="host.edit">
        </div>
        <div class="col-2">
          <button class="btn btn-warning" v-show="!host.edit" v-on:click="editHost(mac, host)">edit</button>
          <button class="btn btn-secondary" v-show="host.edit" v-on:click="cancelHost(mac, host)">cancel</button>
          <button class="btn btn-success" v-show="host.edit" v-on:click="saveHost(mac, host)">save</button>
          <button class="btn btn-danger" v-show="!host.edit" v-on:click="deleteHost(mac, host, index)">delete</button>
        </div>
      </div>
    </div>
    <h3 v-if="Object.keys(hostData.unknownHosts).length">Unregistered hosts:</h3>
    <table>
          <tr v-for="(host, mac, index) of hostData.unknownHosts">
               <td>
                  <span><a :href="`/ignition.json?mac=${mac}`" target="_blank">{{mac}}</a></span>
               </td>
               <td>
                    <span v-show="!host.edit">{{host.hostname}}</span>
                    <input type="text" placeholder="Hostname" v-model="host.hostname" v-show="host.edit">
               </td>
               <td>
                    <span v-show="!host.edit">{{host.os}}</span>
                    <input type="text" placeholder="OS" v-model="host.os" v-show="host.edit">
               </td>
               <td>
                    <span v-show="!host.edit">{{host.ignitionFile}}</span>
                    <input type="text" placeholder="Ignition File" v-model="host.ignitionFile" v-show="host.edit">
               </td>
               <td>
                    <button v-show="!host.edit" v-on:click="editHost(mac, host)">edit</button>
                    <button v-show="host.edit" v-on:click="cancelHost(mac, host)">cancel</button>
                    <button v-show="host.edit" v-on:click="saveHost(mac, host)">save</button>
               </td>
          </tr>
     </table>
</template>