// Root ESLint flat config — covers files outside frontend/ (playwright.config.ts,
// e2e/**, spike/**). Non-type-checked: root files have no tsconfig project.
// frontend/ is deliberately left to frontend/eslint.config.js, which applies
// type-checked rules via its tsconfig references.
import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import prettier from 'eslint-config-prettier'
import globals from 'globals'

export default tseslint.config(
  {
    ignores: [
      'node_modules/**',
      'graphify-out/**',
      '.beads/**',
      'dist/**',
      'build/**',
      '.agents/**',
      '.claude/**',
      'frontend/**',
      'spike/**',
      '*.config.mjs',
      // Playwright run artefacts, for the reason .prettierignore already
      // records: both tools walk the filesystem rather than the git index, so a
      // .gitignore'd directory is still scanned. `.e2e/` is worse than merely
      // noisy — the e2e container runs as root and leaves its disposable home
      // owned by root, so after a local run `npx eslint .` died on EACCES
      // before linting a single file (nocx-z9s9.8).
      '.e2e/**',
      'test-results/**',
      'playwright-report/**',
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['e2e/**/*.ts', 'playwright.config.ts', '*.ts', '*.mjs', '*.js'],
    extends: [tseslint.configs.disableTypeChecked],
  },
  {
    // The .mjs files under e2e/ and .githooks/ are Node scripts the gate runs,
    // not browser code. The .ts files here get `process` and `console` from
    // @types/node; a plain module gets them from nowhere, and no-undef is right
    // to say so until it is told what runtime this is.
    //
    // .githooks/ was missing from this list, and nothing reported it: the root
    // lint runs only in the pre-commit hook, and it had been crashing on the
    // e2e container's root-owned .e2e/ before it linted anything. Nineteen
    // errors accumulated behind that crash (nocx-z9s9.8).
    files: ['e2e/**/*.mjs', '.githooks/**/*.mjs'],
    languageOptions: { globals: globals.node },
  },
  prettier,
)
