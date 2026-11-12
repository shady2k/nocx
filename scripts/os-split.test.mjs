import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { test } from 'node:test'

const makefile = readFileSync(new URL('../Makefile', import.meta.url), 'utf8')

function fixture(run) {
  const root = mkdtempSync(join(tmpdir(), 'nocx-os-split-'))
  try {
    writeFileSync(join(root, 'Makefile'), makefile)
    return run(root)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
}

function make(root, args) {
  return execFileSync(
    'make',
    ['--no-print-directory', '-s', 'HOST_GOOS=linux', 'GO=true', ...args],
    {
      cwd: root,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  ).trim()
}

function variable(root, name, args = []) {
  return make(root, [
    ...args,
    '--eval',
    `print-variable:;@printf '%s' '$(${name})'`,
    'print-variable',
  ])
}

test('desktopidentity belongs to the real OS-split package set', () => {
  fixture((root) => {
    assert.ok(variable(root, 'OS_PKG_DIRS').split(/\s+/).includes('internal/desktopidentity'))
  })
})

test('OS regex is derived from the package list, including later additions', () => {
  fixture((root) => {
    assert.equal(
      variable(root, 'OS_PKG_RE', ['OS_PKG_DIRS=internal/example internal/another']),
      '(internal/example|internal/another)',
    )
  })
})

test('OS constraint derivation excludes testdata, like go list ./...', () => {
  fixture((root) => {
    for (const file of [
      'internal/example/platform.go',
      'internal/example/testdata/probe/main.go',
    ]) {
      const path = join(root, file)
      mkdirSync(dirname(path), { recursive: true })
      writeFileSync(path, '//go:build linux\n\npackage example\n')
    }
    // `ci-os-split` ends by handing the directories it derived to
    // scripts/mac-only-tests.mjs (nocx-9id74). The real script cannot answer
    // for this two-file tree: it refuses an empty macOS selection, and it
    // checks its own LISTED entry against internal/storage, which the fixture
    // does not carry. So the fixture carries a stand-in at the path the target
    // runs, as it stands in for `go` below, and records what it was asked —
    // which is this test's assertion: the derived directories, testdata
    // excluded.
    const macCheck = join(root, 'scripts/mac-only-tests.mjs')
    const macArgs = join(root, 'mac-only-tests-args')
    mkdirSync(dirname(macCheck), { recursive: true })
    writeFileSync(
      macCheck,
      `import { writeFileSync } from 'node:fs'\nwriteFileSync(${JSON.stringify(macArgs)}, process.argv.slice(2).join(' ') + '\\n')\n`,
      { mode: 0o755 },
    )
    assert.match(make(root, ['OS_PKG_DIRS=internal/example', 'OS_EXEMPT=', 'ci-os-split']), /ok$/)
    assert.equal(readFileSync(macArgs, 'utf8').trim(), 'check internal/example')
  })
})

test('OS split refuses a real desktopidentity package omitted from the list', () => {
  fixture((root) => {
    const path = join(root, 'internal/desktopidentity/identity_linux.go')
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, '//go:build linux\n\npackage desktopidentity\n')
    assert.throws(
      () =>
        make(root, ['OS_PKG_DIRS=internal/example', 'OS_EXEMPT=internal/example', 'ci-os-split']),
      (error) =>
        error.status === 2 && /not in OS_PKG_DIRS: internal\/desktopidentity/.test(error.stdout),
    )
  })
})

test('portable partition excludes OS packages only at a path boundary', () => {
  fixture((root) => {
    const fakeGo = join(root, 'go-fixture')
    writeFileSync(
      fakeGo,
      '#!/bin/sh\nprintf "%s\\n" github.com/shady2k/nocx/internal/desktopidentity github.com/shady2k/nocx/internal/desktopidentity/child github.com/shady2k/nocx/internal/desktopidentityextra github.com/shady2k/nocx/internal/desktopidentity-suffix\n',
      { mode: 0o700 },
    )
    assert.equal(
      make(root, [`GO=${fakeGo}`, 'OS_PKG_DIRS=internal/desktopidentity', 'print-portable-pkgs']),
      'github.com/shady2k/nocx/internal/desktopidentityextra\ngithub.com/shady2k/nocx/internal/desktopidentity-suffix',
    )
  })
})
