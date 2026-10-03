import { createRouter, createWebHashHistory } from 'vue-router'
import HomeView from '../views/HomeView.vue'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', name: 'home', component: HomeView },
    { path: '/hosts', name: 'hosts', component: () => import('../views/HostsView.vue') },
    { path: '/storage', name: 'storage', component: () => import('../views/StorageView.vue') },
    { path: '/cache', redirect: '/storage' },
    { path: '/config', name: 'config', component: () => import('../views/ConfigView.vue') },
    {
      path: '/autopilot',
      name: 'autopilot',
      component: () => import('../views/AutopilotView.vue')
    },
    { path: '/about', name: 'about', component: () => import('../views/AboutView.vue') }
  ]
})

export default router
