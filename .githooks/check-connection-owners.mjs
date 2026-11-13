#!/usr/bin/env node
/**
 * Connection-owner ratchet — a NEW Go site outside the SSH transport that
 * decides a fact about an SSH connection is refused (nocx-oalo1.3.4).
 *
 * ADR-ba6xet1b says a fact about an SSH connection — its dial, its liveness,
 * its closing, its exit cause, its identity, which panes share it — has ONE
 * owner: the backend's connection manager behind the Carrier interface
 * (.internal/specs/2026-11-06-ssh-foundation-design.md §5.1, ADR-akhhb098).
 * The owner's decision of 2026-11-13 makes that a check rather than a review:
 * "there must be an architectural ban on second surfaces".
 *
 * # Identity, not spelling
 *
 * This check does not read lines. It asks `cmd/conncheck`, a small Go program
 * that loads the tree with golang.org/x/tools/go/packages and reports every
 * REFERENCE to a connection object, resolved by package path and name through
 * go/types. A reviewer's round found seven ways the previous line-regex
 * passed a second owner — an aliased `net` import, an aliased x/crypto import,
 * a SendRequest with any name, a `// "unix"` comment on the dial's line, a
 * helper package re-exporting `net.Dial`, an allowlisted file that also
 * dialled, and a baseline keyed by text — and each is now a fixture in
 * check-connection-owners.test.mjs that FAILS.
 *
 * WHAT IT REFUSES, by rule id (the baseline and the failure message name the
 * rule, so an entry says what it is):
 *
 *   xcrypto-ssh-import   a reference to an object of golang.org/x/crypto/ssh
 *                        or its agent/knownhosts subpackages outside the
 *                        transport, EXCEPT the key-format objects listed for
 *                        that file in ALLOWLIST below. The library IS the
 *                        transport; a second package reaching into it is a
 *                        second place that can dial, sign or probe. The
 *                        allowlist names OBJECTS, not files: a dial added to
 *                        an allowlisted file is still reported.
 *   net-dial             a reference to net.Dial, DialTimeout, DialUDP,
 *                        DialTCP, DialIP, DialUnix, the net.Dialer type, or a
 *                        dial method of it, where the network is not the
 *                        CONSTANT "unix" — the constant is evaluated by
 *                        go/types, so a comment saying unix exempts nothing.
 *                        A unix socket is not an SSH connection (the MCP
 *                        bridge's, the coordinator's, the helper endpoint's).
 *   exit-error-read      a reference to the ssh.ExitError TYPE outside the
 *                        transport, whatever the import is called: the exit
 *                        cause of a remote process, decided away from the
 *                        connection that carried it. Design §5.1.2 moves it
 *                        into the x/crypto carrier, which maps it to a neutral
 *                        cause.
 *   keepalive-request    a call of SendRequest on an *ssh.Client or an
 *                        ssh.Conn outside the transport, WHATEVER the request
 *                        name is: a second prober on a connection that already
 *                        has one — design §5.3 replaces the keepalive verdict
 *                        with the supervisor's.
 *   transport-re-export  inside a transport package, an exported package-level
 *                        var holding a net dial function, or an exported func
 *                        returning ssh.Dial/NewClientConn: a helper that hands
 *                        a dial out makes every caller a second owner without
 *                        naming one.
 *
 * Scope: every tracked non-test .go file except the transport packages, which
 * are `internal/ssh/`, `internal/helper/sshsvc/` and `internal/sshtest/` (the
 * last does not exist yet; it is where the fault proxy lands, and it speaks
 * SSH because it must). The tree is analysed TWICE — as the coordinator is
 * built, and with -tags nocx_local_ssh — because the helper's dial half exists
 * only in the second, and a file behind a build tag is still a second owner.
 *
 * WHAT IT CANNOT SEE, the same admission the sibling ratchets make:
 *
 *   - A dial behind an interface whose implementation lives elsewhere: the
 *     call is a method on an interface, and no static analyser can say what it
 *     becomes. The construction of the concrete dialer is reported instead.
 *   - A value carried into another package through a struct field, a channel
 *     or a callback. (An exported package-level var or func is covered by
 *     transport-re-export; a field is not.)
 *   - A network named by a variable that is not a constant. A dial with a
 *     computed network string is reported, not exempted: the exemption is the
 *     constant "unix" and nothing else.
 *   - A tree that does not compile. A load error fails the check: a tree that
 *     cannot be analysed cannot be proven clean.
 *   - The renderer, which nocx-oalo1.3.5 owns and checks itself.
 *   - Comments and prose: nothing here reads a line of text, which is why
 *     `// net.Dialer never performs the lookup` is no longer a false finding.
 *
 * COST. The analysis is a type-checked load, so its first run after a cold
 * checkout pays for the module's export data (~50 s on the 4-core CI-class
 * VM this was developed on, with cgo) and every later run is ~3 s. The
 * pre-commit hook runs it after golangci-lint, whose build has already warmed
 * the same cache.
 *
 * Invocation: node .githooks/check-connection-owners.mjs   (from the repo root)
 * Regenerate: node .githooks/update-connection-owners-baseline.mjs
 */

