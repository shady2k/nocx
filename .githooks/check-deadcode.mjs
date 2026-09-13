#!/usr/bin/env node
/**
 * Dead-code ratchet — `deadcode` (golang.org/x/tools/cmd/deadcode), baselined.
 *
 * deadcode answers one question: "is this Go function reachable from main()?"
 * It runs Rapid Type Analysis over the module and reports every function no
 * executable reaches, grouping by package. That is a floor, and a narrow one:
 *
 *   - It does not report a function that is reachable but never *read* — a
 *     `readonly` field written by four call sites and read by none is invisible
 *     to it, exactly like `restoreDescriptor` in the frontend (tabs.ts:456,
 *     main.tsx:226, state/tab-model.ts:255). Reachability is not consumption.
 *   - It analyzes only Go; nothing TypeScript reaches is its concern (that is
 *     the job of knip via lint-fixtures/check-dead-exports.mjs).
 *   - Without -test it counts test-only helpers as dead (86 on 2026-08-06; 9
 *     with -test), so the committed baseline includes them and they may only
 *     shrink. Do not read a green gate as proof that no dead paths exist.
 *
 * AND — the one that changes what this gate is worth — RTA treats a method
 * reached through an interface as reachable, so a method on a type that some
 * live interface value can hold is never reported, whether or not any
 * production code calls it. That is not a corner case here: it is why
 * `deadcode -filter 'nocx/internal/content'` prints nothing and always has,
 * including on the tree where ContentDB.Add had no caller outside its own
 * tests (nocx-rtg0). This ratchet runs the same analysis with no filter, so it
 * shares the blind spot exactly. It catches a dead FUNCTION and a dead method
 * on a type nothing dispatches through; it cannot catch a dead method behind a
 * live interface, and no configuration of deadcode makes it. For that question
 * the tool is `deadcode -whylive <symbol>` (AGENTS.md), read by a person.
 *
 * Policy: existing violations are baselined warnings; a function deadcode
 * reports that the baseline does not list is a new violation and fails the
 * job. The baseline may only shrink — removing an entry is always a pass.
 * Regenerate with `node .githooks/update-deadcode-baseline.mjs`, which refuses
 * to write a baseline that grows.
 *
 * Invocation: node .githooks/check-deadcode.mjs   (from the repo root)
 *   --platform=<goos>/<goarch>  assert which platform is being analysed
 *   --tags=<a,b>                override the build tags (default: see below)
 * NOCX_BASELINE_UPDATE=1 prints every violation without failing, the same
 * escape hatch the CSS checkers use for their fixture gates.
 */
import { spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const PROJECT_ROOT = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'deadcode-baseline.json')

// Overridable so a pinned binary can be tested; defaults to PATH lookup,
// which the pre-commit hook primes with $(go env GOPATH)/bin.
const DEADCODE_CMD = process.env.DEADCODE || 'deadcode'

const UNREACHABLE_RE = /^(.+?):\d+:\d+: unreachable func: (.+)$/

// Packages that exist only to support tests — `storagetest`, `vaulttest` —
// are not candidates at all, rather than 23 baselined warnings that mean
// nothing.
//
// The gate runs deadcode WITHOUT -test on purpose, and that must not change:
// with -test, a production function whose only callers are its own tests
// looks reachable, and that is the exact defect this repo has shipped twice
// (nocx-rtg0's ContentDB.Add, nocx-ak2d's InstalledFactStore.Record — written,
// covered, wired, and called by nobody). Keeping -test off is what closes it.
//
// The cost of keeping it off is that test HELPERS are unreachable from main()
// too — not a finding but a definition. Those land in one of two places: a
// `_test.go` file, which deadcode never compiles without -test and so never
// reports, or a test-support PACKAGE, which it compiles like any other and
// reports like any other. The second case fell through the classification,
// so a quarter of the baseline was noise and adding one test helper failed
// the commit.
//
// Matched on the package directory, not on a file name: a directory named
// `…test` is the Go convention for this and is checkable, whereas a file
// called testseam.go inside a production package is a guess. Those stay
// baselined, deliberately.
//
// This does NOT weaken the check that matters. The exclusion is matched on
// the package directory, so a dead function in an ordinary production
// package is still reported — which is how InstalledFactStore.Record was
// reported for as long as it had no caller. It is wired now (nocx-ak2d) and
// so is legitimately absent from the baseline; do not read that absence as
// the exclusion having swallowed it.
const TEST_SUPPORT_PKG_RE = /(^|\/)[a-z0-9]*test\/[^/]+\.go:\d+:\d+: unreachable func:/

