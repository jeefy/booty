<script setup lang="ts">
import { ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'

const navOpen = ref(false)
const route = useRoute()

watch(
  () => route.fullPath,
  () => {
    navOpen.value = false
  }
)
</script>

<template>
  <nav class="navbar navbar-expand-md navbar-dark fixed-top app-navbar">
    <div class="container">
      <RouterLink class="navbar-brand" to="/">
        <span class="brand-mark" aria-hidden="true"></span>
        Booty
      </RouterLink>
      <button
        class="navbar-toggler"
        type="button"
        aria-controls="navbarMain"
        :aria-expanded="navOpen"
        aria-label="Toggle navigation"
        @click="navOpen = !navOpen"
      >
        <span class="navbar-toggler-icon"></span>
      </button>
      <div id="navbarMain" class="collapse navbar-collapse" :class="{ show: navOpen }">
        <ul class="navbar-nav me-auto mb-2 mb-md-0">
          <li class="nav-item">
            <RouterLink class="nav-link" to="/">Home</RouterLink>
          </li>
          <li class="nav-item">
            <RouterLink class="nav-link" to="/hosts">Hosts</RouterLink>
          </li>
          <li class="nav-item">
            <RouterLink class="nav-link" to="/cache">OCI Cache</RouterLink>
          </li>
          <li class="nav-item">
            <RouterLink class="nav-link" to="/config">Config</RouterLink>
          </li>
          <li class="nav-item">
            <RouterLink class="nav-link" to="/about">About</RouterLink>
          </li>
        </ul>
        <a
          class="nav-link small text-secondary"
          href="https://github.com/jeefy/booty/"
          target="_blank"
          rel="noopener noreferrer"
        >
          GitHub
        </a>
      </div>
    </div>
  </nav>

  <main class="container">
    <RouterView />
  </main>
</template>

<style scoped>
.app-navbar {
  min-height: var(--booty-nav-height);
  background-color: var(--booty-nav-bg);
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}

.navbar-brand {
  display: inline-flex;
  align-items: center;
  gap: var(--booty-space-2);
  font-weight: 700;
  letter-spacing: 0.02em;
}

.brand-mark {
  width: 0.625rem;
  height: 0.625rem;
  border-radius: 2px;
  background: var(--booty-accent);
  transform: rotate(45deg);
}

.nav-link.router-link-exact-active {
  color: #fff;
  box-shadow: inset 0 -2px 0 var(--booty-accent);
}
</style>
