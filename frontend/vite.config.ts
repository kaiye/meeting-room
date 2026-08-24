import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3120,
    proxy: {
      '/api': { target: 'http://localhost:8090', ws: true },
    },
  },
})
