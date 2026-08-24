#!/usr/bin/env node
/**
 * Error-vocabulary checker — no surface declares its own error vocabulary
 * (nocx-8sudy).
 *
 * Commit 7ce9b934 removed eight surfaces' private error elements. The rule
 * it enforced was already written in `frontend/src/ui/README.md` ("Toast is
 * the only notification affordance") but nothing checked it, so nine
 * surfaces each grew a status line of their own — each with its own class
 * name and its own colour, the shape AGENTS.md names under "Look for the
 * existing answer before you write a second one". This rule is the gate
 * that keeps a tenth one out.
 *
 * The rule has two halves, both required:
 *
 *   - the class NAMES an error/refusal concept — its own `-error`,
 *     `-refus*` vocabulary. The name half alone would catch a data model's
 *     field, and the colour half alone would catch every legitimate danger
 *     state.
 *   - the rule paints a danger colour: any declaration whose value reads
 *     the `--color-danger` or `--color-error` token (through `var()`, with
 *     a fallback, or inside `color-mix(...)` — css-tree gives us the AST,
 *     so `var( --color-danger , transparent )` is a hit, not an escape).
 *
 * A class that names an error but paints nothing (files' sticky
 * "refresh has stopped" row) is not this vocabulary and passes by the
 * colour half. A class that paints danger but does not name an error —
 * connections' `cm-impact-dangerous` badge, which labels a change as
 * auth-affecting rather than refusing — passes by the name half; that
 * near-miss (`dangerous`, `danger`, `failed`, `error`'s siblings) is why
 * the name test keys on error/refusal terms, not on every red-adjacent
 * word.
 *
 * Kit identities are derived, never hardcoded: the identity set comes from
 * scan-kit-identities.mjs over `src/ui/` (the prefix is not the test,
 * nocx-hav2). `ui-field-error`, `ui-toast[data-level='danger']`,
 * `ui-status-card[data-tone='danger']` and their relatives pass because
 * the kit owns them — a kit identity may declare a rule for its own error
 * state; a surface may not.
 *
 * The two grandfathered cases predate the rule and are baseline decisions,
 * each with its reason written in: `update-notice.error` (the update notice
 * IS the message — its error branch is the surface's whole content) and
 * `pane-error` (a pane whose terminal failed shows its own dead state over
 * the preserved scrollback, not a status line over a working UI). The
 * rule proves no NEW vocabulary appears.
 *
 * Invocation:
 *   node lint-fixtures/check-error-vocabulary.mjs
 *   node lint-fixtures/check-error-vocabulary.mjs --dir=<fixture dir>
 *
 * `--dir` scans exactly that directory (fixture mode): no baseline, so
 * every intentional violation is reported — the negative-fixture gate
 * asserts them by name.
 */
import { createRequire } from 'node:module'
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scanKitIdentities } from './scan-kit-identities.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'error-vocabulary-baseline.json')
const css = createRequire(import.meta.url)('css-tree')

/** A class segment naming an error/refusal concept — the two vocabularies
 *  nocx-8sudy removed, however they are joined. Deliberately NOT "danger"
 *  or "failed": a danger label is a classification, a failed state is a
 *  data-driven state (overview.css, ports.css), neither a refusal message. */
const ERROR_RE = /(?:^|_|-)(?:error|refus)/i

/** The danger tokens that mark the declaration as painting the error state. */
const DANGER_TOKENS = new Set(['--color-danger', '--color-error'])

/** Scan one CSS source for error-vocabulary violations. `kitIdentities` is
 *  the derived kit identity set (scanKitIdentities' byClass keys); a class
 *  the kit owns is the kit's grammar for its own error state, not a
 *  surface dialect. */
