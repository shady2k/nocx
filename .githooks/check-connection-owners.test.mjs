import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join, relative } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import {
  ALLOWED_DIRS,
  ALLOWLIST,
  ANALYSIS_TOOLCHAIN,
  RULE_EXEMPT_DIRS,
  RULES,
  aggregate,
  analysisEnv,
  allowlistProblems,
  analyze,
  evaluate,
  isAllowlisted,
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
 * withFixture writes one fixture tree, runs `fn` with its root, and removes it
 * again. `files` maps a path INSIDE the fixture to its source, so a fixture can
 * occupy `internal/transport/ws.go` and be labelled exactly as the product's
 * own file is.
 */
function withFixture(files, fn, base = join(PROJECT_ROOT, '.githooks', '.fixtures')) {
  const name = `f${process.pid}-${++fixtureSeq}`
  const dir = join(base, name)
  try {
    const fixtureFiles = typeof files === 'function' ? files(name) : files
    for (const [path, src] of Object.entries(fixtureFiles)) {
      const full = join(dir, path)
      mkdirSync(join(full, '..'), { recursive: true })
      writeFileSync(full, src)
    }
    return fn(dir, './' + relative(PROJECT_ROOT, dir))
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

/**
 * factsFor answers the analyser's raw facts for one fixture PACKAGE, loaded by
 * its pattern. It is the fast path for a fixture that is about the walk itself
 * rather than about which packages the pass selects.
 */
function factsFor(files, opts = {}) {
  return withFixture(files, (dir, fixture) => {
    const pkgDir = Object.keys(files)[0].split('/').slice(0, -1).join('/')
    return analyze({
      module: PROJECT_ROOT,
      scope: dir,
      pkgs: fixture + '/' + pkgDir,
      tags: opts.tags,
    })
  })
}

/**
 * factsThroughTheFullPass is factsFor through the package selection CI uses:
 * `-pkgs` is NOT given, so the analyser walks the scope with its own candidate
 * filter and loads what it chose. A fact this answers and factsFor does not is
 * a fact the filter cuts off before go/types, which is exactly how a package
 * whose only reference is a method call on a value used to escape.
 */
function factsThroughTheFullPass(files, opts = {}) {
  return withFixture(files, (dir) => analyze({ module: PROJECT_ROOT, scope: dir, tags: opts.tags }))
}

function analyseOne(files, opts = {}) {
  return aggregate(violationsFromFacts(factsFor(files, opts)))
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

// ─── C23: the far helper's reachability has one owner ────────────────────
//
// ADR-727g63tr decision 2 and design §5.10's C23: whether a far helper serves
// a connection is decided by internal/farhelper, from the host's mode and the
// machine's recorded consent. A helper-client dial or a read of either store
// anywhere else is a second decider; the analyser reports the OBJECT, so an
// alias, a helper of our own or a line comment changes nothing. Each rule has
// its positive case beside the negative one: the same reference inside
// internal/farhelper is the owner doing its job.

const HELPER_DIAL = 'github.com/shady2k/nocx/internal/helper/client.Dial'
const MODE_LOOKUP = 'github.com/shady2k/nocx/internal/hostmode.(*Store).Lookup'
const CONSENT_LOOKUP = 'github.com/shady2k/nocx/internal/helper/consent.(*Store).Lookup'
const FS_CONN = 'github.com/shady2k/nocx/internal/ssh.NewFSConn'
const SAMPLE_PORTS_OP = 'github.com/shady2k/nocx/internal/helper/proto.OpSamplePorts'
const SAMPLE_PORTS = 'github.com/shady2k/nocx/internal/helper/client.(*ProbeLease).SamplePorts'
const ACQUIRE_LEASE = 'github.com/shady2k/nocx/internal/helper/client.(*Client).AcquireProbeLease'
const GIT_FACTORY = 'github.com/shady2k/nocx/internal/git/helper.NewFactory'

/**
 * The label the analyser gives the same function in the product, for a fact
 * found in a fixture: the fixture is a package inside this module under a
 * dot-directory, so its package path carries one extra segment. Everything
 * else — receiver type and function name — is the product's, which is what
 * makes an allowlist entry testable through the analyser instead of through a
 * string a test typed.
 */
const FIXTURE_PKG = /^github\.com\/shady2k\/nocx\/\.githooks\/\.fixtures\/[^/]+\//
const asProduct = (label) => label.replace(FIXTURE_PKG, 'github.com/shady2k/nocx/')

const DIAL_FIXTURE = `package app

import "github.com/shady2k/nocx/internal/helper/client"

func dial() (*client.Client, error) { return client.Dial(nil, client.Config{}) }
`
const MODE_FIXTURE = `package app

import "github.com/shady2k/nocx/internal/hostmode"

func mode(s *hostmode.Store, fp string) (hostmode.Mode, bool) { return s.Lookup(fp) }
`
const CONSENT_FIXTURE = `package app

import "github.com/shady2k/nocx/internal/helper/consent"

func granted(s *consent.Store, fp string) (consent.Answer, bool) { return s.Lookup(fp) }
`

test('a helper-client dial outside internal/farhelper is a second decider (C23)', () => {
  const rows = analyseOne({ 'internal/app/second.go': DIAL_FIXTURE })
  assert.deepEqual(rulesOf(rows), ['helper-decider'])
  assert.equal(rows[0].object, HELPER_DIAL)
})

test('the same helper-client dial inside internal/farhelper is the owner itself (C23)', () => {
  const rows = analyseOne({ 'internal/farhelper/second.go': DIAL_FIXTURE })
  assert.deepEqual(rows, [])
})

test('a host-mode read outside internal/farhelper is a second decider (C23)', () => {
  const rows = analyseOne({ 'internal/app/second.go': MODE_FIXTURE })
  assert.deepEqual(rulesOf(rows), ['helper-decider'])
  assert.equal(rows[0].object, MODE_LOOKUP)
})

test('the same host-mode read inside internal/farhelper is the owner itself (C23)', () => {
  const rows = analyseOne({ 'internal/farhelper/second.go': MODE_FIXTURE })
  assert.deepEqual(rows, [])
})

test('a consent read outside internal/farhelper is a second decider (C23)', () => {
  const rows = analyseOne({ 'internal/app/second.go': CONSENT_FIXTURE })
  assert.deepEqual(rulesOf(rows), ['helper-decider'])
  assert.equal(rows[0].object, CONSENT_LOOKUP)
})

test('the same consent read inside internal/farhelper is the owner itself (C23)', () => {
  const rows = analyseOne({ 'internal/farhelper/second.go': CONSENT_FIXTURE })
  assert.deepEqual(rows, [])
})

// ─── C24: no panel reaches the host but through the far helper ───────────
//
// The three roads to the host a panel has today: SFTP (ssh.NewFSConn), the
// helper's ports op named by a panel, and a ProbeLease exec for a sample. The
// helper protocol's own packages are the road the far helper rides, not a
// panel, so they are exempt — and the negative fixture is what proves the
// exemption did not swallow the rule.

test('ssh.NewFSConn from a panel package is a second road to the host (C24)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import (
	"io"

	nossh "github.com/shady2k/nocx/internal/ssh"
)

func files(rw io.ReadWriteCloser) (nossh.FSConn, error) { return nossh.NewFSConn(nil, rw) }
`,
  })
  assert.deepEqual(rulesOf(rows), ['panel-host-path'])
  assert.equal(rows[0].object, FS_CONN)
})

test('the same SFTP lease inside internal/farhelper is the road the helper rides (C24)', () => {
  const rows = analyseOne({
    'internal/farhelper/second.go': `package farhelper

import (
	"io"

	nossh "github.com/shady2k/nocx/internal/ssh"
)

func files(rw io.ReadWriteCloser) (nossh.FSConn, error) { return nossh.NewFSConn(nil, rw) }
`,
  })
  assert.deepEqual(rows, [])
})

test('naming the helper ports op from a panel package is a second road (C24)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "github.com/shady2k/nocx/internal/helper/proto"

func op() string { return proto.OpSamplePorts }
`,
  })
  assert.deepEqual(rulesOf(rows), ['panel-host-path'])
  assert.equal(rows[0].object, SAMPLE_PORTS_OP)
})

