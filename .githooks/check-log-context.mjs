#!/usr/bin/env node
/**
 * Log-context ratchet — every NEW `.Debug(`/`.Info(`/`.Warn(`/`.Error(` call
 * on a logger must go through `log.From(ctx)` (or a logger derived from it),
 * baselined (nocx-n14oo.9).
 *
 * Measured 2026-09-16: 22 of 814 log call sites in this module carried the
 * exchange (WithContext / log.Start) by hand; the other 792 compiled, passed
 * review, and logged a line with no module, no request id and no trace,
 * because nothing enforced carrying them. log.From(ctx) removes the choice —
 * a call site that asks the context for its logger cannot forget the ids,
 * because there is nothing to forget. This ratchet is what stops a new call
 * site opting back into forgetting them.
 *
 * WHAT IT CANNOT SEE, the same admission the sibling ratchets make about
 * their own blind spots:
 *
 *   - It is a regex over source text, not a type checker. "Compliant" means
 *     an identifier's most recent assignment IN THE SAME FILE, read top to
 *     bottom, matches one of: `x := log.From(...)`, the second return of
 *     `_, x, _ := log.Start(...)`, or `x := y.With(...)` / `x :=
 *     y.WithContext(...)` where y was already compliant — or x is a function
 *     parameter typed `log.Logger` anywhere in the file. Once true it stays
 *     true for the rest of the FILE, not just the enclosing function, so a
 *     name reused for something else later in the same file inherits an
 *     amnesty it should not have. That trades a rare false negative for
 *     never blocking a legitimate call, which is the direction a ratchet
 *     should err in.
 *   - A call spanning two source lines — `lg.\n\tInfo(...)` — is invisible
 *     to it: the receiver and the method are on different lines and neither
 *     line alone matches. Existing code doing this is baselined like
 *     anything else; gofumpt does not produce this shape, so it is rare.
 *   - A `*slog.Logger` call site can never be compliant, because log.From
 *     returns log.Logger and nothing converts one to the other implicitly.
 *     Every such site the tree had on 2026-09-16 is baselined; migrating one
 *     off *slog.Logger onto log.Logger is what lets it clear the ratchet.
 *
 * Scope: every tracked, non-test .go file except internal/log/** itself —
 * the package that defines Debug/Info/Warn/Error is not a candidate for
 * calling them "through" its own not-yet-defined API, the same reasoning
 * check-deadcode.mjs gives for excluding …test support packages.
 *
 * Invocation: node .githooks/check-log-context.mjs   (from the repo root)
 * Regenerate: node .githooks/update-log-context-baseline.mjs
 * NOCX_BASELINE_UPDATE=1 prints every violation without failing, the same
 * escape hatch the other baselined checkers use.
 */
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
export const PROJECT_ROOT = resolve(__dirname, '..')
export const BASELINE_PATH = resolve(__dirname, 'log-context-baseline.json')

// Directories excluded from the scan outright — see the file doc.
const EXCLUDED_DIRS = ['internal/log/']

