import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import {
  ALLOWED_DIRS,
  ALLOWLIST,
  ANALYSIS_TOOLCHAIN,
  RULES,
  aggregate,
  analysisEnv,
  allowlistProblems,
  analyze,
  evaluate,
  loadBaseline,
  toolchainForGoMod,
  unreasonedEntries,
  violationKey,
  violationsFromFacts,
} from './check-connection-owners.mjs'
import { planBaselineUpdate } from './update-connection-owners-baseline.mjs'

/**
 * The connection-owner ratchet (nocx-oalo1.3.4) refuses a NEW Go site outside
 * the SSH transport that decides a fact about an SSH connection.
 *
 * These fixtures are the SEVEN BYPASSES a review found in the previous,
 * textual version of this check — an aliased import, an aliased exit cause, a
 * keepalive spelled differently, `// "unix"` as a comment, a re-exporting
 * helper, an allowlisted file that also dials, and a text-keyed baseline —
 * each failing the check as it now stands, with its paired case passing.
 *
 * Each fixture is a real package inside this module (under a dot-directory,
 * so `./...` never sees it) because identity is the point: the analyser has
 * to RESOLVE `net` and `golang.org/x/crypto/ssh`, and that means the real
 * module's dependency graph.
 */

const PROJECT_ROOT = fileURLToPath(new URL('..', import.meta.url))
let fixtureSeq = 0

/**
 * analyseOne writes one fixture package and answers the violations the polic
 * finds in it. `files` maps a path INSIDE the fixture to its source, so a
 * fixture can occupy `internal/transport/ws.go` and be labelled exactly as
 * the product's own file is.
 */
function analyseOne(files, opts = {}) {
  const name = `f${process.pid}-${++fixtureSeq}`
  const dir = join(PROJECT_ROOT, '.githooks', '.fixtures', name)
  try {
    for (const [path, src] of Object.entries(files)) {
      const full = join(dir, path)
      mkdirSync(join(full, '..'), { recursive: true })
      writeFileSync(full, src)
    }
    // The package is the directory holding the fixture's files; the scope is
    // the fixture root, so the labels read as product paths.
    const pkgDir = Object.keys(files)[0].split('/').slice(0, -1).join('/')
    const pkgs = './.githooks/.fixtures/' + name + '/' + pkgDir
    const facts = analyze({ module: PROJECT_ROOT, scope: dir, pkgs, tags: opts.tags })
    return aggregate(violationsFromFacts(facts))
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

const rulesOf = (rows) => rows.map((r) => r.rule)

// ─── bypass 1: an aliased net import ─────────────────────────────────────

test('an aliased net import is the same dial (bypass 1)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import n "net"

func d(addr string) { _, _ = n.Dial("tcp", addr) }
`,
  })
  assert.deepEqual(rulesOf(rows), ['net-dial'])
  assert.equal(rows[0].object, 'net.Dial')
})

test('an aliased net import is still exempt for the constant unix', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import n "net"

func d(path string) { _, _ = n.Dial("unix", path) }
`,
  })
  assert.deepEqual(rows, [])
})

// ─── the review's two: a parenthesized callee and a dot import ───────────
//
// Both are the SAME identifier as `net.Dial`; only the expression around it
// differs. The analysis reports the identifier, so neither can hide.

test('a parenthesized callee is the same dial (review: paren)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "net"

func d(addr string) { _, _ = (net.Dial)("tcp", addr) }
`,
  })
  assert.deepEqual(rulesOf(rows), ['net-dial'])
  assert.equal(rows[0].object, 'net.Dial')
})

test('a dot-imported dial is the same dial (review: dot import)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import . "net"

func d(addr string) { _, _ = Dial("tcp", addr) }
`,
  })
  assert.deepEqual(rulesOf(rows), ['net-dial'])
  assert.equal(rows[0].object, 'net.Dial')
})

test('a constant-unix dial through a paren is exempt', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "net"

func d(path string) { _, _ = (net.Dial)("unix", path) }
`,
  })
  assert.deepEqual(rows, [])
})

test('a dot-imported x/crypto object is the same import (review: dot import)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import . "golang.org/x/crypto/ssh"

func f(b []byte) { _, _ = ParsePublicKey(b) }
`,
  })
  assert.deepEqual(rulesOf(rows), ['xcrypto-ssh-import'])
  assert.equal(rows[0].object, 'golang.org/x/crypto/ssh.ParsePublicKey')
})

