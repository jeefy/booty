<script>
export default {
  data() {
    return {
        hostData: {},
        version: {},
        oldData: {},
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
    <table>
          <tr v-for="(host, mac, index) of hostData.hosts">
               <td>
                    <span><a href="/ignition.json?mac={{mac}}" target="_blank">{{mac}}</a></span>
               </td>
               <td>
                    <span v-show="!host.edit">{{host.hostname}}</span>
                    <input type="text" v-model="host.hostname" v-show="host.edit">
               </td>
               <td>
                    <button v-show="!host.edit" v-on:click="editHost(mac, host)">edit</button>
                    <button v-show="host.edit" v-on:click="cancelHost(mac, host)">cancel</button>
                    <button v-show="host.edit" v-on:click="saveHost(mac, host)">save</button>
                    <button v-show="!host.edit" v-on:click="deleteHost(mac, host, index)">delete</button>
               </td>
          </tr>
     </table>

    <h3 v-if="Object.keys(hostData.unknownHosts).length">Unregistered hosts:</h3>
    <table>
          <tr v-for="(host, mac, index) of hostData.unknownHosts">
               <td>
                    <span>{{mac}}</span>
               </td>
               <td>
                    <span v-show="!host.edit">{{host.hostname}}</span>
                    <input type="text" v-model="host.hostname" v-show="host.edit">
               </td>
               <td>
                    <button v-show="!host.edit" v-on:click="editHost(mac, host)">edit</button>
                    <button v-show="host.edit" v-on:click="cancelHost(mac, host)">cancel</button>
                    <button v-show="host.edit" v-on:click="saveHost(mac, host)">save</button>
               </td>
          </tr>
     </table>
</template>