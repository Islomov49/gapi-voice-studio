import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The dev server passes /api to the Studio service so the session cookie stays first-party.
const studio = process.env.STUDIO_URL ?? 'http://127.0.0.1:8090'

export default defineConfig({
  plugins: [react()],
  server: {
    host: process.env.HOST ?? '127.0.0.1',
    port: Number(process.env.PORT ?? 5180),
    allowedHosts: ['localhost', '127.0.0.1', '.ts.net'],
    proxy: { '/api': { target: studio, changeOrigin: false } },
  },
  build: { outDir: 'dist', sourcemap: false },
})
