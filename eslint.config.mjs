// Root ESLint flat config — covers files outside frontend/ (playwright.config.ts,
// e2e/**, spike/**). Non-type-checked: root files have no tsconfig project.
// frontend/ is deliberately left to frontend/eslint.config.js, which applies
// type-checked rules via its tsconfig references.
import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import prettier from 'eslint-config-prettier'

export default tseslint.config(
  {
    ignores: [
      'node_modules/**',
      'graphify-out/**',
      '.beads/**',
      'dist/**',
      'build/**',
      '_bmad/**',
      '.agents/**',
      '.claude/**',
      '.opencode/**',
      'frontend/**',
      'spike/**',
      '*.config.mjs',
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['e2e/**/*.ts', 'playwright.config.ts', '*.ts', '*.mjs', '*.js'],
    extends: [tseslint.configs.disableTypeChecked],
  },
  prettier,
)