export function scanCss(file, source, kitIdentities = new Set()) {
  let ast
  let parseErrors = 0
  try {
    ast = css.parse(source, {
      positions: true,
      onParseError: () => {
        parseErrors++
      },
    })
  } catch {
    parseErrors++
  }
  if (parseErrors > 0) {
    // Fail closed: unparseable CSS is itself a defect, and a scanner that
    // silently skips a file would quietly stop guarding it. css-tree v3
    // recovers instead of throwing, so the parse-error callback is the
    // signal — a file that does not parse reports itself rather than
    // evading the rule.
    return [{ file, selector: '<unparseable>', line: 0 }]
  }
  const violations = []
  css.walk(ast, (node) => {
    if (node.type !== 'Rule') return

    const classes = []
    for (const selector of node.prelude.children.toArray()) {
      for (const part of selector.children.toArray()) {
        if (part.type === 'ClassSelector') classes.push(part.name)
      }
    }
    if (classes.length === 0) return

    // Danger paint: any declaration in the block reads a danger token.
    let painted = false
    for (const decl of node.block.children.toArray()) {
      if (decl.type !== 'Declaration' || !decl.value) continue
      css.walk(decl.value, (v) => {
        if (v.type === 'Identifier' && DANGER_TOKENS.has(v.name)) painted = true
      })
    }
    if (!painted) return

    for (const name of classes) {
      if (!ERROR_RE.test(name)) continue
      if (kitIdentities.has(name)) continue
      violations.push({ file, selector: `.${name}`, line: node.loc?.start.line ?? 0 })
    }
  })
  return violations
}

/** Every .css file under a directory, recursively. */
function allCSSUnder(dir) {
  const out = []
  if (!existsSync(dir)) return out
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...allCSSUnder(full))
    else if (entry.isFile() && entry.name.endsWith('.css')) out.push(full)
  }
  return out
}

/** The kit's identity set, derived from the components' own JSX — never
 *  from the `ui-` prefix (the prefix is not the test, nocx-hav2). */
export function kitIdentitySet() {
  const { byClass } = scanKitIdentities(resolve(FRONTEND_DIR, 'src/ui'))
  return new Set(byClass.keys())
}

/** Stable key that lets a baseline entry survive line moves. */
export function violationKey(v) {
  return `${v.file}:${v.selector}`
}

function loadBaseline() {
  const map = new Map()
  try {
    const data = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
    for (const v of data.violations) {
      map.set(violationKey(v), v)
    }
  } catch {
    // No baseline — every violation is an error
  }
  return map
}

// ─── CLI entry point ──────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const updateMode = process.env.NOCX_BASELINE_UPDATE === '1'
  const dirArg = process.argv.slice(2).find((a) => a.startsWith('--dir='))
  const kitIdentities = kitIdentitySet()

  let violations
  let baseline
  if (dirArg) {
    // Fixture mode: scan exactly the named directory, never the baseline —
    // the point is to see what the rule finds.
    const dir = resolve(dirArg.slice('--dir='.length))
    violations = allCSSUnder(dir).flatMap((f) =>
      scanCss(relative(FRONTEND_DIR, f), readFileSync(f, 'utf8'), kitIdentities),
    )
    baseline = new Map()
  } else {
    violations = allCSSUnder(resolve(FRONTEND_DIR, 'src/styles')).flatMap((f) =>
      scanCss(relative(FRONTEND_DIR, f), readFileSync(f, 'utf8'), kitIdentities),
    )
    baseline = updateMode ? new Map() : loadBaseline()
  }

  const unbaselined = violations.filter((v) => !baseline.has(violationKey(v)))

  for (const v of violations) {
    console.log(`${v.file}:${v.line}: ${v.selector}`)
  }

  if (unbaselined.length > 0) {
    console.error(
      `Error-vocabulary violations: ${violations.length} total, ${unbaselined.length} un-baselined.`,
    )
    for (const v of unbaselined) {
      console.error(`  NEW: ${v.file}:${v.line} ${v.selector}`)
    }
    console.error(
      'A surface declaring its own error vocabulary is the second dialect this',
      'rule exists to make impossible. Render the message through the kit instead',
      '(ui-field-error, Toast, StatusCard) — or, if a pre-existing case is',
      'genuinely a state rather than a refusal, record why in the baseline:',
      '`node lint-fixtures/update-error-vocabulary-baseline.mjs` (it refuses to grow).',
    )
    process.exitCode = 1
  } else if (violations.length > 0) {
    console.error(`Error-vocabulary violations: ${violations.length} (all baselined).`)
  }
}