// ─── cgo `//export` callbacks are roots ───────────────────────────────────
//
// A function marked `//export` is called from C, and RTA cannot see a C-to-Go
// call because there is no Go call site to see. So deadcode reports the
// callback dead — and with it everything ONLY the callback reaches, which is
// the half that matters: internal/emulator/ghostty's copyBorrowed and lookup
// are called by the six `//export`ed nocxGo* callbacks and by nothing else
// (nocx-ygxjv.16).
//
// Three answers that do not work, and why, because each looks reasonable:
//
//   - Baseline them. The baseline is a list of functions nobody has wired
//     yet, and its whole value is that a NEW name in it is a finding. Six
//     callbacks in it would mean the gate could not tell the seventh — the one
//     whose C side nobody wrote — from a callback that C calls every keystroke.
//     The helpers would need entries of their own, and those go stale the
//     moment a callback stops or starts using one.
//   - Suppress the callback's own report. The helper it reaches is still
//     reported, so the gate stays red and the suppression bought a shorter red.
//   - Compute the reachability here, from the package's source. That is a
//     second answer to the question deadcode already answers, and it can only
//     be wrong in the direction that matters: an edge that looks like a call
//     and is not — a shadowing local, a method name on another type — marks a
//     dead function live, and the report disappears. A gate that hides one
//     dead function to fix a false positive is the wrong trade.
//
// So the roots are SUPPLIED to the analyser and the analyser does the rest.
// deadcode's roots are the main packages' init and main functions, loaded
// through `go list`, which honours -overlay. For every file that declares an
// `//export`ed callback this script renders a file in the SAME package — same
// build constraints and same cgo preamble, so it compiles under exactly the
// conditions the callback's own file does — whose init() calls each callback.
// The rendered files are virtual: written outside the tree and reached through
// an overlay, so they are never committed, never compiled into the product,
// and never seen by gofmt, golangci-lint or a person reading the package.
//
// What this can and cannot hide is exact, which is the whole reason for doing
// it this way. A function the callbacks reach is a function C reaches, and
// reporting it is the false positive being fixed. A function they do NOT reach
// is untouched: the synthetic init adds call edges and nothing else, so a dead
// function in a cgo package is still reported, and so is a `//export` comment
// in a file that does not import "C" — cgo honours no directive there, so it
// is not a root here either. The test that holds both halves is
// frontend/lint-fixtures/check-deadcode.test.mjs, over the cgo fixture module
// beside it.

/** A directory the go tool does not walk: dot/underscore prefixes, node_modules. */
const INVISIBLE_DIR_RE = /^(?:[._]|node_modules$)/

/** `//export Name` — the directive cgo requires immediately above the function. */
const EXPORT_DIRECTIVE_RE = /^\/\/export[ \t]+([A-Za-z_]\w*)[ \t]*$/

/** The import that makes a file a cgo file: `import "C"`, or `"C"` in an import block. */
const CGO_IMPORT_RE = /^[ \t]*(?:import[ \t]+)?"C"[ \t]*(?:\/\/.*)?$/

/** The first line of a top-level declaration; the cgo import cannot come after one. */
const TOP_LEVEL_DECL_RE = /^(?:func|var|const|type)\b/

/** A cgo `//export`ed callback: the name and its parameter list, as written. */
const EXPORT_FUNC_RE = /^func[ \t]+([A-Za-z_]\w*)[ \t]*\(([^)]*)\)/

