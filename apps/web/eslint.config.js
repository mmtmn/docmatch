import js from '@eslint/js';
import { defineConfig, globalIgnores } from 'eslint/config';
import prettier from 'eslint-config-prettier';
import astro from 'eslint-plugin-astro';
import jsxA11yX from 'eslint-plugin-jsx-a11y-x';
import reactHooks from 'eslint-plugin-react-hooks';
import globals from 'globals';
import tseslint from 'typescript-eslint';
import docmatch from './tools/eslint-rules/index.js';

export default defineConfig([
  globalIgnores([
    'dist/',
    'node_modules/',
    '.astro/',
    'coverage/',
    'playwright-report/',
    'test-results/',
    '.vitest/',
    '.lighthouseci/',
  ]),

  js.configs.recommended,
  tseslint.configs.recommendedTypeChecked,
  {
    files: ['**/*.{ts,tsx,mts,cts}'],
    languageOptions: {
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    },
    rules: {
      // Event handlers returning promises are fine (`onSubmit={(e) => void run(e)}` is enforced).
      '@typescript-eslint/no-misused-promises': [
        'error',
        { checksVoidReturn: { attributes: false } },
      ],
    },
  },
  {
    files: ['**/*.{js,mjs,cjs}', '**/*.astro'],
    extends: [tseslint.configs.disableTypeChecked],
  },
  {
    files: ['**/*.{js,mjs,cjs,ts,tsx,mts,cts,astro}'],
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    rules: {
      '@typescript-eslint/consistent-type-imports': [
        'error',
        // `import('x').T` stays allowed: ambient `declare module` blocks in env.d.ts need it.
        { fixStyle: 'inline-type-imports', disallowTypeAnnotations: false },
      ],
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrors: 'none' },
      ],
      'no-console': ['error', { allow: ['warn', 'error'] }],
      // Product confirmations and errors must use styled, accessible UI.
      'no-alert': 'error',
      // BOM / NBSP only ever appear as escapes inside strings, templates and regexes.
      'no-irregular-whitespace': [
        'error',
        { skipStrings: true, skipTemplates: true, skipRegExps: true, skipComments: true },
      ],
    },
  },

  // React islands: hooks rules and accessibility checks.
  {
    files: ['**/*.{jsx,tsx}'],
    ...jsxA11yX.configs.recommended,
    languageOptions: {
      ...jsxA11yX.configs.recommended.languageOptions,
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
  },
  { files: ['**/*.{jsx,tsx}'], ...reactHooks.configs.flat.recommended },

  // Astro templates.
  astro.configs.recommended,
  astro.configs['jsx-a11y-recommended'],

  // Every visible string comes from packages/locales via t().
  {
    files: ['src/**/*.{jsx,tsx}', 'src/**/*.astro'],
    ignores: ['src/**/*.test.{jsx,tsx}'],
    plugins: { docmatch },
    rules: { 'docmatch/no-literal-ui-text': 'error' },
  },

  // CommonJS configs (Lighthouse CI only loads .cjs/.js via require).
  { files: ['**/*.cjs'], rules: { '@typescript-eslint/no-require-imports': 'off' } },

  // Tests and tooling may log and use string literals freely.
  {
    files: ['**/*.test.{ts,tsx}', 'test/**', 'e2e/**', 'tools/**', '*.config.{js,ts,cjs,mjs}'],
    rules: { 'no-console': 'off' },
  },

  prettier,
]);
