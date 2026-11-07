// Fixtures shared by the Files panel's tests.
import { RpcError } from '../dispatcher'

/**
 * The answer a call that never started carries
 * (contracts/operation.expired.schema.json): -32005 with the catalogue's
 * `not-started` reason. A surface treats it as "nothing changed, ask again",
 * and ADR-8eej3e66 stage 8 makes it the only such answer: the -32004 capacity
 * refusal it replaced no longer exists.
 */
export const NOT_STARTED = new RpcError('The call did not start', -32005, {
  reason: 'not-started',
})
