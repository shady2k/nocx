#!/usr/bin/env node
/**
 * Kit identity fixture gate — runs the AST scanner on the fixture directory
 * and asserts every required identity is found, every excluded pattern is
 * absent, and undetermined expressions are reported.
 *
 * Run from frontend/ via `node lint-fixtures/check-kit-identities.mjs`
 * or indirectly through `sh lint-fixtures/gate.sh`.
 */
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scanKitIdentities } from './scan-kit-identities.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FIXTURE_DIR = resolve(__dirname, 'kit-identity-fixture')

const { byClass, identities, undetermined } = scanKitIdentities(FIXTURE_DIR)

const errors = []

// ── Required found identities ──────────────────────────────────────────────
function checkFound(className, file, label) {
  const owners = byClass.get(className)
  if (!owners) {
    errors.push(`MISSING: "${className}" not found — ${label}`)
    return
  }
  if (!owners.has(file)) {
    errors.push(
      `MISSING: "${className}" should be owned by ${file}, got [${[...owners].join(', ')}] — ${label}`,
    )
  }
}

function checkPart(file, partClass, rootClass) {
  const id = identities.get(file)
  if (!id) {
    errors.push(`MISSING: no identities for ${file} — ${partClass} part not registered`)
    return
  }
  if (!id.parts.includes(partClass)) {
    errors.push(
      `MISSING: "${partClass}" not listed as a part of ${file} — parts are [${id.parts.join(', ')}]`,
    )
  }
  if (rootClass && !id.roots.includes(rootClass)) {
    errors.push(
      `MISSING: "${rootClass}" not listed as a root in ${file} — roots are [${id.roots.join(', ')}]`,
    )
  }
}

checkFound('ui-fixture-plain', 'plain-literal.tsx', 'plain literal class')
checkFound('ui-fixture-tmpl', 'template-passthrough.tsx', 'template literal with passthrough')
checkFound('ui-fixture-rp', 'root-and-part.tsx', 'root identity')
checkPart('root-and-part.tsx', 'ui-fixture-rp__element', 'ui-fixture-rp')
checkFound('ui-fixture-vanilla', 'vanilla-emitter.ts', 'className = on a vanilla-emitted component')
checkPart('vanilla-emitter.ts', 'ui-fixture-vanilla__part', 'ui-fixture-vanilla')

// ── Required absent identities (must NOT be found) ──────────────────────────
function checkAbsent(className, label) {
  if (byClass.has(className)) {
    const owners = [...byClass.get(className)]
    errors.push(`FALSE POSITIVE: "${className}" found in [${owners.join(', ')}] — ${label}`)
  }
}

checkAbsent('ui-fixture-comment', 'appears only in a JSDoc comment')
checkAbsent('ui-fixture-qs', 'appears only as a querySelector argument')
checkAbsent('ui-fixture-vanilla-qs', 'appears only as a querySelector argument in a .ts file')

// ── Undetermined expressions must be reported ──────────────────────────────
const undetFiles = new Set(undetermined.map((u) => u.file))
if (!undetFiles.has('undetermined-class.tsx')) {
  errors.push(
    'MISSING REPORT: undetermined-class.tsx should produce an undetermined entry for labelClass()',
  )
}

// ── Result ──────────────────────────────────────────────────────────────────
if (errors.length > 0) {
  console.error('KIT-IDENTITY FIXTURE FAILURES:')
  for (const e of errors) {
    console.error(`  ✗ ${e}`)
  }
  process.exit(1)
}

console.log(
  `OK — ${byClass.size} classes across ${identities.size} files, ${undetermined.length} undetermined expression(s)`,
)