/** Every non-test Go file under root, paths sorted for a deterministic render. */
function goSources(root) {
  const files = []
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name)
      if (entry.isDirectory()) {
        if (INVISIBLE_DIR_RE.test(entry.name)) continue
        // A nested module (third_party/libghostty-vt/linkprobe) is not part of
        // the program `go list ./...` loads here, and neither is its overlay.
        if (existsSync(join(path, 'go.mod'))) continue
        walk(path)
      } else if (entry.isFile() && entry.name.endsWith('.go') && !entry.name.endsWith('_test.go')) {
        files.push(path)
      }
    }
  }
  walk(root)
  return files.sort()
}

/**
 * Every callback under root: the file, its `//export`ed names, the parameter
 * list of each, and the pieces of the file the renderer has to copy (build
 * constraints, package clause, cgo preamble).
 *
 * A directive whose function declaration cannot be read on ONE line is an
 * error rather than a skip: the script renders that call's arguments from the
 * parameters, and a root it silently dropped would put the callback back in
 * the report as a violation nobody could explain.
 */
export function findCgoExportCallbacks(root) {
  const found = []
  for (const file of goSources(root)) {
    const lines = readFileSync(file, 'utf8').split('\n')

    // The `"C"` import, and only in the file header: `import "C"` and the
    // block form are both accepted, and a `"C"` string in a function body is
    // not an import.
    let importIndex = -1
    for (let i = 0; i < lines.length; i++) {
      if (TOP_LEVEL_DECL_RE.test(lines[i])) break
      if (CGO_IMPORT_RE.test(lines[i])) {
        importIndex = i
        break
      }
    }
    if (importIndex < 0) continue

    const names = []
    let inBlockComment = false
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i]
      if (inBlockComment) {
        if (line.includes('*/')) inBlockComment = false
        continue
      }
      if (line.includes('/*')) {
        // An example of the directive written inside a comment is not one.
        if (!line.includes('*/')) inBlockComment = true
        continue
      }
      const directive = EXPORT_DIRECTIVE_RE.exec(line)
      if (!directive) continue
      const decl = EXPORT_FUNC_RE.exec(lines[i + 1] ?? '')
      if (!decl || decl[1] !== directive[1]) {
        throw new Error(
          `${relative(root, file)}:${i + 1}: //export ${directive[1]} is not immediately above ` +
            `"func ${directive[1]}(…)" on one line, so this script cannot render its call`,
        )
      }
      names.push({ name: directive[1], params: decl[2] })
    }
    if (names.length === 0) continue

    found.push({
      root,
      file,
      rel: relative(root, file),
      names,
      constraints: buildConstraints(lines),
      pkg: packageName(lines, file),
      preamble: cgoPreamble(lines, importIndex),
    })
  }
  return found
}

/** The `//go:build` / `// +build` lines of a file, in order, constraints only. */
function buildConstraints(lines) {
  const out = []
  for (const line of lines) {
    if (line.startsWith('package ')) break
    if (/^\/\/go:build[ \t]/.test(line) || /^\/\/[ \t]*\+build[ \t]/.test(line)) out.push(line)
  }
  return out
}

function packageName(lines, file) {
  const clause = lines.find((line) => line.startsWith('package '))
  const name = clause && /^package[ \t]+([A-Za-z_]\w*)/.exec(clause)?.[1]
  if (!name) throw new Error(`${file}: no package clause`)
  return name
}

/** The comment cgo reads as the file's C preamble: the one above `import "C"`. */
function cgoPreamble(lines, importIndex) {
  let i = importIndex - 1
  while (i >= 0 && lines[i].trim() === '') i--
  if (i < 0) return []
  const last = lines[i].trim()
  if (last.endsWith('*/')) {
    let start = i
    while (start >= 0 && !lines[start].includes('/*')) start--
    return start < 0 ? [] : lines.slice(start, i + 1)
  }
  if (last.startsWith('//')) {
    let start = i
    while (start - 1 >= 0 && lines[start - 1].trim().startsWith('//')) start--
    return lines.slice(start, i + 1)
  }
  return []
}

/**
 * The name a rendered root takes for a source file. It carries the source's
 * trailing `_GOOS` / `_GOARCH` components — `effects.go` renders
 * `zz_deadcode_cgo_roots_effects.go`, `gated_darwin.go` renders
 * `…_gated_darwin.go` — because go infers an implicit build constraint from the
 * file NAME as well as from its `//go:build` line, and a root that leaked onto
 * every platform would make a platform-gated callback live in an analysis that
 * never compiles its file.
 */
