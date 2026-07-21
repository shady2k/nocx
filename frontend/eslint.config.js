// ESLint flat config — the frontend's golangci-lint (AGENTS.md: Go and
// TypeScript are held to the same bar). Type-checked rules are on: without the
// type information most of what golangci-lint catches on the Go side has no
// TypeScript equivalent. Formatting is prettier's job — eslint-config-prettier
// switches off every stylistic rule that would fight it.
import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import prettier from 'eslint-config-prettier'

export default tseslint.config(
  { ignores: ['dist/**', 'wailsjs/**'] },
  js.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,
  {
    languageOptions: {
      parserOptions: {
        // Both projects: tsconfig.json owns src/, tsconfig.node.json owns the
        // Vite config. A file in neither is a file nobody type-checks.
        project: ['./tsconfig.json', './tsconfig.node.json'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },
  {
    // Config files are checked by tsconfig.node.json and run in Node, not the
    // browser; they need no type-aware linting of their own.
    files: ['*.config.js', '*.config.ts'],
    extends: [tseslint.configs.disableTypeChecked],
  },
  prettier,
)