test('the helper protocol package may name its own ports op (C24 positive)', () => {
  const rows = analyseOne({
    'internal/helper/client/second.go': `package client

import "github.com/shady2k/nocx/internal/helper/proto"

func op() string { return proto.OpSamplePorts }
`,
  })
  assert.deepEqual(rows, [])
})

test('a ProbeLease exec from a panel package is a second road (C24)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "github.com/shady2k/nocx/internal/helper/client"

func ports(l *client.ProbeLease) { _, _ = l.SamplePorts(nil, "") }
`,
  })
  assert.deepEqual(rulesOf(rows), ['panel-host-path'])
  assert.equal(rows[0].object, SAMPLE_PORTS)
})

test('any ProbeLease exec is refused, not only the ports sample (C24)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import "github.com/shady2k/nocx/internal/helper/client"

func home(l *client.ProbeLease) { _, _ = l.Home(nil) }
`,
  })
  assert.deepEqual(rulesOf(rows), ['panel-host-path'])
  assert.equal(rows[0].object, 'github.com/shady2k/nocx/internal/helper/client.(*ProbeLease).Home')
})

test('the same ProbeLease exec inside internal/farhelper is the road the helper rides (C24)', () => {
  const rows = analyseOne({
    'internal/farhelper/second.go': `package farhelper

import "github.com/shady2k/nocx/internal/helper/client"

func ports(l *client.ProbeLease) { _, _ = l.SamplePorts(nil, "") }
`,
  })
  assert.deepEqual(rows, [])
})

test('a lease acquired from a helper client a panel holds is a second road (C24)', () => {
  const rows = analyseOne({
    'internal/app/second.go': `package app

import (
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func lease(c *client.Client) { _, _ = c.AcquireProbeLease(nil, proto.LeaseParams{}) }
`,
  })
  assert.deepEqual(rulesOf(rows), ['panel-host-path'])
  assert.equal(rows[0].object, ACQUIRE_LEASE)
})

test('the same lease acquisition inside internal/farhelper is the axis doing its job (C24)', () => {
  const rows = analyseOne({
    'internal/farhelper/second.go': `package farhelper

import (
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func lease(c *client.Client) { _, _ = c.AcquireProbeLease(nil, proto.LeaseParams{}) }
`,
  })
  assert.deepEqual(rows, [])
})

// ─── the pass CI runs must find these, not only a fixture by pattern ─────
//
// The analyser walks the scope before it loads anything and loads only the
// packages it selected; `-pkgs` is how a test can name one package directly,
// and it is exactly how the previous fixtures missed this. A method call on a
// value — `s.Lookup`, `l.SamplePorts` — names the method and the receiver,
// never the package, so a selector-name filter dropped the package before
// go/types ever saw it. These fixtures go through the SAME selection the check
// uses, with no `-pkgs`.

/**
 * crossPackageStoreCall writes a two-package fixture whose caller reaches the
 * store through a CONCRETE receiver the provider package returns — no store
 * import anywhere in the caller — and spells the call as `callee` writes it, so
 * a parenthesized form is the same fixture with one pair of brackets.
 */
function crossPackageStoreCall(callee) {
  return withFixture(
    (name) => ({
      'internal/provider/provider.go': `package provider

import "github.com/shady2k/nocx/internal/hostmode"

type ModeProvider struct { store *hostmode.Store }
func (p ModeProvider) ModeStore() *hostmode.Store { return p.store }
func Provider() ModeProvider { return ModeProvider{} }
`,
      'internal/app/caller.go': `package app

import "github.com/shady2k/nocx/internal/${name}/internal/provider"

func lookup(fp string) { _, _ = ${callee}(fp) }
`,
    }),
    (dir) => aggregate(violationsFromFacts(analyze({ module: PROJECT_ROOT, scope: dir }))),
    join(PROJECT_ROOT, 'internal'),
  )
}

test('the full pass finds a cross-package store method call without a store import (C23)', () => {
  const rows = crossPackageStoreCall('provider.Provider().ModeStore().Lookup')
  assert.ok(rows.some((f) => f.rule === 'helper-decider' && f.object === MODE_LOOKUP))
})