import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
export const PROJECT_ROOT = resolve(__dirname, '..')
export const BASELINE_PATH = resolve(__dirname, 'connection-owners-baseline.json')

/** The SSH transport: the only packages allowed to speak x/crypto at all. */
export const ALLOWED_DIRS = ['internal/ssh/', 'internal/helper/sshsvc/', 'internal/sshtest/']

/**
 * Sites that are not an SSH connection and never become one, each with its
 * reason. `objects` names the x/crypto objects ONE file may use — the check
 * reads what those files actually reference, so an entry is exact and a new
 * object in the same file is a new finding.
 */
export const ALLOWLIST = [
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/transport/ws.go',
    objects: [
      'golang.org/x/crypto/ssh.ParseRawPrivateKey',
      'golang.org/x/crypto/ssh.NewSignerFromKey',
      'golang.org/x/crypto/ssh.FingerprintSHA256',
      'golang.org/x/crypto/ssh.PassphraseMissingError',
    ],
    reason:
      'reads a vault key as a KEY FORMAT for the secrets panel (ParseRawPrivateKey, ' +
      'NewSignerFromKey, FingerprintSHA256): it opens no connection',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/transport/ws_secrets.go',
    objects: ['golang.org/x/crypto/ssh.ParsePrivateKeyWithPassphrase'],
    reason:
      'checks a stored key with a passphrase (ParsePrivateKeyWithPassphrase): a key ' +
      'format, not a transport',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/app/helper_reverse.go',
    objects: [
      'golang.org/x/crypto/ssh.ParsePublicKey',
      'golang.org/x/crypto/ssh.PublicKey',
      'golang.org/x/crypto/ssh.FingerprintSHA256',
    ],
    reason:
      'parses the helper host key and prints its fingerprint for consent (ADR-0034), ' +
      'and dials nothing',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'cmd/e2e-sshd/main.go',
    objects: [
      'golang.org/x/crypto/ssh.NewSignerFromKey',
      'golang.org/x/crypto/ssh.Signer',
      'golang.org/x/crypto/ssh.MarshalPrivateKey',
      'golang.org/x/crypto/ssh/knownhosts.Line',
      'golang.org/x/crypto/ssh.ServerConfig',
      'golang.org/x/crypto/ssh.NewServerConn',
      'golang.org/x/crypto/ssh.UnknownChannelType',
      'golang.org/x/crypto/ssh.ConnectionFailed',
      'golang.org/x/crypto/ssh.DiscardRequests',
      'golang.org/x/crypto/ssh.Conn',
      'golang.org/x/crypto/ssh.Channel',
      'golang.org/x/crypto/ssh.Request',
      'golang.org/x/crypto/ssh.NewChannel',
      'golang.org/x/crypto/ssh.Marshal',
      'golang.org/x/crypto/ssh.Unmarshal',
    ],
    reason: 'the e2e stand SSH SERVER: the peer a test connects to, never a connection nocx holds',
  },
  {
    rule: 'net-dial',
    file: 'cmd/e2e-sshd/main.go',
    reason: 'the e2e stand SSH server dials a direct-tcpip destination for the test, as sshd does',
  },
  {
    rule: 'net-dial',
    file: 'internal/mcpstdio/mcpstdio.go',
    reason: 'the MCP bridge over a unix socket: its one dial passes the constant "unix"',
  },
  {
    rule: 'net-dial',
    file: 'internal/tunnel/remote.go',
    reason:
      'a remote forward relays an arriving connection to its destination ON THIS MACHINE ' +
      '(OpenSSH -R semantics), not to the SSH host',
  },
  {
    rule: 'net-dial',
    file: 'internal/update/download.go',
    reason: 'the updater downloads an artefact over HTTPS; no SSH connection is involved',
  },
]

