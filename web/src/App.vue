<script setup lang="ts">
import { RouterLink, RouterView } from 'vue-router'

</script>

<template>
  <nav class="navbar navbar-expand-md navbar-dark fixed-top bg-dark">
    <a class="navbar-brand" href="#">Booty</a>
    <button class="navbar-toggler" type="button" data-toggle="collapse" data-target="#navbarCollapse" aria-controls="navbarCollapse" aria-expanded="false" aria-label="Toggle navigation">
      <span class="navbar-toggler-icon"></span>
    </button>
    <div class="collapse navbar-collapse" id="navbarCollapse">
      <div class="collapse navbar-collapse" id="navbarSupportedContent">
        <div class="navbar-nav mr-auto">
          <router-link class="nav-link" to="/">Home</router-link>
        </div>
        <div class="navbar-nav mr-auto">
          <router-link class="nav-link" to="/hosts">Hosts</router-link>
        </div>
        <div class="navbar-nav mr-auto">
          <router-link class="nav-link" to="/about">About</router-link>
        </div>
      </div>
      <!--
      <form class="d-flex">
        <input class="form-control me-2" type="search" placeholder="Search" aria-label="Search">
        <button class="btn btn-outline-success" type="submit">Search</button>
      </form>
      -->
    </div>
  </nav>

  <main role="main" class="container">
    <div class="jumbotron">
      <h1>Booty is up and running!</h1>
      <RouterView />
    </div>
  </main>
</template>

<style>
/* Show it is fixed to the top */
body {
  min-height: 75rem;
  padding-top: 4.5rem;
}
main.container {
  margin-top: 5rem;
}
div#app > nav.navbar {
  padding-left: 1rem;
}
#navbarCollapse > form {
  display: inline;
  width: 200;
}
</style>