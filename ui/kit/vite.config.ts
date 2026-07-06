import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  base: './',
  server: {
    // dev mode: run shnkitd with --api-addr 127.0.0.1:8471 --token dev-token,
    // open http://localhost:5173/?token=dev-token
    proxy: {
      '/api': process.env.KITD_URL ?? 'http://127.0.0.1:8471',
      '/events': process.env.KITD_URL ?? 'http://127.0.0.1:8471',
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
  },
} as Parameters<typeof defineConfig>[0])