/** What each rule is for, printed beside a failure so the refusal explains itself. */
export const RULES = {
  'xcrypto-ssh-import': 'x/crypto/ssh is the transport, and belongs to the transport packages',
  'net-dial': 'dials a network outside the transport',
  'exit-error-read':
    "reads x/crypto's ExitError, so decides a remote exit away from its connection",
  'keepalive-request':
    'sends a keepalive outside the transport, where the connection has one prober',
  'transport-re-export':
    'hands a dial function out of a transport package, where every caller becomes an owner',
}

/** Whether `file` is one of the transport packages. */
export function isTransportFile(file) {
  return ALLOWED_DIRS.some((dir) => file.startsWith(dir))
}

function allowlisted(rule, file, object) {
  return ALLOWLIST.some((entry) => {
    if (entry.rule !== rule || entry.file !== file) return false
    if (!entry.objects) return true // a site-level exemption (the net-dial entries)
    return entry.objects.includes(object)
  })
}

// ─── the toolchain the analysis runs under ───────────────────────────────
//
// The analysis is a TYPE-CHECKED load, and the reader inside
// golang.org/x/tools understands export data only up to the Go release it was
// written for. On a host whose `go` is newer than that release (1.27 today) the
// load fails with "export data version N greater than maximum supported
// version M" — so a check that inherited the developer's `go` would be green or
// red depending on the machine, which is not a check at all.
//
// The toolchain is therefore PINNED BY THE MODULE and the host is irrelevant:
// `go.mod` names it in `toolchain`, one line, and a bump of the language
// version moves it with the same edit. The `go` directive alone is not enough
// to run under: it names a language version (`1.26`) and GOTOOLCHAIN refuses a
// version without a patch ("go1.26 is a language version but not a toolchain
// version"). A go.mod that names no toolchain, or one on another language line,
// is refused rather than guessed at.

/**
 * toolchainForGoMod answers the `toolchain` value a go.mod declares, refusing
 * one that is missing or that sits on a different language line than the
 * `go` directive.
 */
export function toolchainForGoMod(text) {
  const goLine = /^go\s+(\d+\.\d+(?:\.\d+)?)\s*$/m.exec(text)
  if (!goLine) throw new Error('go.mod names no `go` directive')
  const tcLine = /^toolchain\s+(\S+)\s*$/m.exec(text)
  if (!tcLine) {
    throw new Error(
      'go.mod names no `toolchain` line. The connection-owner analysis runs under the ' +
        "module's own toolchain, and `go 1.26` is a language version with no patch to run: " +
        'add `toolchain go1.26.x` beside it, so a host whose go is newer cannot change the answer.',
    )
  }
  const tc = tcLine[1]
  const tcMatch = /^go(\d+\.\d+)\.\d+$/.exec(tc)
  if (!tcMatch) {
    throw new Error(
      `go.mod's toolchain ${JSON.stringify(tc)} is not a go<major>.<minor>.<patch> version`,
    )
  }
  const goMinor = goLine[1].split('.').slice(0, 2).join('.')
  if (tcMatch[1] !== goMinor) {
    throw new Error(
      `go.mod's toolchain ${tc} is Go ${tcMatch[1]} but its go directive says ${goLine[1]}: ` +
        'the analysis would run under a language version the module does not declare',
    )
  }
  return tc
}

/** The toolchain this repository's analysis runs under. */
export const ANALYSIS_TOOLCHAIN = toolchainForGoMod(
  readFileSync(resolve(PROJECT_ROOT, 'go.mod'), 'utf8'),
)

