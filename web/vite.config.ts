import { fileURLToPath, URL } from 'node:url'

import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

// Backend routes that the dev server forwards to the Go process so that
// `npm run dev` works against a locally running `booty`.
const API_ROUTES = [
  '/booty.json',
  '/info',
  '/flatcar',
  '/registry',
  '/register',
  '/unregister',
  '/hosts',
  '/ignition.json',
  '/healthz',
  '/version.json',
  '/data'
]

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const target = env.VITE_API_TARGET || 'http://localhost:8080'

  return {
    base: '/ui/',
    plugins: [vue()],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url))
      }
    },
    server: {
      proxy: Object.fromEntries(API_ROUTES.map((route) => [route, { target, changeOrigin: true }]))
    }
  }
})