/**
 * schedulerFixture is one package with a connector that is not farhelper's own,
 * and a scheduler built from it as `call` spells NewScheduler. The body is
 * shared so the parenthesized half of a pair differs from its bare form by the
 * brackets and nothing else.
 */
const schedulerFixture = (pkg, call = 'discovery.NewScheduler') => `package ${pkg}

import (
  "context"
  "github.com/shady2k/nocx/internal/discovery"
  "github.com/shady2k/nocx/internal/ssh"
)

type testConnector struct{}
func (testConnector) Lease(context.Context, string, ...ssh.ConnectOption) (discovery.Lease, error) { return nil, nil }
func (testConnector) Exec(context.Context, string, ...string) (string, error) { return "", nil }
func (testConnector) Done() <-chan struct{} { return nil }
func (testConnector) LostErr() error { return nil }
func schedule() { _ = ${call}(testConnector{}, nil) }
`

const CONNECTOR_FIXTURE = schedulerFixture('app')

test('the full pass reports a non-farhelper discovery connector (Ports)', () => {
  const rows = aggregate(
    violationsFromFacts(factsThroughTheFullPass({ 'internal/app/second.go': CONNECTOR_FIXTURE })),
  )
  assert.ok(rows.some((f) => f.rule === 'panel-host-path' && f.object.includes('testConnector')))
})

// ─── the seam is the axis's TYPE, wherever the call stands ───────────────
//
// ALX-738 finding 2, ALX-750 finding 1. The Ports seam is the argument of
// discovery.NewScheduler, and the exemption for it is the far helper's OWN
// declared type — the type declared in THIS module's internal/farhelper, and
// nowhere else. Each way that went wrong has a fixture here:
//
//   - the pointer: the future wiring hands `*farhelper.PortsConnector` to the
//     scheduler, and an exemption that unwrapped nothing reported it;
//   - the neighbour: `strings.HasPrefix(path, product/internal/farhelper)`
//     admits `internal/farhelperlegacy`, which is somebody else's package;
//   - the nested copy: a path that ENDS in /internal/farhelper —
//     `…/internal/app/internal/farhelper` — is another package again, so a
//     connector declared there is a foreign connector with a familiar name;
//   - the interface: a named interface declared in farhelper is a SHAPE, and
//     anything can implement it, so admitting one licences a foreign connector
//     behind a declaration that merely lives there (ALX-685).
//
// The exemptions are read from the RAW facts, before the policy: the policy
// drops every fact whose FILE is under internal/farhelper/, so a positive
// fixture whose call stands there proves nothing about the analyser. The call
// stands in the fixture's internal/app in every case below.

/**
 * FARHELPER_FIXTURE declares a connector at the owner's canonical path. Its
 * FILE and TYPE names are the fixture's and nobody else's: the product declares
 * `PortsConnector` in internal/farhelper/ports_connector.go, and a fixture that
 * took that name made the package stop loading with `PortsConnector redeclared`
 * the day the product's file landed — both canonical-path tests red on a
 * migration the rule accepts (review #5, ALX-755).
 */
const FARHELPER_FIXTURE = `package farhelper

import (
  "context"
  "github.com/shady2k/nocx/internal/discovery"
  "github.com/shady2k/nocx/internal/ssh"
)

// FixtureOwnerConnector stands where the axis's own connector will stand.
type FixtureOwnerConnector struct{}
func (FixtureOwnerConnector) Lease(context.Context, string, ...ssh.ConnectOption) (discovery.Lease, error) { return nil, nil }
func (FixtureOwnerConnector) Exec(context.Context, string, ...string) (string, error) { return "", nil }
func (FixtureOwnerConnector) Done() <-chan struct{} { return nil }
func (FixtureOwnerConnector) LostErr() error { return nil }

// FixtureOwnerShape is declared in farhelper and is still not the axis's connector.
type FixtureOwnerShape interface {
  Lease(context.Context, string, ...ssh.ConnectOption) (discovery.Lease, error)
}
`

/**
 * PRODUCT_PORTS_CONNECTOR is the product's side of the canonical path: its
 * standard `PortsConnector` and the named interface `Shape`, in a file whose
 * NAME the product chooses — `ports.go` in the fixtures below, deliberately not
 * the `ports_connector.go` review #5's fixture had guessed (review #6,
 * ALX-762).
 *
 * The declarations are minimal because the tree they stand in is a module of
 * the fixture's own (see withOwnerModule): the analyser names the seam by
 * IDENTITY — `discovery.NewScheduler` and its `Connector` parameter at the
 * canonical paths — and the fence by the package path of the type handed to the
 * scheduler, so a module that carries this module's path reproduces both
 * exactly. The real seam's own shape is covered by schedulerFixture, which runs
 * in the product module against the real packages.
 */
const PRODUCT_PORTS_CONNECTOR = `package farhelper

type PortsConnector struct{}

func (PortsConnector) Lease() {}

type Shape interface{ Lease() }
`

/** The fixture's own declarations at the owner's path: names only the fixture owns. */
const FIXTURE_OWNER_DECLARATIONS = `package farhelper

// FixtureOwnerConnector stands where the axis's own connector will stand.
type FixtureOwnerConnector struct{}

func (FixtureOwnerConnector) Lease() {}

// FixtureOwnerShape is a named interface at the owner's path — a shape, and not
// the axis's connector.
type FixtureOwnerShape interface{ Lease() }
`

/**
 * FIXTURE_DISCOVERY is the seam the fixture module's callers reach, at this
 * module's discovery path: `NewScheduler` taking the `Connector` interface.
 */
const FIXTURE_DISCOVERY = `package discovery

// Connector is the scheduler's parameter: the seam the Ports rule names.
type Connector interface{ Lease() }

type Scheduler struct{}

func NewScheduler(Connector, any) Scheduler { return Scheduler{} }
`

