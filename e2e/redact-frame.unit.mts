import { test } from 'node:test'
import assert from 'node:assert/strict'

import { formatFrame, redactFrame } from './redact-frame.mts'

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
