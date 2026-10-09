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
      // .internal/ is specs, plans and throwaway measurement spikes — a
      // spike's reference script is Node written to be read once and deleted,
      // and holding it to the product's lint is how a measurement turns into
      // an afternoon of config. 'spike/**' above already says this for the
      // older location; this is the same rule for where they live now.
      '.internal/**',
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
      // Vendored verbatim from the shady2k-skills plugin, for the reason
      // .prettierignore records beside the same files: the proof that matters
      // is `cmp` against the plugin's copy, so nothing here may rewrite them,
      // and a lint finding in one is the plugin's to fix, not ours.
      '.githooks/backlog-gate/check.mjs',
      '.githooks/backlog-gate/check-commits.mjs',
      '.githooks/backlog-gate/check-docs.mjs',
      '.githooks/backlog-gate/check-present.mjs',
      '.githooks/backlog-gate/check-product.mjs',
      '.githooks/backlog-gate/document-format.mjs',
      '.githooks/backlog-gate/time-format.mjs',
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
