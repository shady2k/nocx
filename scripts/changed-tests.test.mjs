import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import {
  changedTestNames,
  filterPlan,
  goTestFunctions,
  isE2eSpecPath,
  listChangedTests,
  packageDir,
  packageInSet,
  parseNameStatus,
  repeatInvocations,
} from './changed-tests.mjs'

test('name-status records: added, modified, deleted and renamed', () => {
  const raw = [
    'M\0e2e/one.spec.ts\0D\0e2e/gone.spec.ts\0A\0e2e/new.spec.ts\0',
    'R100\0old_test.go\0new_test.go\0',
  ].join('')
  assert.deepEqual(parseNameStatus(raw), [
    { status: 'M', path: 'e2e/one.spec.ts' },
    { status: 'D', path: 'e2e/gone.spec.ts' },
    { status: 'A', path: 'e2e/new.spec.ts' },
    { status: 'R', path: 'new_test.go', from: 'old_test.go' },
  ])
  assert.throws(() => parseNameStatus('R100\0only-one\0'), /incomplete R100 record/)
})

test('a spec is a top-level .spec.ts under e2e, and a package is its directory', () => {
  assert.equal(isE2eSpecPath('e2e/a.spec.ts'), true)
  assert.equal(isE2eSpecPath('e2e/nested/a.spec.ts'), false)
  assert.equal(isE2eSpecPath('e2e/a.unit.mts'), false)
  assert.equal(isE2eSpecPath('frontend/src/a.spec.ts'), false)
  assert.equal(packageDir('internal/app/ssh_test.go'), './internal/app')
  assert.equal(packageDir('root_test.go'), './')
})

test('the Go reader finds top-level Test functions and their bodies only', () => {
  const source = `
package example

// func TestInAComment(t *testing.T) {}
func helper() { if true { } }

var braces = "}{"

func (x *T) TestMethod(t *testing.T) { t.Fatal("not a test") }

func TestOne(t *testing.T) {
	if strings.Contains("{", "}") { // a brace in a string and a comment
		t.Fatal("one")
	}
}

func TestTwo(t *testing.T) (int, error) { return 0, nil }
func notATest(t *testing.T) {}
func Testhelper(t *testing.T) { t.Log("Go does not treat this as a test") }
`
  const funcs = goTestFunctions(source)
  assert.deepEqual([...funcs.keys()].sort(), ['TestOne', 'TestTwo'])
  assert.match(funcs.get('TestOne'), /t\.Fatal\("one"\)/)
  assert.match(funcs.get('TestTwo'), /return 0, nil/)
})

test('a test counts when added or when its own body changed', () => {
  const base = `
package p
func TestKept(t *testing.T) { t.Log("same") }
func TestChanged(t *testing.T) { t.Log("before") }
func TestDeleted(t *testing.T) { t.Log("gone") }
func helper() { t.Log("helper") }
`
  const head = `
package p
// a comment beside the function is not its body
func TestKept(t *testing.T) { t.Log("same") }
func TestChanged(t *testing.T) { t.Log("after") }
func TestAdded(t *testing.T) { t.Log("new") }
func helper() { t.Log("helper changed") }
`
  assert.deepEqual(changedTestNames(base, head), ['TestAdded', 'TestChanged'])
  assert.deepEqual(changedTestNames(null, head), ['TestAdded', 'TestChanged', 'TestKept'])
})

test('a changed package list is matched in both spellings the Makefile prints', () => {
  const module = 'github.com/shady2k/nocx'
  const portable = 'github.com/shady2k/nocx github.com/shady2k/nocx/internal/foo'
  assert.equal(packageInSet('./', portable, module), true)
  assert.equal(packageInSet('./internal/foo', portable, module), true)
  assert.equal(packageInSet('./internal/foo/bar', portable, module), false)
  const os = './internal/pty/... ./internal/storage/... ./cmd/e2e-sshd/...'
  assert.equal(packageInSet('./internal/pty', os, module), true)
  assert.equal(packageInSet('./internal/pty/child', os, module), true)
  assert.equal(packageInSet('./internal/other', os, module), false)
})

test('the repeat is one go test per package, selecting exactly the changed names', () => {
  const plan = {
    specs: ['e2e/a.spec.ts'],
    go: [
      { package: './internal/foo', tests: ['TestA', 'TestB'] },
      { package: './internal/bar', tests: ['TestC'] },
    ],
  }
  assert.deepEqual(
    filterPlan(plan.go, './internal/foo/...', 'm').map((entry) => entry.package),
    ['./internal/foo'],
  )
  assert.deepEqual(
    repeatInvocations(plan.go, {
      packages: './internal/foo/...',
      module: 'm',
      count: 20,
      race: true,
    }),
    [
      {
        command: 'go',
        args: ['test', '-race', '-count', '20', '-run', '^(TestA|TestB)$', './internal/foo'],
      },
    ],
  )
})

test('a real repository lists changed specs and test bodies, never deletions', () => {
  const root = mkdtempSync(join(tmpdir(), 'nocx-changed-tests-'))
  const git = (args, cwd = root) =>
    execFileSync('git', args, { cwd, encoding: 'utf8' }).replace(/\n$/, '')
  try {
    git(['init', '-q'])
    git(['config', 'user.email', 'test@example.com'])
    git(['config', 'user.name', 'test'])
    mkdirSync(join(root, 'e2e'))
    mkdirSync(join(root, 'internal', 'pkg'), { recursive: true })
    writeFileSync(join(root, 'e2e', 'kept.spec.ts'), 'test("kept", () => {})\n')
    writeFileSync(join(root, 'e2e', 'changed.spec.ts'), 'test("old", () => {})\n')
    writeFileSync(join(root, 'e2e', 'gone.spec.ts'), 'test("gone", () => {})\n')
    writeFileSync(
      join(root, 'internal', 'pkg', 'pkg_test.go'),
      [
        'package pkg',
        'func TestKept(t *testing.T) {}',
        'func TestChanged(t *testing.T) { t.Log("before") }',
        'func TestGone(t *testing.T) {}',
        '',
      ].join('\n'),
    )
    git(['add', '.'])
    git(['commit', '-q', '-m', 'base'])
    const base = git(['rev-parse', 'HEAD'])

    writeFileSync(join(root, 'e2e', 'changed.spec.ts'), 'test("new", () => {})\n')
    writeFileSync(join(root, 'e2e', 'new.spec.ts'), 'test("new", () => {})\n')
    rmSync(join(root, 'e2e', 'gone.spec.ts'))
    writeFileSync(
      join(root, 'internal', 'pkg', 'pkg_test.go'),
      [
        'package pkg',
        'func TestKept(t *testing.T) {}',
        'func TestChanged(t *testing.T) { t.Log("after") }',
        'func TestNew(t *testing.T) {}',
        '',
      ].join('\n'),
    )
    git(['add', '.'])
    git(['commit', '-q', '-m', 'head'])
    const head = git(['rev-parse', 'HEAD'])

    assert.deepEqual(listChangedTests({ base, head, root, git }), {
      specs: ['e2e/changed.spec.ts', 'e2e/new.spec.ts'],
      go: [{ package: './internal/pkg', tests: ['TestChanged', 'TestNew'] }],
    })
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