// These fixtures are packages INSIDE internal/ (their own import paths have to
// resolve, and a dot-directory is not importable), so their names carry the
// module and one extra segment: .../nocx/internal/<name>/internal/<pkg>.
const FIXTURE_BASE = join(PROJECT_ROOT, 'internal')
const fixtureImport = (name, pkg) => `github.com/shady2k/nocx/internal/${name}/internal/${pkg}`

/** The owner's canonical import path: the one the fence is written against. */
const OWNER_PATH = 'github.com/shady2k/nocx/internal/farhelper'

/**
 * connectorCallers is the package whose calls reach the Ports seam, with the
 * connector imported from `importPath` — the only thing a case varies is which
 * package owns the type it passes.
 */
const connectorCallers = (
  importPath,
  connector = 'FixtureOwnerConnector',
  shape = 'FixtureOwnerShape',
) => `package app

import (
  "github.com/shady2k/nocx/internal/discovery"
  "${importPath}"
)

func byValue(c farhelper.${connector}) { _ = discovery.NewScheduler(c, nil) }
func byPointer(c *farhelper.${connector}) { _ = discovery.NewScheduler(c, nil) }
func byShape(c farhelper.${shape}) { _ = discovery.NewScheduler(c, nil) }
`

/** The panel-host-path facts of one analysis, before the policy removes any. */
const panelFacts = (facts) => facts.filter((f) => f.rule === 'panel-host-path')

/**
 * nestedConnectorFacts puts the connector in a package one segment away from
 * the owner — the fixture's own internal/farhelper — and the calls in the
 * fixture's internal/app. Its path ENDS in /internal/farhelper, which is the
 * shape a suffix test mistook for the owner (ALX-750).
 */
function nestedConnectorFacts() {
  return panelFacts(
    withFixture(
      (name) => ({
        'internal/app/second.go': connectorCallers(fixtureImport(name, 'farhelper')),
        'internal/farhelper/fixture_owner_connector.go': FARHELPER_FIXTURE,
      }),
      (dir) => analyze({ module: PROJECT_ROOT, scope: dir }),
      FIXTURE_BASE,
    ),
  )
}

/**
 * withOwnerModule builds a MODULE of its own and runs `fn` with its root.
 *
 * Review #6 (ALX-762). The canonical-path fixture used to put its synthetic
 * files into the PRODUCT's package through an overlay, at file paths the
 * fixture had picked for the product. The product picks its own file names: the
 * day it declares `PortsConnector` in ports.go, the fixture's stand-in is a
 * second declaration in the same package and nothing loads; the day it spells
 * the file ports_connector.go, the overlay REPLACES the product's file and the
 * coexistence the test is about is never seen. A fixture of its own has neither
 * failure: the product's files are not in the tree it loads.
 *
 * The tree is THIS module's by import path and the fixture's by content: go.mod
 * and go.sum are copied from this checkout, so the analyser resolves the same
 * dependency graph and runs under the same toolchain the product does, and
 * cmd/conncheck is symlinked in so `go run ./cmd/conncheck` starts from the
 * fixture's root exactly as it starts from the product's — which is what keeps
 * the relative `-overlay` spelling of ALX-761 honest on both sides. Everything
 * under internal/ is synthetic.
 */
