import { defineConfig } from 'vite'

export default defineConfig({
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:25504',
      '/healthz': 'http://localhost:25504'
    }
  }
})
