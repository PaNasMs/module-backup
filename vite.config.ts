import { defineConfig } from 'vite'
import { resolve } from 'node:path'

const shared: Record<string, string> = {
  react: 'PaNasMsSDK.react',
  'react/jsx-runtime': 'PaNasMsSDK.jsx',
  '@tanstack/react-query': 'PaNasMsSDK.query',
  '@radix-ui/react-dialog': 'PaNasMsSDK.dialog',
  'react-router-dom': 'PaNasMsSDK.router',
}
for (const name of ['ui', 'runtime', 'client', 'i18n', 'navigation', 'external']) shared['@panasms/' + name] = 'PaNasMsSDK.' + name

export default defineConfig({
  resolve: { alias: { '@mdi/js': resolve('node_modules/@mdi/js/mdi.js') } },
  build: {
    outDir: resolve('dist/ui'), emptyOutDir: true,
    lib: { entry: resolve('frontend/backup.tsx'), name: 'PaNasMsModule_backup', formats: ['iife'], fileName: () => 'index.js', cssFileName: 'index' },
    rollupOptions: { external: Object.keys(shared), output: { globals: shared } },
  },
})