/**
 * The environment every analyser invocation gets. `GOTOOLCHAIN` is the
 * module's, never the host's: that is the whole of the fix for a developer
 * whose go is newer than the one the analysis can read export data from.
 */
export function analysisEnv(env = process.env) {
  return { ...env, GOTOOLCHAIN: ANALYSIS_TOOLCHAIN }
}

// ─── the analysed facts ──────────────────────────────────────────────────

/**
 * Run cmd/conncheck for one build-tag set and answer its facts. `opts.module`
 * and `opts.scope` exist for the tests: a fixture is a package inside the
 * module with its own root, so its file labels come out as the real product
 * paths and the policy applies unchanged.
 */
export function analyze(opts = {}) {
  const module = opts.module ?? PROJECT_ROOT
  const scope = opts.scope ?? module
  const args = ['run', './cmd/conncheck', '-module', module, '-scope', scope]
  if (opts.tags) args.push('-tags', opts.tags)
  if (opts.pkgs) args.push('-pkgs', opts.pkgs)
  let out
  try {
    out = execFileSync('go', args, {
      cwd: module,
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
      stdio: ['ignore', 'pipe', 'pipe'],
      env: analysisEnv(),
    })
  } catch (err) {
    const detail = (err.stderr ?? '').toString().trim() || err.message
    throw new Error(
      'the connection-owner analyser could not run (cmd/conncheck). It needs the Go ' +
        `toolchain the module is built with.\n${detail}`,
    )
  }
  return JSON.parse(out).facts ?? []
}

/** Facts from both build configurations, deduplicated by position and object. */
export function collectFacts() {
  const seen = new Map()
  for (const tags of ['', 'nocx_local_ssh']) {
    for (const f of analyze({ tags })) {
      const key = `${f.rule}:${f.file}:${f.line}:${f.func}:${f.object}:${f.detail ?? ''}`
      if (!seen.has(key)) seen.set(key, f)
    }
  }
  return [...seen.values()]
}

/**
 * The policy: drop the transport's own use of the library (except a
 * re-export), and drop the exact allowlisted object sites. Everything else is
 * a violation.
 */
export function violationsFromFacts(facts) {
  return (facts ?? []).filter((f) => {
    if (isTransportFile(f.file)) return f.rule === 'transport-re-export'
    return !allowlisted(f.rule, f.file, f.object)
  })
}

/** Stable key: rule + file + enclosing function + the object referenced. */
export function violationKey(v) {
  return `${v.rule}:${v.file}:${v.func}:${v.object}`
}

/**
 * Group facts into baseline rows: one row per key, with the number of
 * occurrences and the first line that carries one. A second identical site in
 * one function is a second occurrence, which the count makes visible — the
 * previous baseline was a set keyed by text and could not see it.
 */
export function aggregate(violations) {
  const rows = new Map()
  for (const v of violations) {
    const key = violationKey(v)
    const row = rows.get(key)
    if (row) {
      row.count += 1
      continue
    }
    rows.set(key, {
      rule: v.rule,
      file: v.file,
      func: v.func,
      object: v.object,
      count: 1,
      line: v.line,
      detail: v.detail ?? '',
    })
  }
  return [...rows.values()].sort((a, b) => violationKey(a).localeCompare(violationKey(b)))
}

/** Every violation across the scanned tree, aggregated and sorted. */
export function collectViolations() {
  return aggregate(violationsFromFacts(collectFacts()))
}

/** Load the committed baseline: {violations: [{rule,file,func,object,count,reason}]}. */
export function loadBaseline(path = BASELINE_PATH) {
  const data = JSON.parse(readFileSync(path, 'utf8'))
  return data.violations
}

/**
 * Split the current rows against the baseline:
 *   - `fresh`: a key the baseline does not hold, or one whose count GREW;
 *   - `stale`: a key whose count SHRANK, or one the tree no longer has.
 * Both fail the check. Growth is a new second owner; shrinkage or absence
 * means the baseline still licenses a site that is gone, and a licence nobody
 * uses is how a stale exemption hides the next real one.
 */
