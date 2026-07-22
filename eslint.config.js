import eslint from '@eslint/js'
import globals from 'globals'
import tseslint from 'typescript-eslint'
import vue from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'

const typed = {
  project: './tsconfig.eslint.json',
  tsconfigRootDir: import.meta.dirname,
  extraFileExtensions: ['.vue'],
}

export default [
  { ignores: ['**/dist/**', '**/node_modules/**', '**/test-results/**', '**/coverage/**'] },
  eslint.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,
  ...vue.configs['flat/essential'],
  {
    files: ['web/src/**/*.{ts,vue}', 'tests/e2e/**/*.ts', 'playwright.config.ts'],
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
      parserOptions: typed,
    },
    rules: {
      '@typescript-eslint/no-floating-promises': 'error',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_' }],
      'no-unused-vars': 'off',
      'no-control-regex': 'off',
    },
  },
  {
    files: ['tests/**/*.ts', 'web/src/**/*.test.ts'],
    rules: {
      // Playwright's dynamically typed response bodies and matcher methods cross
      // a deliberate runtime boundary; production sources retain typed checks.
      '@typescript-eslint/no-unsafe-assignment': 'off',
      '@typescript-eslint/no-unsafe-call': 'off',
      '@typescript-eslint/no-unsafe-member-access': 'off',
      '@typescript-eslint/no-unsafe-return': 'off',
      '@typescript-eslint/unbound-method': 'off',
      '@typescript-eslint/await-thenable': 'off',
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-base-to-string': 'off',
      '@typescript-eslint/no-unnecessary-type-assertion': 'off',
      '@typescript-eslint/require-await': 'off',
      '@typescript-eslint/prefer-promise-reject-errors': 'off',
    },
  },
  {
    files: ['web/src/**/*.vue'],
    languageOptions: {
      parser: vueParser,
      parserOptions: { ...typed, parser: tseslint.parser, extraFileExtensions: ['.vue'] },
    },
  },
]
