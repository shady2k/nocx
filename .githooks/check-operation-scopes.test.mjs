import assert from 'node:assert/strict'
import { test } from 'node:test'
import { collectViolations, partition } from './check-operation-scopes.mjs'

const FILE = 'internal/transport/ws_fixture.go'

test('a direct provider call in the handler layer is a violation', () => {
  const violations = collectViolations({
    [FILE]: `func (s *WSServer) handler() {
	_ = s.filesys.Close("binding")
}`,
  })
  assert.equal(violations.length, 1)
  assert.equal(violations[0].kind, 'provider-call')
})

test('a background context in request work is a violation', () => {
  const violations = collectViolations({
    [FILE]: `func (s *WSServer) handler() {
	ctx := context.Background()
	s.notifyRaiser.Raise(ctx, event)
}`,
  })
  assert.equal(violations.length, 1)
  assert.equal(violations[0].kind, 'unscoped-context')
})

test('an unchecked context detachment is a violation', () => {
  const violations = collectViolations({
    [FILE]: `func (s *WSServer) handler(ctx context.Context) {
	detached := context.WithoutCancel(ctx)
	_ = detached
}`,
  })
  assert.equal(violations.length, 1)
  assert.equal(violations[0].kind, 'unscoped-context')
})

test('a baselined line passes and corrected code drops out of the baseline', () => {
  const source = {
    [FILE]: `func (s *WSServer) handler() {
	_ = s.git.CloseSession("sid")
}`,
  }
  const violations = collectViolations(source)
  const baseline = [
    {
      file: FILE,
      text: '_ = s.git.CloseSession("sid")',
      kind: 'provider-call',
      reason: 'teardown after the session retired',
    },
  ]
  const { known, fresh } = partition(violations, baseline)
  assert.equal(known.length, 1)
  assert.equal(fresh.length, 0)

  const fixed = collectViolations({ [FILE]: 'func (s *WSServer) handler() {}' })
  assert.equal(partition(fixed, baseline).known.length, 0)
})

test('a provider call that is not in the baseline is fresh', () => {
  const { fresh } = partition(
    collectViolations({ [FILE]: 'func (s *WSServer) handler() { _ = s.vault.Status() }' }),
    [],
  )
  assert.equal(fresh.length, 1)
  assert.equal(fresh[0].kind, 'provider-call')
})

test('comments are not calls', () => {
  const violations = collectViolations({
    [FILE]: '// s.filesys.Close(bid) and context.Background() are named here, not called',
  })
  assert.equal(violations.length, 0)
})
