import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import packageJson from './package.json' with { type: 'json' };

function omitOptimizedDependencySourceMaps(): Plugin {
  return {
    name: 'switch-codex:omit-optimized-dependency-source-maps',
    apply: 'serve',
    enforce: 'post',
    transform(code, id) {
      if (!id.includes('/node_modules/.vite/deps/')) {
        return null;
      }

      return {
        code,
        map: {
          version: 3,
          names: [],
          sources: [],
          sourcesContent: [],
          mappings: ''
        }
      };
    }
  };
}

export default defineConfig({
  root: 'src',
  plugins: [react(), omitOptimizedDependencySourceMaps()],
  define: {
    __APP_VERSION__: JSON.stringify(packageJson.version)
  },
  clearScreen: false,
  envPrefix: ['VITE_', 'TAURI_ENV_*'],
  optimizeDeps: {
    rolldownOptions: {
      output: {
        minify: true
      }
    }
  },
  // WKWebView spends tens of seconds parsing dependency source maps in dev.
  // Keep HMR, but serve executable code only.
  dev: {
    sourcemap: false
  },
  server: {
    host: '127.0.0.1',
    port: 1420,
    strictPort: true,
    watch: {
      ignored: ['**/src-tauri/**']
    }
  },
  build: {
    target: ['chrome105', 'safari13'],
    outDir: '../dist',
    emptyOutDir: true
  }
});
