import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// The Go backend serves the API; there is no Python service in the Go
// implementation. GRADOPS_BACKEND lets a developer point the dev server at a
// backend that is not on the default port.
const backend = process.env.GRADOPS_BACKEND ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': backend,
      '/healthz': backend,
    },
  },
})
