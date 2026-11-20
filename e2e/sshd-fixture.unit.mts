import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import ts from 'typescript'

// The two harness pieces the SSH panel scenarios stand on that live beside the
// fixture (nocx-oalo1.6.3): the host identity a mode is written against, and
// the write itself.
//
// The module is loaded the way the other e2e unit tests load one — imports
// stripped, and the one global the tested functions need handed back. What is
// tested here is the shape on the wire, not Playwright.
Object.assign(globalThis, { createHash })
const source = readFileSync(new URL('./sshd-fixture.ts', import.meta.url), 'utf8')
const compiled = ts
  .transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ESNext },
  })
  .outputText.replace(/import .*? from .*?;\n/g, '')
const { hostKeyFingerprint, recordHostMethod } = await import(
  `data:text/javascript,${encodeURIComponent(compiled)}`
)

/** A page that answers the call it is given and remembers what it was asked —
 *  enough, because `recordHostMethod` speaks through `rpc`, whose only use of
 *  the page is `evaluate`. */
function recordingPage(): { page: unknown; calls: { method: string; params: unknown }[] } {
  const calls: { method: string; params: unknown }[] = []
  const page = {
    evaluate: (_fn: unknown, arg: { method: string; params: unknown }) => {
      calls.push({ method: arg.method, params: arg.params })
      return Promise.resolve({})
    },
  }
  return { page, calls }
}

test('the host fingerprint is the hash of the key blob, not of the algorithm name', () => {
  const blob = Buffer.from(Array.from({ length: 32 }, (_, i) => i))
  const line = `[127.0.0.1]:2222 ssh-ed25519 ${blob.toString('base64')}`
  const want = 'SHA256:' + createHash('sha256').update(blob).digest('base64').replace(/=+$/, '')
  assert.equal(hostKeyFingerprint(line), want)

  // The failure this pins is a real one: the algorithm sits in the same line
  // and hashing it produces a plausible-looking identifier no host has.
  const algorithm = Buffer.from('ssh-ed25519')
  const wrong =
    'SHA256:' + createHash('sha256').update(algorithm).digest('base64').replace(/=+$/, '')
  assert.notEqual(hostKeyFingerprint(line), wrong)
})

test('setting a host to raw writes the method against the host key', async () => {
  const { page, calls } = recordingPage()
  await recordHostMethod(page as never, { port: 4711, token: 'tok' }, 'SHA256:abc', 'raw')
  assert.deepEqual(calls, [
    {
      method: 'connections.setIntegrationMethod',
      params: { fingerprint: 'SHA256:abc', method: 'raw' },
    },
  ])
})
