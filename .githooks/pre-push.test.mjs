import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'

// A push that introduces no commit reads no tracker state (nocx-fg79x.3): the
// backlog's rules run when the store is published, and pre-push keeps only the
// commit-link check, which reads the commits the push introduces. The fake `br`
// leaves a marker if anything on this path calls it.
test('a push that introduces no commit does not read the tracker', () => {
  const bin = mkdtempSync(join(tmpdir(), 'nocx-pre-push-'))
  const marker = join(bin, 'br-ran')
  try {
    const br = join(bin, 'br')
    writeFileSync(br, `#!/bin/sh\ntouch '${marker}'\nexit 1\n`)
    const chmod = spawnSync('chmod', ['+x', br])
    assert.equal(chmod.status, 0)
    const zero = '0'.repeat(40)
    const input = `(delete) ${zero} refs/heads/gone ${zero}\n`
    const result = spawnSync(
      'sh',
      ['.githooks/pre-push', 'origin', 'https://example.invalid/repo.git'],
      {
        encoding: 'utf8',
        input,
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}` },
      },
    )
    assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`)
    assert.doesNotMatch(result.stderr, /backlog comparison skipped|tracker status/)
    assert.equal(spawnSync('test', ['-e', marker]).status, 1, 'pre-push read the tracker')
  } finally {
    rmSync(bin, { recursive: true, force: true })
  }
})