export function evaluate(rows, baseline) {
  const previous = new Map(baseline.map((entry) => [violationKey(entry), entry]))
  const current = new Map(rows.map((row) => [violationKey(row), row]))
  const fresh = []
  const stale = []
  for (const row of rows) {
    const before = previous.get(violationKey(row))
    if (!before) fresh.push(row)
    else if (row.count > before.count) fresh.push({ ...row, was: before.count })
  }
  for (const before of baseline) {
    const row = current.get(violationKey(before))
    if (!row) stale.push({ ...before, now: 0 })
    else if (row.count < before.count) stale.push({ ...before, now: row.count })
  }
  return { fresh, stale }
}

/** The keys a baseline no longer justifies (kept for the updater's callers). */
export function staleEntries(rows, baseline) {
  return evaluate(rows, baseline).stale
}

/** A baseline entry with no reason is an unexplained exception: it names a
 * site that decides a connection fact, and nobody wrote down why it may. */
export function unreasonedEntries(baseline) {
  return baseline.filter((entry) => typeof entry.reason !== 'string' || entry.reason.trim() === '')
}

/** An allowlist entry with no reason is a silent licence; refuse it. */
export function allowlistProblems() {
  const problems = []
  for (const entry of ALLOWLIST) {
    if (typeof entry.reason !== 'string' || entry.reason.trim() === '') {
      problems.push(`${entry.file} (${entry.rule}) is allowlisted with no reason`)
    }
    if (!Object.hasOwn(RULES, entry.rule)) {
      problems.push(`${entry.file} is allowlisted for an unknown rule ${entry.rule}`)
    }
    if (entry.rule === 'xcrypto-ssh-import' && !Array.isArray(entry.objects)) {
      problems.push(
        `${entry.file} allowlists x/crypto with no object list: the library is allowed ` +
          'object by object, so that a dial added to the same file is still reported',
      )
    }
  }
  return problems
}

// ─── CLI entry point ─────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const problems = allowlistProblems()
  if (problems.length > 0) {
    for (const p of problems) console.error(`allowlist: ${p}`)
    process.exit(1)
  }
  const baseline = loadBaseline()
  const unreasoned = unreasonedEntries(baseline)
  if (unreasoned.length > 0) {
    for (const entry of unreasoned) {
      console.error(`baseline: ${violationKey(entry)} — no reason written`)
    }
    console.error(
      '.githooks/connection-owners-baseline.json is a list of sites that decide a connection ' +
        'fact today; each entry says why it may still, and which stage removes it.',
    )
    process.exit(1)
  }
  let rows
  try {
    rows = collectViolations()
  } catch (err) {
    console.error(err.message)
    process.exit(2)
  }
  const { fresh, stale } = evaluate(rows, baseline)
  if (stale.length > 0) {
    console.error(
      `${stale.length} baselined site(s) no longer decide a connection fact, or decide it ` +
        'fewer times. A baseline entry that nothing uses is a stale exemption, and it must be ' +
        'dropped deliberately: node .githooks/update-connection-owners-baseline.mjs',
    )
    for (const entry of stale) {
      console.error(`  STALE ${violationKey(entry)}  (baseline ${entry.count}, now ${entry.now})`)
    }
  }
  if (fresh.length > 0) {
    console.error(
      `${fresh.length} new site(s) decide a connection fact outside the SSH transport ` +
        `(${ALLOWED_DIRS.join(', ')}). A fact about a connection has one owner, the backend's ` +
        'connection manager behind the Carrier interface (ADR-ba6xet1b, design §5.1). Take it ' +
        'through the transport, or add it deliberately: ' +
        'node .githooks/update-connection-owners-baseline.mjs --allow-growth',
    )
    for (const row of fresh) {
      const line = row.line === undefined ? '' : `:${row.line}`
      const grew = row.was === undefined ? '' : ` (baseline ${row.was}, now ${row.count})`
      console.error(`  NEW ${row.rule}: ${row.file}${line} ${row.func} → ${row.object}${grew}`)
    }
  }
  if (fresh.length > 0 || stale.length > 0) process.exit(1)
  console.log(
    `OK — connection-owner ratchet: ${baseline.length} baselined site(s) outside the transport, ` +
      '0 new, 0 stale',
  )
}
