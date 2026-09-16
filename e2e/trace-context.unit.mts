import { test } from 'node:test'
import assert from 'node:assert/strict'

import { deriveSpanId, deriveTraceId, linesForTrace, traceparentFor } from './trace-context.mts'

test('deriveTraceId is deterministic for the same seed', () => {
  assert.equal(deriveTraceId('a-test-id'), deriveTraceId('a-test-id'))
})

test('deriveTraceId differs for different seeds', () => {
  assert.notEqual(deriveTraceId('test-one'), deriveTraceId('test-two'))
})

test('deriveTraceId is 32 lowercase hex characters, never the reserved zero', () => {
  for (const seed of ['x', '', 'a very long test title › with › nesting']) {
    const id = deriveTraceId(seed)
    assert.match(id, /^[0-9a-f]{32}$/)
    assert.notEqual(id, '0'.repeat(32))
  }
})

test('deriveSpanId is 16 lowercase hex characters and independent of the trace id', () => {
  const seed = 'shared-seed'
  const traceId = deriveTraceId(seed)
  const spanId = deriveSpanId(seed)
  assert.match(spanId, /^[0-9a-f]{16}$/)
  assert.notEqual(spanId, traceId.slice(0, 16))
})

test('traceparentFor produces a valid, well-formed W3C header', () => {
  const header = traceparentFor('some-test-id')
  const parts = header.split('-')
  assert.equal(parts.length, 4)
  assert.equal(parts[0], '00')
  assert.match(parts[1], /^[0-9a-f]{32}$/)
  assert.match(parts[2], /^[0-9a-f]{16}$/)
  assert.equal(parts[3], '01')
})

test('linesForTrace keeps only lines carrying this trace_id', () => {
  const log = [
    'INFO  module=internal/transport "ws server started" trace_id=aaaa',
    'DEBUG module=internal/transport "jsonrpc dispatch" trace_id=bbbb method=transport.ping',
    'WARN  module=internal/transport "jsonrpc dispatch" trace_id=aaaa method=layout.read',
  ].join('\n')

  const kept = linesForTrace(log, 'aaaa')

  assert.equal(kept.length, 2)
  assert.ok(kept.every((l) => l.includes('trace_id=aaaa')))
})

test('linesForTrace does not match a trace id that is a substring of another', () => {
  const log = ['INFO trace_id=aaaa1111 line one', 'INFO trace_id=aaaa line two'].join('\n')

  const kept = linesForTrace(log, 'aaaa')

  assert.equal(kept.length, 1)
  assert.ok(kept[0].includes('line two'))
})

test('linesForTrace returns nothing for an empty trace id or an empty log', () => {
  assert.deepEqual(linesForTrace('trace_id=aaaa', ''), [])
  assert.deepEqual(linesForTrace('', 'aaaa'), [])
})