// ─── the whole node, not half of it ──────────────────────────────────────
//
// A function literal is a TYPE and a body. The walk used to visit only the
// body, so `var open = func(c *ssh.Client) { … }` named the transport in its
// signature and produced nothing.

test("a function literal's signature is part of the walk (review: func literal)", () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "golang.org/x/crypto/ssh"

var open = func(c *ssh.Client) {
	_, _ = c.Dial("tcp", "host:22")
}
`,
  })
  assert.deepEqual(rulesOf(rows), ['xcrypto-ssh-import'])
  assert.equal(rows[0].object, 'golang.org/x/crypto/ssh.Client')
  assert.equal(rows[0].func, '<package>', 'a package-level literal is decided at the package')
})

test('a func literal in an allowlisted file is judged by its objects', () => {
  const rows = analyseOne({
    'internal/transport/ws.go': `package transport

import "golang.org/x/crypto/ssh"

var parse = func(b []byte) *ssh.PassphraseMissingError {
	_, _ = ssh.ParseRawPrivateKey(b)
	return nil
}
`,
  })
  assert.deepEqual(rows, [])
})

test("a generic type's constraint is part of the walk (review: other partial nodes)", () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "golang.org/x/crypto/ssh"

type K[T ssh.PublicKey] struct{}

var _ K[ssh.PublicKey]
`,
  })
  assert.deepEqual(rulesOf(rows), ['xcrypto-ssh-import'])
  assert.equal(rows[0].count, 2, 'the constraint and the instantiation are two references')
})

// ─── bypass 2: an aliased exit cause ─────────────────────────────────────

test('an exit cause read through any alias is refused (bypass 2)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import x "golang.org/x/crypto/ssh"

var se *x.ExitError
`,
  })
  assert.deepEqual(rulesOf(rows), ['exit-error-read'])
  assert.equal(rows[0].object, 'golang.org/x/crypto/ssh.ExitError')
})

test('a key-format object through an alias, in an allowlisted file, passes', () => {
  const rows = analyseOne({
    'internal/transport/ws.go': `package transport

import x "golang.org/x/crypto/ssh"

func parse(b []byte) { _, _ = x.ParseRawPrivateKey(b) }
`,
  })
  assert.deepEqual(rows, [])
})

// ─── bypass 3: a keepalive under another name ────────────────────────────

test('SendRequest on a client is a second prober whatever it is named (bypass 3)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "golang.org/x/crypto/ssh"

func f(c *ssh.Client) { _, _, _ = c.SendRequest("whatever", true, nil) }
`,
  })
  // The type reference in the signature is an x/crypto fact too, so the
  // signature names the library as well; what matters is the CALL.
  assert.deepEqual(rows.map((r) => r.rule).sort(), ['keepalive-request', 'xcrypto-ssh-import'])
  const keepalive = rows.find((r) => r.rule === 'keepalive-request')
  assert.equal(keepalive.object, '(*golang.org/x/crypto/ssh.Client).SendRequest')
  assert.equal(keepalive.detail, 'whatever')
})

test('a SendRequest on a type of our own is not a prober', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

type fake struct{}

func (fake) SendRequest(string, bool, []byte) (bool, []byte, error) { return false, nil, nil }

func f(x fake) { _, _, _ = x.SendRequest("keepalive@openssh.com", true, nil) }
`,
  })
  assert.deepEqual(rows, [])
})

// ─── bypass 4: unix as a comment ─────────────────────────────────────────

test('a comment saying unix is not the constant (bypass 4)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "net"

func d(addr string) { _, _ = net.Dial("tcp", addr) } // "unix" is not this argument
`,
  })
  assert.deepEqual(rulesOf(rows), ['net-dial'])
})

