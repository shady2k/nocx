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
 * The survey of origin/main 04b45bbf4 found ten backend facts with more than
 * one owner before this check existed — the dead `ssh.Connect` stack among
 * them — and every stage of nocx-oalo1 removes one of them. This ratchet is
 * what stops a new one appearing while they do: the list of today's
 * violations is committed and may only SHRINK.
 *
 * WHAT IT REFUSES, by rule id (the baseline and the failure message name the
 * rule, so an entry says what it is):
 *
 *   xcrypto-ssh-import   an import of golang.org/x/crypto/ssh, or its agent
 *                        or knownhosts subpackage, outside the transport. The
 *                        library IS the transport; a second package importing
 *                        it is a second place that can dial, sign or probe.
 *   net-dial             net.Dial(, net.DialTimeout(, net.Dialer{ or
 *                        .DialContext( outside the transport, where the
 *                        network is not the literal "unix" — a unix socket is
 *                        not an SSH connection (the MCP bridge's, the
 *                        coordinator's, the helper endpoint's).
 *   exit-error-read      gossh.ExitError / ssh.ExitError outside the transport:
 *                        the exit cause of a remote process, decided away from
 *                        the connection that carried it. Design §5.1.2 moves
 *                        it into the x/crypto carrier, which maps it to a
 *                        neutral cause.
 *   keepalive-request    SendRequest("keepalive…") outside the transport: a
 *                        second prober on a connection that already has one —
 *                        design §5.3 replaces the keepalive verdict with the
 *                        supervisor's.
 *
 * Scope: every tracked non-test .go file except the transport packages,
 * which are `internal/ssh/`, `internal/helper/sshsvc/` and `internal/sshtest/`
 * (the last does not exist yet; it is where the fault proxy lands, and it
 * speaks SSH because it must).
 *
 * WHAT IT CANNOT SEE, the same admission the sibling ratchets make:
 *
 *   - It is a regex over source text, not a type checker. It cannot follow a
 *     value: `d := net.Dialer{…}` then `d.Dial(…)` two lines later, a dial
 *     behind an interface, a network named by a constant, or a call whose
 *     arguments sit on the next line. `net.Dialer{` is reported where it is
 *     constructed, which is the site a person would edit.
 *   - A receiver spelled anything but `net` or `.DialContext(` is invisible,
 *     and an alias other than `gossh`/`ssh` does not match the exit-error
 *     rule. Both are why the rule set is small and named rather than clever.
 *   - It cannot tell an SSH dial from an HTTP one. That is what the allowlist
 *     below and the baseline are for: the allowlist is a site that is not an
 *     SSH connection by nature and never will be; the baseline is a site that
 *     IS a second owner today and leaves with a stage or a bead named on its
 *     entry's reason. Neither list tells you which, so both are kept short.
 *   - The renderer is not read at all. nocx-oalo1.3.5 extends the record this
 *     check enforces with its half: connection state, open and reconnect only
 *     through frontend/src/connection/, and no match on error text.
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
 * reason. A file named here is exempt from ONE rule, and only that rule.
 */
export const ALLOWLIST = [
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/transport/ws.go',
    reason:
      'reads a vault key as a KEY FORMAT for the secrets panel (ParseRawPrivateKey, ' +
      'NewSignerFromKey, FingerprintSHA256): it opens no connection',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/transport/ws_secrets.go',
    reason:
      'checks a stored key with a passphrase (ParsePrivateKeyWithPassphrase): a key ' +
      'format, not a transport',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/app/helper_reverse.go',
    reason:
      'parses the helper host key and prints its fingerprint for consent (ADR-0034), ' +
      'and dials nothing',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'cmd/e2e-sshd/main.go',
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
    reason: 'the MCP bridge over a unix socket: its one dial passes the literal "unix"',
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
}

/** An import line, with an optional alias: `gossh "golang.org/x/crypto/ssh"`. */
const XCRYPTO_IMPORT_RE =
  /^\s*(?:import\s+)?(?:[A-Za-z_.]\w*\s+)?"golang\.org\/x\/crypto\/ssh(?:\/[a-z0-9_]+)*"\s*(?:\/\/.*)?$/

const NET_DIAL_RE = /\bnet\.Dial\(|\bnet\.DialTimeout\(|\bnet\.Dialer\s*\{|\.DialContext\(/
const UNIX_NETWORK_RE = /"unix"/
const EXIT_ERROR_RE = /\b(?:gossh|ssh)\.ExitError\b/
const KEEPALIVE_RE = /\.SendRequest\(\s*"keepalive/

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
    .filter((f) => !f.startsWith('.claude/')) // nested worktree, as pre-commit notes
}

/** Whether `file` is one of the transport packages. */
export function isTransportFile(file) {
  return ALLOWED_DIRS.some((dir) => file.startsWith(dir))
}

function allowlisted(rule, file) {
  return ALLOWLIST.some((entry) => entry.rule === rule && entry.file === file)
}

/**
 * Violations in Go source text already read into memory, decoupled from the
 * filesystem and from git so a test can hand it a fixture directly. `file` is
 * only ever the label on the returned records, and decides the scope.
 */
export function violationsInText(file, text) {
  if (isTransportFile(file)) return []
  const violations = []
  text.split('\n').forEach((line, idx) => {
    const trimmed = line.trim()
    if (trimmed === '' || trimmed.startsWith('//')) return
    const found = []
    if (XCRYPTO_IMPORT_RE.test(trimmed)) found.push('xcrypto-ssh-import')
    if (NET_DIAL_RE.test(trimmed) && !UNIX_NETWORK_RE.test(trimmed)) found.push('net-dial')
    if (EXIT_ERROR_RE.test(trimmed)) found.push('exit-error-read')
    if (KEEPALIVE_RE.test(trimmed)) found.push('keepalive-request')
    for (const rule of found) {
      if (allowlisted(rule, file)) continue
      violations.push({ file, rule, line: idx + 1, text: trimmed })
    }
  })
  return violations
}

/** Every violation across the scanned tree. */
export function collectViolations() {
  const violations = []
  for (const file of trackedGoFiles()) {
    violations.push(...violationsInText(file, readFileSync(resolve(PROJECT_ROOT, file), 'utf8')))
  }
  return violations
}

/** Stable key: rule + file + the trimmed line. A line number would churn on
 * every unrelated edit above it — the tradeoff check-log-context.mjs makes. */
export function violationKey(v) {
  return `${v.rule}:${v.file}:${v.text}`
}

/** Load the committed baseline: {violations: [{rule, file, text, reason}]}. */
export function loadBaseline(path = BASELINE_PATH) {
  const data = JSON.parse(readFileSync(path, 'utf8'))
  return data.violations
}

/** Split violations into baselined matches and new ones. */
export function partition(violations, baseline) {
  const known = new Set(baseline.map((entry) => violationKey(entry)))
  const fresh = []
  for (const v of violations) {
    if (!known.has(violationKey(v))) fresh.push(v)
  }
  return fresh
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
      console.error(`baseline: ${entry.rule}: ${entry.file}: ${entry.text} — no reason written`)
    }
    console.error(
      '.githooks/connection-owners-baseline.json is a list of sites that decide a connection ' +
        'fact today; each entry says why it may still, and which stage removes it.',
    )
    process.exit(1)
  }
  const fresh = partition(collectViolations(), baseline)
  if (fresh.length > 0) {
    console.error(
      `${fresh.length} new site(s) decide a connection fact outside the SSH transport ` +
        `(${ALLOWED_DIRS.join(', ')}). A fact about a connection has one owner, the backend's ` +
        'connection manager behind the Carrier interface (ADR-ba6xet1b, design §5.1). Take it ' +
        'through the transport, or add it deliberately: ' +
        'node .githooks/update-connection-owners-baseline.mjs --allow-growth',
    )
    for (const v of fresh) {
      console.error(`  NEW ${v.rule}: ${v.file}:${v.line}  ${v.text}`)
    }
    process.exit(1)
  }
  console.log(
    `OK — connection-owner ratchet: ${baseline.length} baselined site(s) outside the transport, 0 new`,
  )
}