export function rootsFileName(sourceFile) {
  return `zz_deadcode_cgo_roots_${basename(sourceFile)}`
}

/**
 * The synthetic file rendered for one callback-bearing source file: the whole
 * declaration, because it is compiled as if it were in the package.
 */
export function renderCgoRootsFile(callback) {
  const { constraints, pkg, preamble, names, file } = callback
  const calls = names.map(({ name, params }) => `${name}(${zeroValueArgs(params, file, name)})`)
  const out = []
  if (constraints.length > 0) out.push(...constraints, '')
  out.push(`package ${pkg}`, '')
  if (preamble.length > 0) out.push(...preamble)
  out.push('import "C"', '')
  out.push(
    '// Rendered by .githooks/check-deadcode.mjs, in an overlay: never written to',
    '// this tree, never compiled into the product. See that script for why the',
    '// cgo callbacks below are roots a Go call graph cannot see.',
    'func init() {',
    ...calls.map((call) => `\t${call}`),
    '}',
  )
  return `${out.join('\n')}\n`
}

/**
 * One zero-valued argument per parameter, as `*new(T)`: the expression is valid
 * for every type a cgo callback can take, including pointers and function
 * types, and needs no knowledge of the type beyond its name.
 *
 * Parameters are all named or all unnamed within one list (Go's rule), which is
 * what makes `a, b int` readable without a parser: a lone identifier belongs to
 * a group whose type is on a later part, and in a list where no part carries a
 * type at all, every part IS the type.
 */
function zeroValueArgs(params, file, name) {
  const parts = splitTopLevel(params, ',')
  if (parts.length === 1 && parts[0].trim() === '') return ''
  const named = parts.some((part) => part.trim().split(/\s+/).length > 1)
  const args = []
  let pending = 0
  for (const part of parts) {
    const tokens = part.trim().split(/\s+/)
    if (named && tokens.length === 1) {
      pending++
      continue
    }
    const type = named ? tokens[tokens.length - 1] : tokens.join(' ')
    for (let i = 0; i <= pending; i++) args.push(zeroValue(type))
    pending = 0
  }
  if (pending > 0) {
    throw new Error(`${file}: ${name} has a parameter list this script cannot read: (${params})`)
  }
  return args.join(', ')
}

function zeroValue(type) {
  // A variadic parameter is callable with one value of the element type.
  return `*new(${type.startsWith('...') ? type.slice(3) : type})`
}

/** Split on a separator at nesting depth 0, so `map[string]int, error` stays two. */
function splitTopLevel(text, sep) {
  const parts = []
  let depth = 0
  let current = ''
  for (const ch of text) {
    if (ch === '(' || ch === '[' || ch === '{') depth++
    if (ch === ')' || ch === ']' || ch === '}') depth--
    if (ch === sep && depth === 0) {
      parts.push(current)
      current = ''
      continue
    }
    current += ch
  }
  parts.push(current)
  return parts
}

/**
 * Write the rendered files into a temp directory and the overlay that maps
 * each into its package. Returns the directory to remove and the overlay path
 * to hand the go command; nothing is written inside the repository.
 */
function writeCgoRootsOverlay(callbacks) {
  const dir = mkdtempSync(join(tmpdir(), 'nocx-deadcode-cgo-roots-'))
  const replace = {}
  callbacks.forEach((callback, n) => {
    const solid = join(dir, `${n}-${basename(callback.file)}`)
    writeFileSync(solid, renderCgoRootsFile(callback))
    replace[join(dirname(callback.file), rootsFileName(callback.file))] = solid
  })
  const overlay = join(dir, 'overlay.json')
  writeFileSync(overlay, `${JSON.stringify({ Replace: replace }, null, 2)}\n`)
  return { dir, overlay }
}

