import { test } from 'node:test'
import assert from 'node:assert/strict'

import { formatFrame, redactFrame, type PendingCalls } from './redact-frame.mts'

test('redactFrame keeps method and id from a request', () => {
  const f = redactFrame('sent', JSON.stringify({ jsonrpc: '2.0', id: 3, method: 'layout.read' }))
  assert.deepEqual(f, { direction: 'sent', method: 'layout.read', id: 3 })
})

test('redactFrame drops params entirely, however sensitive', () => {
  const f = redactFrame(
    'sent',
    JSON.stringify({
      jsonrpc: '2.0',
      id: 1,
      method: 'vault.unseal',
      params: { means: 'passphrase', secret: 'super-secret-passphrase' },
    }),
  )
  assert.ok(f)
  assert.equal(JSON.stringify(f).includes('super-secret-passphrase'), false)
  assert.equal((f as unknown as Record<string, unknown>).params, undefined)
})

test('redactFrame drops result entirely', () => {
  const f = redactFrame(
    'received',
    JSON.stringify({ jsonrpc: '2.0', id: 1, result: { token: 'abc', apiKey: 'shh' } }),
  )
  assert.ok(f)
  assert.equal(JSON.stringify(f).includes('shh'), false)
  assert.equal((f as unknown as Record<string, unknown>).result, undefined)
})

test('redactFrame keeps only code and message from an error, dropping data', () => {
  const f = redactFrame(
    'received',
    JSON.stringify({
      jsonrpc: '2.0',
      id: 2,
      error: { code: -32004, message: 'Control plane busy', data: { reason: 'control-saturated' } },
    }),
  )
  assert.deepEqual(f, {
    direction: 'received',
    id: 2,
    error: { code: -32004, message: 'Control plane busy' },
  })
})

test('redactFrame keeps a null id distinct from an absent one', () => {
  const withNull = redactFrame('received', JSON.stringify({ jsonrpc: '2.0', id: null, error: {} }))
  assert.ok(withNull)
  assert.equal('id' in withNull, true)
  assert.equal(withNull.id, null)

  const notification = redactFrame(
    'sent',
    JSON.stringify({ jsonrpc: '2.0', method: 'some.notification' }),
  )
  assert.ok(notification)
  assert.equal('id' in notification, false)
})

test('redactFrame returns null for a non-JSON or non-object payload', () => {
  assert.equal(redactFrame('sent', 'not json'), null)
  assert.equal(redactFrame('sent', '"just a string"'), null)
  assert.equal(redactFrame('sent', '[1,2,3]'), null)
})

test('formatFrame renders direction, method, id and error compactly', () => {
  assert.equal(
    formatFrame({ direction: 'sent', method: 'layout.read', id: 1 }),
    '-> layout.read id=1',
  )
  assert.equal(
    formatFrame({ direction: 'received', id: 1, error: { code: -32601, message: 'nope' } }),
    '<- id=1 error={"code":-32601,"message":"nope"}',
  )
})

// nocx-atpw5.1.36: a session.intent refusal is a RESULT, not an error, so
// before this the failure block could not tell "the keys never reached the
// program" from "the runtime took them and wrote zero bytes" — the two
// questions every lost-keystroke report asks.
test('redactFrame names a response from the request that produced it', () => {
  const calls: PendingCalls = new Map()
  redactFrame('sent', JSON.stringify({ jsonrpc: '2.0', id: 7, method: 'session.intent' }), calls)
  const f = redactFrame(
    'received',
    JSON.stringify({ jsonrpc: '2.0', id: 7, result: { state: 'executed', bytesWritten: 6 } }),
    calls,
  )
  assert.deepEqual(f, {
    direction: 'received',
    method: 'session.intent',
    id: 7,
    intent: { state: 'executed', bytesWritten: 6 },
  })
  assert.equal(calls.has('7'), false)
})

test('redactFrame keeps a refusal cause and never the region it quotes', () => {
  const calls: PendingCalls = new Map()
  redactFrame('sent', JSON.stringify({ jsonrpc: '2.0', id: 9, method: 'session.intent' }), calls)
  const f = redactFrame(
    'received',
    JSON.stringify({
      jsonrpc: '2.0',
      id: 9,
      result: {
        state: 'refused',
        bytesWritten: 0,
        refusal: { cause: 'busy', regionNow: 'secret-typed-into-a-prompt' },
      },
    }),
    calls,
  )
  assert.ok(f)
  assert.equal(JSON.stringify(f).includes('secret-typed-into-a-prompt'), false)
  assert.deepEqual(f.intent, { state: 'refused', bytesWritten: 0, refusalCause: 'busy' })
})

test('redactFrame still drops every other result', () => {
  const calls: PendingCalls = new Map()
  redactFrame('sent', JSON.stringify({ jsonrpc: '2.0', id: 4, method: 'vault.unseal' }), calls)
  const f = redactFrame(
    'received',
    JSON.stringify({ jsonrpc: '2.0', id: 4, result: { token: 'abc', apiKey: 'shh' } }),
    calls,
  )
  assert.ok(f)
  assert.equal(f.intent, undefined)
  assert.equal(JSON.stringify(f).includes('shh'), false)
})

test('redactFrame leaves a response bare when its request was never seen', () => {
  const f = redactFrame(
    'received',
    JSON.stringify({ jsonrpc: '2.0', id: 11, result: { state: 'executed', bytesWritten: 6 } }),
  )
  assert.ok(f)
  assert.equal(f.method, undefined)
  assert.equal(f.intent, undefined)
})

test('formatFrame prints an intent outcome after id and error', () => {
  assert.equal(
    formatFrame({
      direction: 'received',
      method: 'session.intent',
      id: 50,
      intent: { state: 'refused', bytesWritten: 0, refusalCause: 'busy' },
    }),
    '<- session.intent id=50 intent={"state":"refused","bytesWritten":0,"refusalCause":"busy"}',
  )
})