test('the constant unix, even through a const, is exempt', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "net"

const network = "unix"

func d(path string) { _, _ = net.Dial(network, path) }
`,
  })
  assert.deepEqual(rows, [])
})

// ─── bypass 5: a transport helper that hands the dial out ────────────────

test('an exported var re-exporting net.Dial is refused (bypass 5)', () => {
  const rows = analyseOne({
    'internal/sshtest/raw.go': `package sshtest

import "net"

var RawDial = net.Dial
`,
  })
  assert.deepEqual(rulesOf(rows), ['transport-re-export'])
  assert.equal(rows[0].object, 'net.Dial')
})

test('an exported func whose body is a dial is refused too', () => {
  const rows = analyseOne({
    'internal/sshtest/raw.go': `package sshtest

import "golang.org/x/crypto/ssh"

func DialIt(addr string) (*ssh.Client, error) {
	return ssh.Dial("tcp", addr, nil)
}
`,
  })
  assert.deepEqual(rulesOf(rows), ['transport-re-export'])
  assert.equal(rows[0].object, 'golang.org/x/crypto/ssh.Dial')
})

test('an unexported helper inside the transport is the transport itself', () => {
  const rows = analyseOne({
    'internal/sshtest/raw.go': `package sshtest

import "net"

var rawDial = net.Dial
`,
  })
  assert.deepEqual(rows, [])
})

// ─── bypass 6: the allowlisted file that also dials ──────────────────────

test('a dial added to an allowlisted file is still refused (bypass 6)', () => {
  const rows = analyseOne({
    'internal/transport/ws.go': `package transport

import "golang.org/x/crypto/ssh"

func f(addr string) { _, _ = ssh.Dial("tcp", addr, nil) }
`,
  })
  assert.deepEqual(rulesOf(rows), ['xcrypto-ssh-import'])
  assert.equal(rows[0].object, 'golang.org/x/crypto/ssh.Dial')
})

test('the same file keeping to its key format passes', () => {
  const rows = analyseOne({
    'internal/transport/ws.go': `package transport

import "golang.org/x/crypto/ssh"

func f(b []byte) { _, _ = ssh.ParseRawPrivateKey(b) }
`,
  })
  assert.deepEqual(rows, [])
})

// ─── bypass 7: the text-keyed baseline ───────────────────────────────────

test('a second identical site in one function is a second occurrence (bypass 7)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "net"

func d(a, b string) {
	_, _ = net.Dial("tcp", a)
	_, _ = net.Dial("tcp", b)
}
`,
  })
  assert.equal(rows.length, 1, 'one function, one object, one key')
  assert.equal(rows[0].count, 2, 'and two occurrences behind it')

  const baseline = [
    {
      rule: 'net-dial',
      file: 'internal/app/second.go',
      func: rows[0].func,
      object: 'net.Dial',
      count: 1,
      reason: 'x',
    },
  ]
  const { fresh, stale } = evaluate(rows, baseline)
  assert.equal(fresh.length, 1, 'a count that grew is new')
  assert.equal(fresh[0].was, 1)
  assert.equal(stale.length, 0)
})

test('a baselined site that vanished, or shrank, is stale and fails', () => {
  const key = {
    rule: 'net-dial',
    file: 'internal/app/gone.go',
    func: 'app.d',
    object: 'net.Dial',
    count: 1,
    reason: 'x',
  }
  assert.equal(evaluate([], [key]).stale.length, 1, 'gone is stale')

  const rows = [
    { rule: 'net-dial', file: 'internal/app/gone.go', func: 'app.d', object: 'net.Dial', count: 1 },
  ]
  const bigger = { ...key, count: 2 }
  const shrunk = evaluate(rows, [bigger])
  assert.equal(shrunk.stale.length, 1, 'a shrink is stale too')
  assert.equal(shrunk.stale[0].now, 1)
})

test('no growth without the flag; with it, an entry starts unreasoned', () => {
  const row = {
    rule: 'net-dial',
    file: 'internal/app/x.go',
    func: 'app.d',
    object: 'net.Dial',
    count: 1,
  }
  assert.throws(
    () => planBaselineUpdate({ violations: [row], old: [] }),
    /REFUSING to update baseline/,
  )
  const plan = planBaselineUpdate({ violations: [row], old: [], allowGrowth: true })
  assert.equal(plan.entries.length, 1)
  assert.equal(plan.entries[0].reason, '')
  assert.equal(plan.entries[0].count, 1)
  // A baseline entry with no reason is refused, not silently honoured.
  assert.equal(unreasonedEntries(plan.entries).length, 1)
})

test('an existing reason survives a rebuild, and a count change does not lose it', () => {
  const old = [
    {
      rule: 'exit-error-read',
      file: 'internal/app/x.go',
      func: 'app.d',
      object: 'golang.org/x/crypto/ssh.ExitError',
      count: 1,
      reason: 'stage 3 maps it',
    },
  ]
  const grown = [
    {
      rule: 'exit-error-read',
      file: 'internal/app/x.go',
      func: 'app.d',
      object: 'golang.org/x/crypto/ssh.ExitError',
      count: 2,
    },
  ]
  const plan = planBaselineUpdate({ violations: grown, old, allowGrowth: true })
  assert.equal(plan.entries[0].reason, 'stage 3 maps it')
  assert.equal(plan.entries[0].count, 2)

  const shrunk = planBaselineUpdate({
    violations: [{ ...grown[0], count: 1 }],
    old: [{ ...old[0], count: 2 }],
  })
  assert.equal(shrunk.entries[0].count, 1)
  assert.equal(shrunk.entries[0].reason, 'stage 3 maps it')
  assert.equal(shrunk.shrunk.length, 1)
})