function withOwnerModule(files, fn, { base = join(PROJECT_ROOT, '.githooks', '.fixtures') } = {}) {
  const dir = join(base, `m${process.pid}-${++fixtureSeq}`)
  try {
    mkdirSync(join(dir, 'cmd'), { recursive: true })
    writeFileSync(join(dir, 'go.mod'), readFileSync(join(PROJECT_ROOT, 'go.mod')))
    writeFileSync(join(dir, 'go.sum'), readFileSync(join(PROJECT_ROOT, 'go.sum')))
    symlinkSync(join(PROJECT_ROOT, 'cmd', 'conncheck'), join(dir, 'cmd', 'conncheck'))
    for (const [path, src] of Object.entries(typeof files === 'function' ? files(dir) : files)) {
      const full = join(dir, path)
      mkdirSync(join(full, '..'), { recursive: true })
      writeFileSync(full, src)
    }
    return fn(dir)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

/**
 * ownerModuleFacts answers the canonical-path fixture's panel facts, from a
 * module of the fixture's own. `withProductFile` adds the product's side — its
 * `PortsConnector` and `Shape` — in a file whose name is the PRODUCT's, since
 * that is the whole claim: the product's file set is not the fixture's to name.
 */
function ownerModuleFacts({ withProductFile = false, productFile = 'ports.go' } = {}) {
  return panelFacts(
    withOwnerModule(
      {
        'internal/discovery/seam.go': FIXTURE_DISCOVERY,
        'internal/farhelper/fixture_owner_connector.go': FIXTURE_OWNER_DECLARATIONS,
        'internal/app/second.go': connectorCallers(
          OWNER_PATH,
          withProductFile ? 'PortsConnector' : 'FixtureOwnerConnector',
          withProductFile ? 'Shape' : 'FixtureOwnerShape',
        ),
        ...(withProductFile
          ? { [`internal/farhelper/${productFile}`]: PRODUCT_PORTS_CONNECTOR }
          : {}),
      },
      (dir) => analyze({ module: dir, scope: dir }),
    ),
  )
}

test('the full pass exempts a farhelper connector by value and by pointer (Ports)', () => {
  // Both calls stand in the fixture's internal/app, so a fact here would reach
  // the ratchet.
  const owned = ownerModuleFacts().filter((f) => f.object.includes('FixtureOwnerConnector'))
  assert.deepEqual(
    owned.map((f) => f.object),
    [],
    'the axis owns the connector it hands the scheduler, including the pointer',
  )
})

test('the full pass refuses a named interface declared in farhelper (Ports)', () => {
  // `FixtureOwnerShape` lives in farhelper and is not farhelper's connector:
  // any type may implement it, so exempting it licenses the road C24 closes.
  const shape = ownerModuleFacts().filter((f) => f.object.includes('FixtureOwnerShape'))
  assert.equal(shape.length, 1, 'the interface argument is the seam, and it is not the axis')
  // The argument is the type declared at the canonical path, which is why the
  // fixture stands in a module carrying this module's path: a fixture that read
  // the nested package would name a different path and prove nothing about the
  // owner.
  assert.ok(shape[0].object.includes(`(${OWNER_PATH}.FixtureOwnerShape)`), shape[0].object)
})

test('the canonical fixture stands beside a product file it did not name (Ports)', () => {
  // Review #5 (ALX-755) and #6 (ALX-762) are one claim here. The product owns
  // internal/farhelper: it declares `PortsConnector` and `Shape` beside the
  // fixture's declarations, in whatever file it chooses — `ports.go` below, not
  // the ports_connector.go the fixture used to guess — and the package that
  // holds both must load. A fixture that shared the product's package failed to
  // load the day the product took its own name (`PortsConnector redeclared`, or
  // the overlay replacing the product's file), and it failed for a migration
  // the rule accepts: the fence reads the owner by PATH, so the product's own
  // connector is the axis's and the named interface is still refused.
  const facts = ownerModuleFacts({ withProductFile: true })
  assert.deepEqual(
    facts.filter((f) => f.object.includes('PortsConnector')).map((f) => f.object),
    [],
    'the axis owns its own connector however the product spells its file set',
  )
  const shape = facts.filter((f) => f.object.includes('Shape'))
  assert.equal(shape.length, 1, 'the named interface declared in farhelper is still not the axis')
  assert.ok(shape[0].object.includes(`(${OWNER_PATH}.Shape)`), shape[0].object)
})

test('a nested package ending in internal/farhelper is not the axis (Ports)', () => {
  // ALX-750: the owner is a path in this tree, not a suffix. A type declared in
  // …/internal/app/internal/farhelper is somebody else's, by value and by
  // pointer, and the fact it produces is what the ratchet reads.
  const owned = nestedConnectorFacts().filter((f) => f.object.includes('FixtureOwnerConnector'))
  assert.equal(owned.length, 2, 'a nested copy of the package is not the axis')
  for (const f of owned) assert.match(f.object, /internal\/farhelper\.FixtureOwnerConnector\)$/)
})

test('a farhelper prefix without a boundary is not the axis (Ports)', () => {
  // internal/farhelperlegacy is a different package that merely starts with the
  // same letters; a prefix test admitted it.
  const facts = withFixture(
    (name) => ({
      'internal/app/second.go': `package app

import (
  "github.com/shady2k/nocx/internal/discovery"
  "${fixtureImport(name, 'farhelperlegacy')}"
)

func schedule(c farhelperlegacy.FixtureOwnerConnector) { _ = discovery.NewScheduler(c, nil) }
`,
      'internal/farhelperlegacy/fixture_owner_connector.go': FARHELPER_FIXTURE.replace(
        'package farhelper',
        'package farhelperlegacy',
      ),
    }),
    (dir) => analyze({ module: PROJECT_ROOT, scope: dir }),
    FIXTURE_BASE,
  ).filter((f) => f.rule === 'panel-host-path')
  assert.equal(facts.length, 1, 'the neighbour is not the axis')
  assert.match(facts[0].object, /farhelperlegacy\.FixtureOwnerConnector/)
})

test('a clone under a directory with a space still analyses its overlay (Ports)', () => {
  // Review #5 (ALX-755): GOFLAGS is split on spaces before any go command reads
  // it, so an overlay spelled as an ABSOLUTE path under "…/My Projects" reached
  // go as `-overlay=/…/My` plus a `non-flag` argument and the analyser died
  // before reading a fact. The module root here is the fixture's own module,
  // created under a directory whose name carries the space — the same spelling
  // a clone inside that directory has to `go` — and the overlay is
  // load-bearing: the owner's declaration exists only in `.owner/`, so a run
  // that never applied it loads no package and reads no fact at all.
  const spaced = mkdtempSync(join(tmpdir(), 'nocx clone-'))
  try {
    const facts = panelFacts(
      withOwnerModule(
        (dir) => ({
          'internal/discovery/seam.go': FIXTURE_DISCOVERY,
          '.owner/fixture_owner_connector.go': FIXTURE_OWNER_DECLARATIONS,
          'internal/app/second.go': connectorCallers(OWNER_PATH),
          '.owner/overlay.json': JSON.stringify({
            Replace: {
              [join(dir, 'internal', 'farhelper', 'fixture_owner_connector.go')]: join(
                dir,
                '.owner',
                'fixture_owner_connector.go',
              ),
            },
          }),
        }),
        (dir) => analyze({ module: dir, scope: dir, overlay: join(dir, '.owner', 'overlay.json') }),
        { base: spaced },
      ),
    )
    assert.deepEqual(
      facts.filter((f) => f.object.includes('FixtureOwnerConnector')).map((f) => f.object),
      [],
      'the owner is exempt whatever the clone directory is called',
    )
    // The absence above is only evidence if the pass READ this package: a walk
    // that skips it answers an empty list too, which is the shape of every
    // prefilter gap this file exists to catch (review #6, ALX-762). The named
    // interface is the mandatory fact — the owner exempts what it declares, not
    // any type implementing it.
    const shape = facts.filter((f) => f.object.includes('FixtureOwnerShape'))
    assert.equal(shape.length, 1, 'the pass read the package, so the exemption is evidence')
    assert.ok(shape[0].object.includes(`(${OWNER_PATH}.FixtureOwnerShape)`), shape[0].object)
  } finally {
    rmSync(spaced, { recursive: true, force: true })
  }
})

// ─── a parenthesized callee is the same call, everywhere ─────────────────
//
// ALX-738 finding 3. `(provider.Provider().ModeStore().Lookup)(fp)` and
// `(discovery.NewScheduler)(probes, logger)` are the same calls as their bare
// forms; only the expression around the callee differs. The candidate filter
// and the seam test both looked at `CallExpr.Fun` and got a ParenExpr, so the
// first call's package was never loaded and the second produced no fact even in
// a package the pass had already loaded.

test('the full pass finds a cross-package store method call behind a parenthesized callee (C23)', () => {
  const rows = crossPackageStoreCall('(provider.Provider().ModeStore().Lookup)')
  assert.ok(
    rows.some((f) => f.rule === 'helper-decider' && f.object === MODE_LOOKUP),
    'a parenthesized method call is the same second decider',
  )
})

test('the full pass resolves a scheduler constructor saved in a local (Ports)', () => {
  const src = schedulerFixture('app').replace(
    'func schedule() { _ = discovery.NewScheduler(testConnector{}, nil) }',
    'func schedule() { newScheduler := discovery.NewScheduler; _ = newScheduler(testConnector{}, nil) }',
  )
  const facts = factsThroughTheFullPass({ 'internal/app/second.go': src }).filter((f) => f.rule === 'panel-host-path')
  assert.equal(facts.length, 1)
})

test('the full pass resolves a dot-imported scheduler constructor (Ports)', () => {
  const facts = factsThroughTheFullPass({
    'internal/app/second.go': `package app
import (
  "context"
  . "github.com/shady2k/nocx/internal/discovery"
  "github.com/shady2k/nocx/internal/ssh"
)
type testConnector struct{}
func (testConnector) Lease(context.Context, string, ...ssh.ConnectOption) (Lease, error) { return nil, nil }
func build() { _ = NewScheduler(testConnector{}, nil) }
`,
  }).filter((f) => f.rule === 'panel-host-path')
  assert.equal(facts.length, 1)
})

test('the full pass sees a scheduler built through a parenthesized callee (Ports)', () => {
  const facts = factsThroughTheFullPass({
    'internal/app/second.go': schedulerFixture('app', '(discovery.NewScheduler)'),
  }).filter((f) => f.rule === 'panel-host-path')
  assert.equal(facts.length, 1, 'the parenthesized seam is the seam')
  assert.match(facts[0].object, /testConnector/)
})

test('the full pass finds a host-mode read that names only the store type (C23)', () => {
  const rows = aggregate(
    violationsFromFacts(factsThroughTheFullPass({ 'internal/app/second.go': MODE_FIXTURE })),
  )
  assert.deepEqual(rulesOf(rows), ['helper-decider'])
  assert.equal(rows[0].object, MODE_LOOKUP)
})

test('the full pass finds a consent read that names only the store type (C23)', () => {
  const rows = aggregate(
    violationsFromFacts(factsThroughTheFullPass({ 'internal/app/second.go': CONSENT_FIXTURE })),
  )
  assert.deepEqual(rulesOf(rows), ['helper-decider'])
  assert.equal(rows[0].object, CONSENT_LOOKUP)
})

test('the full pass includes a backquoted aliased helper import (C23)', () => {
  const facts = factsThroughTheFullPass({
    'internal/app/second.go': `package app
import hc \`github.com/shady2k/nocx/internal/helper/client\`
func dial() { _, _ = hc.Dial(nil, hc.Config{}) }
`,
  })
  assert.ok(facts.some((f) => f.rule === 'helper-decider' && f.object === HELPER_DIAL))
})

test('the full pass finds a ProbeLease exec called on a value (C24)', () => {
  const rows = aggregate(
    violationsFromFacts(
      factsThroughTheFullPass({
        'internal/app/second.go': `package app

import "github.com/shady2k/nocx/internal/helper/client"

func ports(l *client.ProbeLease) { _, _ = l.SamplePorts(nil, "") }
`,
      }),
    ),
  )
  assert.deepEqual(rulesOf(rows), ['panel-host-path'])
  assert.equal(rows[0].object, SAMPLE_PORTS)
})

test('the full pass still passes the same reads inside internal/farhelper', () => {
  assert.deepEqual(
    violationsFromFacts(factsThroughTheFullPass({ 'internal/farhelper/second.go': MODE_FIXTURE })),
    [],
  )
  assert.deepEqual(
    violationsFromFacts(
      factsThroughTheFullPass({ 'internal/farhelper/second.go': CONSENT_FIXTURE }),
    ),
    [],
  )
})

// ─── C23: the Git factory may not be built on a foreign client ───────────
//
// ALX-641: the factory carries its client to every later open, so one built on
// a client the axis did not choose keeps serving Git after the axis went
// unavailable or the host went raw. The reference itself is the fact: no
// static analysis can say where a `*client.Client` came from, and the axis is
// the only place that may build it.

const GIT_FACTORY_FIXTURE = `package app

import (
	"github.com/shady2k/nocx/internal/git/helper"
	"github.com/shady2k/nocx/internal/helper/client"
)

func factory(c *client.Client) { helper.NewFactory(c) }
`

test('a duplicate licensed Dial in connectLocked is refused', () => {
  const facts = factsFor({ [HOSTED_CARRIER_FILE]: HOSTED_CARRIER_FIXTURE.replace(
    'return client.Dial(nil, client.Config{})',
    'first, err := client.Dial(nil, client.Config{}); _, _ = client.Dial(nil, client.Config{}); return first, err',
  ) })
  const rows = violationsFromFacts(facts).filter((f) => f.rule === 'helper-decider' && f.func.endsWith('.connectLocked'))
  assert.ok(rows.length >= 1)
})

test('a Git factory built on a helper client is a second decider (C23)', () => {
  const rows = analyseOne({ 'internal/app/second.go': GIT_FACTORY_FIXTURE })
  assert.deepEqual(rulesOf(rows), ['helper-decider'])
  assert.equal(rows[0].object, GIT_FACTORY)
})

test('the same factory inside internal/farhelper is the axis building its own (C23)', () => {
  const rows = analyseOne({ 'internal/farhelper/second.go': GIT_FACTORY_FIXTURE })
  assert.deepEqual(rows, [])
})

// ─── the allowlist licence is per object, and the function narrows it ────
//
// The architect's decision (ALX-641, variant B): the hosted shell's carrier
// stays in internal/app, and C23 allows ITS dial by name rather than by
// baselining the call as debt. A licence that named only the file would also
// cover any second dial added beside it in the same file — so the entry names
// the function.
//
// The label the entry names comes from the ANALYSER, and these tests prove it:
// `realFacts` is the product tree's own output, and the pair of functions in
// ONE fixture file shows that the licence reaches the named one and stops at
// the other. A string a test typed would agree with an entry nothing in the
// tree matches.

const realFactsCache = { facts: null }
function realFacts() {
  if (realFactsCache.facts === null) realFactsCache.facts = analyze({})
  return realFactsCache.facts
}

test('every C23/C24 allowlist entry names a fact the analyser resolves there', () => {
  const facts = realFacts()
  // An entry whose FILE is not in this tree belongs to a prerequisite branch
  // that has not landed (internal/app/far_helper.go arrives with nocx-oalo1.6.2).
  // It is anchored by this test from the merge that brings the file, and by
  // nothing before it — the analyser cannot resolve a fact in a file that does
  // not exist.
  const licensed = ALLOWLIST.filter(
    (e) =>
      (e.rule === 'helper-decider' || e.rule === 'panel-host-path') &&
      existsSync(join(PROJECT_ROOT, e.file)),
  )
  assert.ok(licensed.length >= 7)
  for (const entry of licensed) {
    const hit = facts.find(
      (f) =>
        f.rule === entry.rule &&
        f.file === entry.file &&
        f.func === entry.func &&
        entry.objects.includes(f.object),
    )
    assert.ok(
      hit,
      `${entry.file} ${entry.func} is licensed for ${entry.objects.join(', ')}, and the ` +
        'analyser resolves no such fact there. An allowlist entry whose label nothing ' +
        'matches is a licence for a site that moved, and the real site is unlicensed.',
    )
    assert.equal(
      isAllowlisted(entry.rule, hit.file, hit.func, hit.object),
      true,
      "the analyser's own label must be the one the licence names",
    )
  }
})

const HOSTED_CARRIER_FILE = 'internal/app/hosted_shell_carrier.go'
const HOSTED_CARRIER_FIXTURE = `package app

import "github.com/shady2k/nocx/internal/helper/client"

type hostedShellCarrier struct{}

func (h *hostedShellCarrier) connectLocked() (*client.Client, error) {
	return client.Dial(nil, client.Config{})
}

func (h *hostedShellCarrier) open() (*client.Client, error) {
	return client.Dial(nil, client.Config{})
}
`

test('the hosted shell carrier is licensed by the function the analyser names, and only it', () => {
  const entry = ALLOWLIST.find(
    (e) => e.rule === 'helper-decider' && e.file === HOSTED_CARRIER_FILE && e.objects,
  )
  assert.ok(entry, 'the hosted shell carrier has no allowlist entry')
  assert.ok(entry.objects.includes(HELPER_DIAL))

  const facts = factsFor({ [HOSTED_CARRIER_FILE]: HOSTED_CARRIER_FIXTURE }).filter(
    (f) => f.rule === 'helper-decider' && f.object === HELPER_DIAL,
  )
  assert.equal(facts.length, 2, 'one dial in each function of the same file')
  // The licence is asked with the label the ANALYSER produced, normalised only
  // by the fixture's package-path segment — never with a string typed here.
  const byName = new Map(facts.map((f) => [asProduct(f.func), { ...f, func: asProduct(f.func) }]))
  assert.equal(
    byName.size,
    2,
    'the analyser labels the two functions differently, or the file has one entry point',
  )

  const carrier = byName.get(entry.func)
  assert.ok(carrier, `the analyser names no ${entry.func} in ${HOSTED_CARRIER_FILE}`)
  assert.equal(
    isAllowlisted('helper-decider', carrier.file, carrier.func, carrier.object),
    true,
    'the carrier function the entry names is the licensed one',
  )
  for (const [label, fact] of byName) {
    if (label === entry.func) continue
    assert.equal(
      isAllowlisted('helper-decider', fact.file, fact.func, fact.object),
      false,
      `a dial in ${label} inherited the carrier's licence, though it is another decider`,
    )
  }
  assert.equal(
    isAllowlisted('helper-decider', entry.file, entry.func, MODE_LOOKUP),
    false,
    'the licence names its objects, not the file',
  )
})

// ─── C23: the local helper's door is licensed by name ────────────────────
//
// The local dial reaches THIS machine's daemon and is no far-helper decision,
// so it is exempt — but as ONE FUNCTION, not as all of internal/helper. The
// directory exemption this replaced dropped every fact the analyser found in
// any of the helper's packages, so a Git or pane road moved there would have
// been reported and then silently discarded.

const LOCAL_DOOR_FILE = 'internal/helper/local/local.go'
const LOCAL_DOOR_FIXTURE = `package local

import "github.com/shady2k/nocx/internal/helper/client"

type Config struct{}

func openOnce(cfg Config) (*client.Client, error) {
	return client.Dial(nil, client.Config{})
}

func openAgain(cfg Config) (*client.Client, error) {
	return client.Dial(nil, client.Config{})
}
`

test('the local helper door is licensed by function, and a second door beside it is not', () => {
  const entry = ALLOWLIST.find((e) => e.rule === 'helper-decider' && e.file === LOCAL_DOOR_FILE)
  assert.ok(entry, 'the local helper door has no allowlist entry')
  assert.ok(entry.objects.includes(HELPER_DIAL))

  const facts = factsFor({ [LOCAL_DOOR_FILE]: LOCAL_DOOR_FIXTURE }).filter(
    (f) => f.rule === 'helper-decider' && f.object === HELPER_DIAL,
  )
  assert.equal(facts.length, 2)
  const byName = new Map(facts.map((f) => [asProduct(f.func), { ...f, func: asProduct(f.func) }]))
  const door = byName.get(entry.func)
  assert.ok(door, `the analyser names no ${entry.func} in ${LOCAL_DOOR_FILE}`)
  assert.equal(isAllowlisted('helper-decider', door.file, door.func, door.object), true)
  for (const [label, fact] of byName) {
    if (label === entry.func) continue
    assert.equal(
      isAllowlisted('helper-decider', fact.file, fact.func, fact.object),
      false,
      `a dial in ${label} inherited the local door's licence`,
    )
  }
})

test('a helper-client dial anywhere in internal/helper is still a fact, and is refused', () => {
  const rows = analyseOne({
    'internal/helper/tunnelchan/second.go': DIAL_FIXTURE,
  })
  assert.deepEqual(rulesOf(rows), ['helper-decider'])
  assert.equal(rows[0].object, HELPER_DIAL)
})

test('the helper-decider rules exempt no directory of internal/helper any more', () => {
  // The licence is a name now, not a subtree: this is the assertion that
  // catches a directory being put back.
  assert.deepEqual(RULE_EXEMPT_DIRS['helper-decider'], ['internal/farhelper/'])
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
  for (const entry of baseline) {
    assert.equal(typeof entry.count, 'number', violationKey(entry))
    assert.ok(entry.count > 0, violationKey(entry))
    assert.ok(entry.func.length > 0, violationKey(entry))
    assert.ok(entry.object.length > 0, violationKey(entry))
    assert.ok(entry.reason.trim().length > 0, violationKey(entry))
  }
})

/**
 * The debt-reason policy is one function over WHATEVER rows the baseline holds.
 * It must never require a row to exist: the ratchet's whole point is that debt
 * disappears, and the commit that deletes a row (nocx-oalo1.12 moves
 * ssh.NewFSConn onto the far helper) runs this test in the same CI job that
 * told the author to delete it (ALX-738 finding 1).
 */
function assertDebtReasons(entries) {
  const expected = new Map([
    [GIT_FACTORY, /nocx-oalo1\.6\.3\b/],
    [CONSENT_LOOKUP, /nocx-oalo1\.6\.3\b/],
    [HELPER_DIAL, /nocx-oalo1\.6\.3\b/],
    [FS_CONN, /nocx-oalo1\.12\b/],
    [
      'github.com/shady2k/nocx/internal/discovery.Connector(*github.com/shady2k/nocx/internal/app.helperProbes)',
      /nocx-oalo1\.13\b/,
    ],
  ])
  for (const entry of entries) {
    if (!['helper-decider', 'panel-host-path'].includes(entry.rule)) continue
    assert.match(entry.reason, /nocx-oalo1\.\d+(?:\.\d+)*\b/, violationKey(entry))
    const pattern = expected.get(entry.object)
    if (pattern) assert.match(entry.reason, pattern, violationKey(entry))
  }
}

/** The Files row's own two guards, over every Files row present — including none. */
function assertFilesDebtReason(entries) {
  for (const entry of entries.filter((e) => e.rule === 'panel-host-path' && e.object === FS_CONN)) {
    assert.match(entry.reason, /nocx-oalo1\.12\b/, violationKey(entry))
    assert.doesNotMatch(entry.reason, /nocx-oalo1\.6\.12/, violationKey(entry))
  }
}

test('remaining helper and panel baseline debt names its owning task', () => {
  assertDebtReasons(loadBaseline())
})

test('the debt-reason policy accepts a baseline whose Files row is already gone', () => {
  // The row the nocx-oalo1.12 migration deletes, and nothing else. A policy that
  // tests the ROW rather than what it says refuses that commit; the ratchet's
  // `stale` finding is what makes the deletion mandatory, and the fixtures are
  // what keep the sites visible.
  const withoutFiles = loadBaseline().filter(
    (e) => !(e.rule === 'panel-host-path' && e.object === FS_CONN),
  )
  assert.doesNotThrow(() => assertDebtReasons(withoutFiles))
  assert.doesNotThrow(() => assertFilesDebtReason(withoutFiles))
})

test('the debt-reason policy accepts an empty baseline', () => {
  // No debt is the goal. Every row is a licence with a deadline, and the last
  // commit that retires one must not be the one the test refuses.
  assert.doesNotThrow(() => {
    assertDebtReasons([])
    assertFilesDebtReason([])
  })
})

test('a reason names the bead, never the branch that carries it', () => {
  // A branch suffix reads as a task id and resolves to nothing: `br show`
  // answers a branch-suffixed id with an empty result, so a reason that names
  // one explains the site by a bead nobody can open.
  const reasons = [
    ...loadBaseline().map((e) => [violationKey(e), e.reason]),
    ...ALLOWLIST.map((e) => [`allowlist ${e.rule} ${e.file}`, e.reason]),
  ]
  for (const [where, reason] of reasons) {
    const branch = /nocx-oalo1\.\d+(?:\.\d+)*[a-z]\b/.exec(reason)
    assert.equal(
      branch,
      null,
      `${where} names ${branch?.[0]}, which is a branch and not a task: ${reason}`,
    )
  }
})

test('the Files debt names the stage the migration is recorded in', () => {
  assertFilesDebtReason(loadBaseline())
})

// ─── the allowlist narrows: the carrier is exempt, the supervisor is not ──
//
// Design §5.1.1 gives the supervisor `monoclock` and `log` and forbids it
// `internal/ssh` and `x/crypto`: it is carrier-neutral, and a transport type in
// the state machine is the beginning of a second owner of every connection.
// Stage 2 added `internal/hostconn/` to ALLOWED_DIRS, which is the widening the
// review refused (finding 13): this fixture is the check that it stays out.

test('an x/crypto import in the carrier-neutral supervisor is refused', () => {
  const rows = analyseOne({
    'internal/hostconn/supervisor.go': `package hostconn

import gossh "golang.org/x/crypto/ssh"

var _ *gossh.Client
`,
  })
  assert.deepEqual(rulesOf(rows), ['xcrypto-ssh-import'])
  assert.equal(rows[0].object, 'golang.org/x/crypto/ssh.Client')
})

test('the same import in the carrier is the carrier doing its job', () => {
  const rows = analyseOne({
    'internal/helper/sshconn/link.go': `package sshconn

import gossh "golang.org/x/crypto/ssh"

var _ *gossh.Client
`,
  })
  assert.deepEqual(rows, [])
})

test('the transport packages are the only ones exempt, and the supervisor is not one of them', () => {
  // Four: the library's home, the carrier that implements the seam, the service
  // that serves the carrier over the helper protocol, and the fault proxy that
  // speaks SSH because a proxy must. `internal/hostconn` left the list in
  // nocx-oalo1.4's review round 2 (finding 13) — design §5.1.1's "may never
  // import x/crypto" is a rule about the supervisor, and an ALLOWED_DIRS entry
  // was the licence that made it unenforced.
  assert.deepEqual(ALLOWED_DIRS, [
    'internal/ssh/',
    'internal/helper/sshsvc/',
    'internal/helper/sshconn/',
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