/**
 * ONE PLATFORM PER RUN — the host's, and that is a change with a history.
 *
 * deadcode analyses ONE GOOS at a time, so a build-tag-gated pair
 * (secretservice_linux.go / secretservice_other.go) only ever has one half
 * compiled. A baseline generated on one machine therefore listed that
 * machine's halves and nobody else's, and the other platform's halves read as
 * NEW violations — which made the ratchet unpassable on macOS while CI, which
 * never ran deadcode at all, said nothing (nocx-0odm). The answer then was to
 * stop asking the host: analyse darwin AND linux from wherever this ran, with
 * CGO_ENABLED=0, and take the union.
 *
 * Wails v3 ended that, and the option does not exist rather than being hard to
 * find: v3 requires cgo on both targets (its own cross-platform guide — macOS
 * "Yes / Docker with macOS SDK", Linux "Yes / Docker, or native if a C
 * compiler is available"). Measured on the migration branch: CGO_ENABLED=1
 * with -tags gtk3 exits 0, CGO_ENABLED=0 with the same tag fails inside wails'
 * own package. There is no no-cgo mode left to build a cross-GOOS analysis on.
 *
 * The trick existed only because a developer machine cannot be both a Mac and
 * a Linux box. CI already has both, so each job analyses its OWN platform
 * natively with cgo on — ci-mac for darwin, ci-linux for linux — and the
 * baseline stays the union of the two. A single-platform run is a SUBSET of
 * that union, so a violation the baseline does not list still fails the job;
 * the only thing given up is that a stale entry belonging to the other
 * platform goes unreported here. A ratchet fails on new violations; it does
 * not need to notice shrinkage. (What must not happen is the baseline being
 * regenerated from one platform and losing the other's entries —
 * update-deadcode-baseline.mjs is what prevents that.)
 *
 * `--platform=<goos>/<goarch>` does not cross-compile; it ASSERTS. Cross-GOOS
 * is exactly what cgo took away, so a mismatch is a job wired to the wrong
 * runner and is reported as that rather than as a compiler error from inside
 * wails.
 */
export function resolvePlatform(argv, hostEnv) {
  const arg = argv.find((a) => a.startsWith('--platform='))
  const host = { GOOS: hostEnv.GOOS, GOARCH: hostEnv.GOARCH }
  if (!arg) return host

  const [GOOS, GOARCH] = arg.slice('--platform='.length).split('/')
  if (!GOOS || !GOARCH) {
    throw new Error(`--platform wants <goos>/<goarch>, got "${arg}"`)
  }
  if (GOOS !== host.GOOS) {
    throw new Error(
      `--platform asks for ${GOOS} on a ${host.GOOS} host. Wails v3 needs cgo, ` +
        `so there is no cross-GOOS analysis to fall back on: run this on a ${GOOS} ` +
        `machine (in CI, the job for that platform) rather than here.`,
    )
  }
  return { GOOS, GOARCH }
}

/**
 * The build tags, derived the way the Makefile derives WAILS_PLATFORM_TAGS and
 * the pre-commit hook derives golangci-lint's: `gtk3` on Linux when
 * webkit2gtk-4.1 resolves, empty elsewhere. Wails v3 defaults to
 * GTK4/webkitgtk-6.0, which is not the surface this product ships (ADR-0007),
 * so without the tag the analysis fails to compile rather than reporting
 * anything — loudly, which is the correct failure.
 *
 * Derived rather than passed so that a local run and the CI job agree without
 * the caller having to remember; `--tags=` overrides for the awkward host.
 */
export function resolveBuildTags(argv, hostEnv) {
  const arg = argv.find((a) => a.startsWith('--tags='))
  if (arg) return arg.slice('--tags='.length)
  if (hostEnv.GOOS !== 'linux') return ''
  const probe = spawnSync('pkg-config', ['--exists', 'webkit2gtk-4.1'], { stdio: 'ignore' })
  return probe.status === 0 ? 'gtk3' : ''
}

/** The host's own GOOS/GOARCH, asked of the toolchain rather than of node. */
export function hostPlatform() {
  const proc = spawnSync('go', ['env', 'GOOS', 'GOARCH'], { encoding: 'utf8' })
  if (proc.status !== 0) {
    throw new Error(`go env failed: ${(proc.stderr || '').trim()}`)
  }
  const [GOOS, GOARCH] = proc.stdout.trim().split('\n')
  return { GOOS, GOARCH }
}