// A call site: `<ident>.<Method>(` at a word boundary, method one of the
// four above. Captures the receiver identifier and the method.
const CALL_RE = /(?:^|[^.\w])([A-Za-z_]\w*)\.(Debug|Info|Warn|Error)\(/g

// The import line for internal/log, with or without an explicit alias:
//   "github.com/shady2k/nocx/internal/log"
//   nocxlog "github.com/shady2k/nocx/internal/log"
const IMPORT_RE =
  /^\s*(?:import\s+)?(?:([A-Za-z_]\w*)\s+)?"github\.com\/shady2k\/nocx\/internal\/log"\s*$/

/** repo-relative, tracked, non-test *.go files, '/'-separated. */
export function trackedGoFiles() {
  const out = execFileSync('git', ['ls-files', '-z', '--', '*.go'], {
    cwd: PROJECT_ROOT,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
  })
  return out
    .split('\0')
    .filter((f) => f !== '')
    .filter((f) => !f.endsWith('_test.go'))
    .filter((f) => !EXCLUDED_DIRS.some((d) => f.startsWith(d)))
    .filter((f) => !f.startsWith('.claude/')) // nested worktree, see pre-commit's own note
}

/** The local alias `internal/log` is imported under in this file, or "log"
 * (the package's own name) when the file does not import it at all — a file
 * with no import can still have violations if it manufactures a raw
 * *slog.Logger, so scanning does not skip it, it just cannot see a `log.`
 * prefix any file that never imported the package could not have written. */
function resolveAlias(lines) {
  for (const line of lines) {
    const m = IMPORT_RE.exec(line)
    IMPORT_RE.lastIndex = 0
    if (m) return m[1] || 'log'
  }
  return 'log'
}

/** Violations in Go source text already read into memory, decoupled from the
 * filesystem and from git so a test can hand it a fixture directly. `file` is
 * only ever used as the label on the returned records. */
export function violationsInText(file, text) {
  const lines = text.split('\n')
  const alias = resolveAlias(lines)

  const fromRe = new RegExp(`\\b(\\w+)\\s*(?::=|=)[^\\n]*\\b${alias}\\.From\\(`)
  const startRe = new RegExp(
    `\\b\\w+\\s*,\\s*(\\w+)\\s*,\\s*\\w+\\s*:=[^\\n]*\\b${alias}\\.Start\\(`,
  )
  const chainRe = /\b(\w+)\s*(?::=|=)\s*(\w+)\.(?:With|WithContext)\(/
  const funcParamRe = new RegExp(`(\\w+)\\s+${alias}\\.Logger\\b`, 'g')

  const compliant = new Set()

  // Function parameters typed `log.Logger` are compliant for the whole file
  // (see the file doc on why per-function scoping is not attempted).
  for (const line of lines) {
    if (!/^\s*func\b/.test(line)) continue
    let m
    funcParamRe.lastIndex = 0
    while ((m = funcParamRe.exec(line))) compliant.add(m[1])
  }

  const violations = []
  lines.forEach((line, idx) => {
    const fm = fromRe.exec(line)
    if (fm) compliant.add(fm[1])
    const sm = startRe.exec(line)
    if (sm) compliant.add(sm[1])
    const cm = chainRe.exec(line)
    if (cm && compliant.has(cm[2])) compliant.add(cm[1])

    CALL_RE.lastIndex = 0
    let m
    while ((m = CALL_RE.exec(line))) {
      const [, receiver] = m
      if (compliant.has(receiver)) continue
      violations.push({ file, line: idx + 1, text: line.trim() })
    }
  })
  return violations
}

/** Every violation across the scanned tree. */
export function collectLogContextViolations() {
  const violations = []
  for (const file of trackedGoFiles()) {
    const abs = resolve(PROJECT_ROOT, file)
    violations.push(...violationsInText(file, readFileSync(abs, 'utf8')))
  }
  return violations
}

/** Stable key: file + trimmed call text — a line-number key would churn on
 * every unrelated edit above it, the same tradeoff check-control-goroutines
 * makes for its own `go` statements. */
export function violationKey(v) {
  return `${v.file}:${v.text}`
}

/** Load the committed baseline: {"file:text": true, ...}. */
export function loadBaseline() {
  const data = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
  return new Set(data.violations.map((v) => violationKey(v)))
}

// ─── CLI entry point ─────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const baseline = loadBaseline()
  const violations = collectLogContextViolations()
  const offenders = violations.filter((v) => !baseline.has(violationKey(v)))

  if (offenders.length > 0) {
    const heading =
      `${offenders.length} log call(s) do not go through log.From(ctx) — module, request id, ` +
      'trace and span are lost on these lines. Route them through log.From(ctx) (internal/log), ' +
      'or baseline a genuine exception: node .githooks/update-log-context-baseline.mjs'
    if (process.env.NOCX_BASELINE_UPDATE === '1') {
      console.log(heading)
    } else {
      console.error(heading)
    }
    for (const o of offenders) {
      console.error(`  ${o.file}:${o.line}: ${o.text}`)
    }
    if (process.env.NOCX_BASELINE_UPDATE !== '1') process.exit(1)
  }

  console.log(
    `OK — log-context ratchet: ${baseline.size} baselined call(s) not through log.From(ctx), ` +
      `${offenders.length} new`,
  )
}