// ─── the lists themselves ────────────────────────────────────────────────

test('the allowlist is reasoned, known, and exact about x/crypto objects', () => {
  assert.deepEqual(allowlistProblems(), [])
  for (const entry of ALLOWLIST) {
    assert.ok(Object.hasOwn(RULES, entry.rule), entry.file)
  }
  // The three key-format files are allowed OBJECTS, never the dialect.
  for (const entry of ALLOWLIST.filter((e) => e.rule === 'xcrypto-ssh-import')) {
    assert.ok(Array.isArray(entry.objects) && entry.objects.length > 0, entry.file)
    assert.ok(!entry.objects.includes('golang.org/x/crypto/ssh.Dial'), entry.file)
  }
})

test('the committed baseline is counted, reasoned, and only shrinks', () => {
  const baseline = loadBaseline()
  assert.ok(baseline.length > 0)
  for (const entry of baseline) {
    assert.equal(typeof entry.count, 'number', violationKey(entry))
    assert.ok(entry.count > 0, violationKey(entry))
    assert.ok(entry.func.length > 0, violationKey(entry))
    assert.ok(entry.object.length > 0, violationKey(entry))
    assert.ok(entry.reason.trim().length > 0, violationKey(entry))
  }
})

test('the transport packages are the only ones exempt, and the carrier and the supervisor are two of them', () => {
  // Five since nocx-oalo1.4.2: the carrier that implements the seam and the
  // supervisor that owns every connection's state ARE the one owner
  // ADR-ba6xet1b names, so refusing them for deciding a connection fact would
  // put the owner outside its own allowlist (design §5.1.1).
  assert.deepEqual(ALLOWED_DIRS, [
    'internal/ssh/',
    'internal/helper/sshsvc/',
    'internal/helper/sshconn/',
    'internal/hostconn/',
    'internal/sshtest/',
  ])
})

// ─── the toolchain the analysis runs under ───────────────────────────────

test('the analysis runs under the toolchain go.mod declares, not the host go', () => {
  const goMod = readFileSync(join(PROJECT_ROOT, 'go.mod'), 'utf8')
  const declared = /^toolchain (\S+)\s*$/m.exec(goMod)
  assert.ok(
    declared,
    'go.mod names no `toolchain` line: the analysis would run under whatever go the host ' +
      'happens to have, and a host newer than the module cannot read its own export data',
  )
  // THE TEST THIS GUARD EXISTS FOR: a hardcoded or host-derived toolchain that
  // drifts from go.mod fails here. Bumping go.mod's toolchain line is the one
  // edit that moves the analysis with it.
  assert.equal(ANALYSIS_TOOLCHAIN, declared[1])
  assert.equal(analysisEnv().GOTOOLCHAIN, declared[1])
  assert.equal(analysisEnv({ GOTOOLCHAIN: 'local' }).GOTOOLCHAIN, declared[1])
})

test('a go.mod with no toolchain line, or one on another language line, is refused', () => {
  assert.throws(
    () => toolchainForGoMod('module x\n\ngo 1.26\n'),
    /no `toolchain` line/,
    'the go directive names a language version, not a toolchain to run',
  )
  assert.throws(
    () => toolchainForGoMod('module x\n\ngo 1.26\n\ntoolchain go1.27.3\n'),
    /Go 1\.27 but its go directive says 1\.26/,
    'a toolchain on another language line is not derived from the go directive',
  )
  assert.equal(toolchainForGoMod('module x\n\ngo 1.26\n\ntoolchain go1.26.5\n'), 'go1.26.5')
})

test('the unchanged tree passes the check', () => {
  const out = execFileSync('node', ['.githooks/check-connection-owners.mjs'], {
    cwd: PROJECT_ROOT,
    encoding: 'utf8',
  })
  assert.match(out, /connection-owner ratchet/)
  assert.match(out, /0 new, 0 stale/)
})
