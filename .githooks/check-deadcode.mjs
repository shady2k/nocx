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
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
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
function runDeadcode(platform, tags) {
  const proc = spawnSync(DEADCODE_CMD, [...(tags ? ['-tags', tags] : []), './...'], {
    cwd: PROJECT_ROOT,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
    env: { ...process.env, ...platform, CGO_ENABLED: '1' },
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
 * Return the normalized violation list for ONE platform, deduplicated and
 * sorted. Deterministic for a given platform — keys sorted, and any output
 * line the parser does not understand fails loudly rather than silently
 * passing (a format change in a newer deadcode must not look like a clean
 * tree).
 */
export function collectDeadcodeViolations(platform, tags) {
  const seen = new Map()
  for (const v of parseDeadcodeOutput(runDeadcode(platform, tags))) {
    seen.set(violationKey(v), v)
  }
  const violations = [...seen.values()]
  violations.sort((a, b) => `${a.file}:${a.func}`.localeCompare(`${b.file}:${b.func}`))
  return violations
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
  let violations
  let platform
  try {
    const host = hostPlatform()
    platform = resolvePlatform(process.argv.slice(2), host)
    const tags = resolveBuildTags(process.argv.slice(2), host)
    console.error(
      `DEADCODE RATCHET: analysing ${platform.GOOS}/${platform.GOARCH}` +
        `${tags ? ` -tags ${tags}` : ''}, CGO_ENABLED=1.`,
    )
    violations = collectDeadcodeViolations(platform, tags)
  } catch (err) {
    console.error(`DEADCODE RATCHET: ${err.message}`)
    process.exit(1)
  }

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
