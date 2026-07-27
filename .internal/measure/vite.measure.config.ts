import { defineConfig } from 'vite'
import solid from 'vite-plugin-solid'
import { visualizer } from 'rollup-plugin-visualizer'
import type { PluginOption } from 'vite'

/** Measurement variant of the production Vite config.
 *
 * Builds with rollup-plugin-visualizer emitting a gzip-sized treemap.
 * Total JS size is captured from the emitted files; per-package attribution
 * comes from the treemap's hover data.
 */
export default defineConfig({
  root: '.',
  base: './',
  plugins: [
    solid() as PluginOption,
    visualizer({
      filename: 'dist/stats-gzip.html',
      gzipSize: true,
      brotliSize: false,
      template: 'treemap',
      sourcemap: false,
    }) as PluginOption,
  ],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      input: process.env.VITE_ENTRY || 'kobalte-measure.html',
      output: {
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
})
