import { fileURLToPath } from 'node:url'
import { mergeConfig, defineConfig, configDefaults } from 'vitest/config'
import viteConfig from './vite.config'

export default defineConfig((configEnv) =>
  mergeConfig(
    viteConfig(configEnv),
    defineConfig({
      test: {
        environment: 'jsdom',
        exclude: [...configDefaults.exclude, 'dist/**'],
        root: fileURLToPath(new URL('./', import.meta.url)),
        coverage: {
          provider: 'v8',
          include: ['src/**/*.{ts,vue}'],
          exclude: ['src/**/__tests__/**', 'src/main.ts'],
          reporter: ['text-summary', 'html', 'lcov', 'json-summary'],
          reportsDirectory: './coverage'
        }
      }
    })
  )
)
