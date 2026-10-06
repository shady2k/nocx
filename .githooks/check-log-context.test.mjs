import assert from 'node:assert/strict'
import test from 'node:test'
import { violationsInText } from './check-log-context.mjs'

test('metadata and error methods are not logging, but message calls remain gated', () => {
  const source = [
    'info, err := entry.Info()',
    'description := err.Error()',
    'logger.Info("missing context", "err", err.Error())',
    'logger.Warn(',
    '  "multiline message",',
    ')',
  ].join('\n')
  assert.deepEqual(violationsInText('fixture.go', source), [
    {
      file: 'fixture.go',
      line: 3,
      text: 'logger.Info("missing context", "err", err.Error())',
    },
    { file: 'fixture.go', line: 4, text: 'logger.Warn(' },
  ])
})

test('a context-derived logger still admits message calls alongside metadata reads', () => {
  const source = [
    'logger := log.From(ctx)',
    'info, err := entry.Info()',
    'logger.Info("metadata inspected", "name", info.Name())',
  ].join('\n')
  assert.deepEqual(violationsInText('fixture.go', source), [])
})
