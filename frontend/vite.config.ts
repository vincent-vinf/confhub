import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.CONFHUB_DEV_BACKEND ?? 'http://127.0.0.1:8080',
        changeOrigin: false,
        ws: true,
      },
      '/health': {
        target: process.env.CONFHUB_DEV_BACKEND ?? 'http://127.0.0.1:8080',
        changeOrigin: false,
      },
    },
  },
  build: { target: 'es2022', sourcemap: false },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    restoreMocks: true,
    coverage: {
      provider: 'v8',
      include: ['src/lib/api.ts', 'src/lib/config.ts', 'src/lib/format.ts'],
      reporter: ['text', 'json-summary', 'html'],
      reportsDirectory: '../test-results/frontend-coverage',
    },
  },
})
