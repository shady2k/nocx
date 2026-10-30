import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { test } from 'node:test'

// Execute the workflow's actual heredocs, not a second copy of their shell.
const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
const fixtures = [
  ...workflow.matchAll(/^( +)dbus-run-session -- bash <<'TESTEOF'\n[\s\S]*?^\1TESTEOF$/gm),
].map((match) =>
  match[0]
    .split('\n')
    .map((line) => line.slice(match[1].length))
    .join('\n'),
)
assert.equal(fixtures.length, 2, 'both Secret Service workflow fixtures must be exercised')

function runFixture(shell, scenario) {
  const dir = mkdtempSync(join(tmpdir(), 'nocx-quarantine-fixture-'))
  const events = join(dir, 'events')
  const bin = join(dir, 'bin')
  writeFileSync(events, '')
  mkdirSync(bin)
  mkdirSync(join(dir, 'scripts'))
  const executable = (path, body) =>
    writeFileSync(path, `#!/usr/bin/env bash\nset -euo pipefail\n${body}`, { mode: 0o755 })
  executable(
    join(bin, 'dbus-run-session'),
    `
    printf 'dbus\n' >> "$EVENTS"
    test "$1" = --
    shift
    exec "$@"
  `,
  )
  executable(
    join(bin, 'gnome-keyring-daemon'),
    `
    read -r password || true
    test "$password" = nocx-ci
    case "$1" in
      --daemonize)
        test "$2" = --login
        printf 'daemon\n' >> "$EVENTS"
        # Valid output must not let eval conceal a failed startup.
        printf 'export DAEMON_READY=1\n'
        if [[ "$SCENARIO" = daemon-fails ]]; then exit 41; fi
        ;;
      --unlock)
        printf 'unlock\n' >> "$EVENTS"
        test "\${DAEMON_READY:-}" = 1
        if [[ "$SCENARIO" = unlock-fails ]]; then exit 42; fi
        ;;
      *) exit 99 ;;
    esac
  `,
  )
  executable(
    join(dir, 'scripts', 'ci-run-logged.sh'),
    `
    printf 'helper:%s\n' "$1" >> "$EVENTS"
    if [[ "$1" != *-local-ssh ]]; then
      if [[ "$SCENARIO" = first-helper-fails ]]; then exit 43; fi
      if [[ "$SCENARIO" = ordinary-test-fails ]]; then
        # The real helper records test failures but returns zero for the gate.
        printf 'recorded-test-failure\n' >> "$EVENTS"
      fi
    fi
    exit 0
  `,
  )
  try {
    const result = spawnSync('bash', ['-euo', 'pipefail', '-c', shell], {
      cwd: dir,
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${bin}:${process.env.PATH}`,
        EVENTS: events,
        SCENARIO: scenario,
        PKGS: './first',
        LOCAL_SSH_PKGS: './second',
      },
    })
    assert.ifError(result.error)
    return { ...result, events: readFileSync(events, 'utf8').trim().split('\n') }
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

for (const shell of fixtures) {
  const name = /scripts\/ci-run-logged\.sh (\S+)/.exec(shell)[1]
  const first = `helper:${name}`
  const second = `helper:${name}-local-ssh`

  test(`${name}: failed daemon startup cannot be hidden by eval or later helpers`, () => {
    const result = runFixture(shell, 'daemon-fails')
    assert.equal(result.status, 41, result.stderr)
    assert.deepEqual(result.events, ['dbus', 'daemon'])
  })

  test(`${name}: failed keyring unlock stops before either helper`, () => {
    const result = runFixture(shell, 'unlock-fails')
    assert.equal(result.status, 42, result.stderr)
    assert.deepEqual(result.events, ['dbus', 'daemon', 'unlock'])
  })

  test(`${name}: first helper infrastructure failure cannot be hidden by the second helper`, () => {
    const result = runFixture(shell, 'first-helper-fails')
    assert.equal(result.status, 43, result.stderr)
    assert.deepEqual(result.events, ['dbus', 'daemon', 'unlock', first])
  })

  test(`${name}: ordinary recorded test failure still runs the second suite`, () => {
    const result = runFixture(shell, 'ordinary-test-fails')
    assert.equal(result.status, 0, result.stderr)
    assert.deepEqual(result.events, [
      'dbus',
      'daemon',
      'unlock',
      first,
      'recorded-test-failure',
      second,
    ])
  })

  test(`${name}: a healthy fixture runs both suites with the daemon environment`, () => {
    const result = runFixture(shell, 'success')
    assert.equal(result.status, 0, result.stderr)
    assert.deepEqual(result.events, ['dbus', 'daemon', 'unlock', first, second])
  })
}
