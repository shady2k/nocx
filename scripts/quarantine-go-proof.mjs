// Manual real-Go proof. Run through nocx-sky-test, never on the shared VM.
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import assert from 'node:assert/strict'
import { goTestEvidence } from './quarantine.mjs'
const wrapper = new URL('./ci-go-test-exec.sh', import.meta.url).pathname
for (const mode of ['normal', 'abnormal', 'killed']) {
  const dir = mkdtempSync(join(tmpdir(), 'nocx-go-receipt-'))
  writeFileSync(join(dir, 'go.mod'), 'module receiptproof\n\ngo 1.25\n')
  const ending =
    mode === 'normal'
      ? 'os.Exit(status)'
      : mode === 'abnormal'
        ? 'os.Exit(2)'
        : 'syscall.Kill(os.Getpid(), syscall.SIGKILL)'
  writeFileSync(
    join(dir, 'proof_test.go'),
    `package receiptproof
import ("testing"; "os"; ${mode === 'killed' ? '"syscall";' : ''})
func TestA(t *testing.T) { t.Error("a named test failed") }
func TestMain(m *testing.M) { status := m.Run(); _ = status; ${ending} }
`,
  )
  const result = spawnSync('go', ['test', '-json', '-count=1', '-exec', wrapper, '.'], {
    cwd: dir,
    encoding: 'utf8',
  })
  assert.equal(result.status, 1, result.stdout + result.stderr)
  const evidence = goTestEvidence(result.stdout)
  assert.equal(evidence.complete, mode === 'normal', mode + '\n' + result.stdout + result.stderr)
  assert.deepEqual(evidence.failures, ['go:TestA'])
  console.log(mode + ': ' + JSON.stringify(evidence))
}