/**
 * Run deadcode from the repo root for one platform and return its raw stdout.
 * A nonzero exit is a tool failure (module does not compile, the binary is
 * missing) and is never a pass.
 *
 * A signal-kill is a third thing, and it needs its own message. `status` is
 * null rather than nonzero when the kernel kills the process, and stdout and
 * stderr are both empty, so the nonzero branch used to report the bare string
 * "deadcode exited null" underneath the banner "FAIL: deadcode ratchet" — which
 * reads as a finding about the tree and invites the reader to go make something
 * reachable in a tree where the ratchet is already green. It is not a finding:
 * the analysis peaks around 1.4 GB and the hook has two containers running
 * beside it, so on a small machine the OOM killer takes it (measured
 * 2026-08-14). Say so, and say to re-run rather than to edit anything.
 */
function runDeadcode(
  platform,
  tags,
  { root = PROJECT_ROOT, deadcodeCmd = DEADCODE_CMD, overlay } = {},
) {
  // The overlay is how the cgo roots reach the analyser: `go list` (which
  // deadcode loads through) honours it, and GOFLAGS is the only channel to a
  // subprocess this script does not otherwise control. An existing GOFLAGS is
  // kept — it is the developer's, and GOFLAGS is a space-separated list.
  const goflags = [process.env.GOFLAGS, overlay && `-overlay=${overlay}`].filter(Boolean).join(' ')
  const proc = spawnSync(deadcodeCmd, [...(tags ? ['-tags', tags] : []), './...'], {
    cwd: root,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
    env: {
      ...process.env,
      ...platform,
      CGO_ENABLED: '1',
      ...(goflags ? { GOFLAGS: goflags } : {}),
    },
  })

  if (proc.signal) {
    throw new Error(
      `deadcode was killed by ${proc.signal} for ${platform.GOOS}/${platform.GOARCH} ` +
        `— the analysis did not run, so this says nothing about the tree. ` +
        `It needs ~1.4 GB beside this hook's two containers; free memory and ` +
        `re-run the commit. Do not change code or the baseline in response.`,
    )
  }

  if (proc.status !== 0) {
    const detail = (proc.stderr || proc.stdout || '').trim()
    throw new Error(
      `deadcode exited ${proc.status} for ${platform.GOOS}/${platform.GOARCH}` +
        `${detail ? `:\n${detail}` : ''}`,
    )
  }
  return proc.stdout
}

/**
 * The analysis for one platform: its violations, and the cgo callbacks that
 * were supplied to it as roots.
 *
 * `opts.root` and `opts.deadcodeCmd` exist for the fixture test in
 * frontend/lint-fixtures, which analyses a fixture module with a model of
 * deadcode beside it: ci-frontend has no Go toolchain, so the real analyser
 * cannot run there, and the real one is what the ratchet itself runs in
 * ci-mac / ci-linux.
 */
export function analyzeModule(platform, tags, opts = {}) {
  const root = opts.root ?? PROJECT_ROOT
  const callbacks = findCgoExportCallbacks(root)
  const overlay = callbacks.length > 0 ? writeCgoRootsOverlay(callbacks) : null

  let stdout
  try {
    stdout = runDeadcode(platform, tags, {
      ...opts,
      root,
      overlay: overlay?.overlay,
    })
  } finally {
    if (overlay) rmSync(overlay.dir, { recursive: true, force: true })
  }

  const seen = new Map()
  for (const v of parseDeadcodeOutput(stdout)) {
    seen.set(violationKey(v), v)
  }
  const violations = [...seen.values()]
  violations.sort((a, b) => `${a.file}:${a.func}`.localeCompare(`${b.file}:${b.func}`))
  return { violations, roots: callbacks }
}

/**
 * Return the normalized violation list for ONE platform, deduplicated and
 * sorted. Deterministic for a given platform — keys sorted, and any output
 * line the parser does not understand fails loudly rather than silently
 * passing (a format change in a newer deadcode must not look like a clean
 * tree).
 */
export function collectDeadcodeViolations(platform, tags, opts = {}) {
  return analyzeModule(platform, tags, opts).violations
}

