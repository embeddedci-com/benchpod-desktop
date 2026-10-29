import path from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The desktop app's page, embedded into the benchpod-local binary.
//
//   npx vite            dev on :5176, /api proxied to benchpod-local on :7466
//   npx vite build      -> ../webapp/dist-app, copied into cmd/benchpod-local/web by `make local`
//
// The chat and result views are benchpod-ui's source, next to this repository, linked by path.
// That source resolves bare imports against its own node_modules, so the singletons are
// deduped to ours: two Reacts break hooks, two Mantines split the theme provider.
const bpui = path.resolve(__dirname, '../../benchpod-ui/src')

export default defineConfig({
  plugins: [react()],
  base: '/',
  resolve: {
    alias: { '@bpui': bpui },
    dedupe: ['react', 'react-dom', '@mantine/core', '@mantine/hooks'],
  },
  define: {
    'import.meta.env.VITE_BPD_VERSION': JSON.stringify(process.env.VERSION || 'dev'),
  },
  build: { outDir: 'dist-app', emptyOutDir: true },
  server: {
    port: 5176,
    fs: { allow: ['..', bpui] },
    proxy: { '/api': 'http://127.0.0.1:7466' },
  },
})
