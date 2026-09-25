import { createRouter, createWebHashHistory } from 'vue-router'
import HomeView from '../views/HomeView.vue'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', name: 'home', component: HomeView },
    { path: '/hosts', name: 'hosts', component: () => import('../views/HostsView.vue') },
    { path: '/cache', name: 'cache', component: () => import('../views/CacheView.vue') },
    { path: '/config', name: 'config', component: () => import('../views/ConfigView.vue') },
    { path: '/about', name: 'about', component: () => import('../views/AboutView.vue') }
  ]
})

export default router
