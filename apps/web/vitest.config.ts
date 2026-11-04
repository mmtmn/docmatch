import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';
import { limitsPlugin } from './vite/limits';
import { pricingPlugin } from './vite/pricing';

export default defineConfig({
  plugins: [limitsPlugin(), pricingPlugin()],
  resolve: {
    alias: {
      // `astro:env/client` only exists inside Astro's pipeline; tests read a small stub.
      'astro:env/client': fileURLToPath(
        new URL('./test/stubs/astro-env-client.ts', import.meta.url),
      ),
      'astro:env/server': fileURLToPath(
        new URL('./test/stubs/astro-env-server.ts', import.meta.url),
      ),
    },
  },
  css: {
    modules: {
      // Stable class names make assertions on CSS-module classes readable.
      generateScopedName: '[local]',
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}', 'vite/**/*.test.ts', 'tools/**/*.test.mjs'],
    coverage: {
      provider: 'v8',
      include: ['src/lib/**', 'src/hooks/**', 'src/components/**', 'vite/**'],
      exclude: ['**/*.test.*', '**/*.d.ts'],
      reporter: ['text-summary', 'lcov'],
    },
  },
});