function parseDeadcodeOutput(stdout) {
  const violations = []
  for (const line of stdout.split('\n')) {
    if (line === '') continue
    // node_modules ships third-party Go (e.g. flatted/golang), which deadcode
    // picks up whenever npm has run. It is not our code and not ours to
    // ratchet — the baseline is defined over this repo's packages only, and
    // `go list ./...` on a machine without node_modules would say the same.
    // Match a leading `node_modules/` too, not only `…/node_modules/`: the
    // path deadcode prints is relative to ITS working directory, so running
    // from frontend/ yields `node_modules/flatted/…` with no leading slash
    // and a `/node_modules/` test silently stops filtering. That cost a
    // worker a red gate and a paragraph of report on 2026-08-06.
    if (line.includes('/node_modules/') || line.startsWith('node_modules/')) continue
    if (TEST_SUPPORT_PKG_RE.test(line)) continue
    const m = UNREACHABLE_RE.exec(line)
    if (!m) {
      throw new Error(`unparseable deadcode output line: ${line}`)
    }
    violations.push({ file: m[1], func: m[2] })
  }

  return violations
}

export function violationKey(v) {
  return `${v.file}:${v.func}`
}

function loadBaseline() {
  try {
    const data = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
    return new Map(data.violations.map((v) => [violationKey(v), v]))
  } catch {
    return new Map() // no baseline: every violation is new
  }
}

// ─── CLI entry point ──────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  let analysis
  let platform
  try {
    const host = hostPlatform()
    platform = resolvePlatform(process.argv.slice(2), host)
    const tags = resolveBuildTags(process.argv.slice(2), host)
    console.error(
      `DEADCODE RATCHET: analysing ${platform.GOOS}/${platform.GOARCH}` +
        `${tags ? ` -tags ${tags}` : ''}, CGO_ENABLED=1.`,
    )
    analysis = analyzeModule(platform, tags)
    // Say what was added as a root, because it is the difference between this
    // run and `deadcode ./...` run by hand — a reader comparing the two is
    // otherwise looking at six violations that are in one and not the other.
    if (analysis.roots.length > 0) {
      const files = analysis.roots.map((c) => c.rel).join(', ')
      console.error(
        `DEADCODE RATCHET: ${analysis.roots.length} cgo ` +
          `${analysis.roots.length === 1 ? 'file' : 'files'} with //export callbacks supplied ` +
          `as roots (${files}).`,
      )
    }
  } catch (err) {
    console.error(`DEADCODE RATCHET: ${err.message}`)
    process.exit(1)
  }

  const violations = analysis.violations

  const useBaseline = process.env.NOCX_BASELINE_UPDATE !== '1'
  const baselineMap = useBaseline ? loadBaseline() : new Map()

  const unbaselined = violations.filter((v) => !baselineMap.has(violationKey(v)))

  // NOT reported as shrinkage, deliberately. The baseline is the union over
  // both platforms and this run saw one of them, so every entry belonging to
  // the other platform's half of a build-tag-gated pair is "unreported here"
  // and none of it is dead code that went away. Saying "baseline shrunk by 1"
  // on a tree where nothing changed is how a reader learns to ignore the line.
  const unreported =
    baselineMap.size - violations.filter((v) => baselineMap.has(violationKey(v))).length

  for (const v of violations) {
    console.log(JSON.stringify(v))
  }

  if (unbaselined.length > 0) {
    console.error(
      `DEADCODE RATCHET: ${violations.length} unreachable functions on ${platform.GOOS}/${platform.GOARCH} (${baselineMap.size} baselined, ${unbaselined.length} NEW):`,
    )
    for (const v of unbaselined) {
      console.error(`  NEW: ${v.file}: ${v.func}`)
    }
    if (useBaseline) process.exitCode = 1
  } else {
    console.error(
      `DEADCODE RATCHET: ${violations.length} unreachable functions on ` +
        `${platform.GOOS}/${platform.GOARCH}, all baselined ` +
        `(${unreported} of ${baselineMap.size} baselined entries are not compiled here or are gone).`,
    )
  }
}
