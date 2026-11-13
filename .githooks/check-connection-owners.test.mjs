import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import {
  ALLOWED_DIRS,
  partition,
  unreasonedEntries,
  violationsInText,
} from './check-connection-owners.mjs'
import { planBaselineUpdate } from './update-connection-owners-baseline.mjs'

/**
 * The connection-owner ratchet (nocx-oalo1.3.4) refuses a NEW Go site outside
 * the SSH transport that decides a fact about an SSH connection. These
 * fixtures are the rules themselves, one import, one dial and one exit cause
 * at a time, plus the two directions the baseline is allowed to move: a site
 * taken through the transport shrinks it, and a new second owner cannot be
 * added by accident.
 */

const OUTSIDE = 'internal/app/second_owner.go'
const rules = (src, file = OUTSIDE) => violationsInText(file, src).map((v) => v.rule)

test('an x/crypto/ssh import outside the transport fails the check', () => {
  const src = `package app

import (
	gossh "golang.org/x/crypto/ssh"
)

var _ = gossh.Dial
`
  const violations = violationsInText(OUTSIDE, src)
  assert.equal(violations.length, 1)
  assert.equal(violations[0].rule, 'xcrypto-ssh-import')
  assert.equal(violations[0].text, 'gossh "golang.org/x/crypto/ssh"')
})

test('the transport packages are where x/crypto belongs, and are not reported', () => {
  const src = `package ssh

import (
	gossh "golang.org/x/crypto/ssh"
)
`
  for (const dir of ALLOWED_DIRS) {
    assert.deepEqual(rules(src, `${dir}anything.go`), [], dir)
  }
})

test("x/crypto's agent and knownhosts subpackages are the same rule", () => {
  for (const path of ['golang.org/x/crypto/ssh/agent', 'golang.org/x/crypto/ssh/knownhosts']) {
    assert.deepEqual(rules(`package app\n\nimport "${path}"\n`), ['xcrypto-ssh-import'], path)
  }
})

test('a TCP dial outside the transport fails the check', () => {
  assert.deepEqual(rules('package app\n\nfunc f() { _, _ = net.Dial("tcp", addr) }\n'), [
    'net-dial',
  ])
})

test('a unix-socket dial is not an SSH connection and passes', () => {
  assert.deepEqual(rules('package app\n\nfunc f() { _, _ = net.Dial("unix", sock) }\n'), [])
  assert.deepEqual(
    rules('package app\n\nfunc f() { _, _ = d.DialContext(ctx, "unix", path) }\n'),
    [],
  )
})

test('a dialer built or used outside the transport fails the check', () => {
  assert.deepEqual(rules('package app\n\nvar d = net.Dialer{Timeout: t}\n'), ['net-dial'])
  assert.deepEqual(rules('package app\n\nvar c = net.DialTimeout("tcp", a, t)\n'), ['net-dial'])
  assert.deepEqual(rules('package app\n\nfunc f() { _, _ = r.Route.DialContext(ctx, n, a) }\n'), [
    'net-dial',
  ])
})

test('reading a remote exit cause outside the transport fails the check', () => {
  assert.deepEqual(rules('package app\n\nvar se *gossh.ExitError\n'), ['exit-error-read'])
  assert.deepEqual(rules('package app\n\nvar se *ssh.ExitError\n'), ['exit-error-read'])
})

test('a second keepalive prober outside the transport fails the check', () => {
  assert.deepEqual(
    rules(
      'package app\n\nfunc f() { _, _, _ = t.SendRequest("keepalive@openssh.com", true, nil) }\n',
    ),
    ['keepalive-request'],
  )
})

test('comments and prose names are not calls', () => {
  const src = `package app

// golang.org/x/crypto/ssh gives no per-channel interrupt.
// net.Dialer never performs the lookup httptrace's DNS hooks report.
func f() {}
`
  assert.deepEqual(rules(src), [])
})

test('an allowlisted key-format site passes, and is still held to the other rules', () => {
  const keyFormat = `package transport

import "golang.org/x/crypto/ssh"

var _ = ssh.ParsePublicKey
`
  assert.deepEqual(rules(keyFormat, 'internal/transport/ws.go'), [])
  // The allowlist names ONE rule for that file; the others still apply.
  assert.deepEqual(
    rules('package transport\n\nvar se *gossh.ExitError\n', 'internal/transport/ws.go'),
    ['exit-error-read'],
  )
})

test('the allowlist entry that is not a connection at all', () => {
  assert.deepEqual(
    rules('package mcpstdio\n\nvar d = &net.Dialer{}\n', 'internal/mcpstdio/mcpstdio.go'),
    [],
  )
  assert.deepEqual(
    rules(
      'package tunnel\n\nfunc f() { _, _ = net.Dial("tcp", addr) }\n',
      'internal/tunnel/remote.go',
    ),
    [],
  )
})

test('a baselined site passes, and removing it passes the check and shrinks the baseline', () => {
  const text = 'conn, err := r.Route.DialContext(ctx, network, addr)'
  const baseline = [
    { rule: 'net-dial', file: OUTSIDE, text, reason: 'the HTTP route, leaves with the agent' },
  ]
  const present = violationsInText(OUTSIDE, `package app\n\nfunc f() {\n\t${text}\n}\n`)
  assert.equal(partition(present, baseline).length, 0, 'a baselined line is not new')

  // The site moved to the transport: the violation is gone, the check passes,
  // and the updater drops the entry instead of keeping a licence nobody uses.
  const removed = violationsInText(OUTSIDE, 'package app\n\nfunc f() {}\n')
  assert.equal(partition(removed, baseline).length, 0)
  const shrunk = planBaselineUpdate({ violations: removed, old: baseline })
  assert.deepEqual(shrunk.entries, [])
  assert.deepEqual(shrunk.dropped, baseline)
})

test('a new second owner cannot be added to the baseline without its flag', () => {
  const site = {
    rule: 'net-dial',
    file: OUTSIDE,
    text: 'conn, err := net.Dial("tcp", addr)',
  }
  assert.throws(
    () => planBaselineUpdate({ violations: [site], old: [] }),
    /REFUSING to update baseline: 1 site/,
  )
  const grown = planBaselineUpdate({ violations: [site], old: [], allowGrowth: true })
  assert.equal(grown.entries.length, 1)
  assert.equal(grown.entries[0].reason, '', 'a grown entry starts with no reason')
})

test('an existing reason survives a rebuild of the baseline', () => {
  const text = 'var se *gossh.ExitError'
  const old = [{ rule: 'exit-error-read', file: OUTSIDE, text, reason: 'stage 3 maps it' }]
  const violations = violationsInText(OUTSIDE, `package app\n\n${text}\n`)
  const plan = planBaselineUpdate({ violations, old })
  assert.deepEqual(plan.entries, old)
  assert.deepEqual(plan.growth, [])
})

test('a baseline entry with no reason is refused, not silently honoured', () => {
  const baseline = [{ rule: 'net-dial', file: OUTSIDE, text: 'x', reason: '  ' }]
  assert.equal(unreasonedEntries(baseline).length, 1)
  assert.equal(unreasonedEntries([{ ...baseline[0], reason: 'a unix-socket default' }]).length, 0)
})

test('the unchanged tree passes the check', () => {
  const out = execFileSync('node', ['.githooks/check-connection-owners.mjs'], {
    cwd: fileURLToPath(new URL('..', import.meta.url)),
    encoding: 'utf8',
  })
  assert.match(out, /connection-owner ratchet/)
})
