import { nextTick } from 'vue'
import { createRouter, createWebHashHistory, type RouteLocationNormalized } from 'vue-router'
import HomeView from '../views/HomeView.vue'

const router = createRouter({
  history: createWebHashHistory(),
  scrollBehavior: () => ({ top: 0 }),
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

export const MAIN_CONTENT_ID = 'main-content'

/**
 * After an in-app navigation, move focus to <main> so keyboard and screen
 * reader users start at the new page instead of the nav link they clicked.
 * The first load is left alone (the browser owns initial focus), and so are
 * query/hash-only changes on the same page.
 */
export function focusMainAfterNavigation(to: RouteLocationNormalized, from: RouteLocationNormalized) {
  if (from.matched.length === 0 || to.path === from.path) return
  void nextTick(() => {
    document.getElementById(MAIN_CONTENT_ID)?.focus({ preventScroll: true })
  })
}

router.afterEach(focusMainAfterNavigation)

export default router
