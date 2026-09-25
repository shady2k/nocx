// Ends the running block the way the product does (nocx-2v80t.3.33): the
// authenticated completion lands its status, and the backend's block.closed
// closes it on screen. The block manager has no other way to finish a
// command's block — the pane-decided freeze it used to offer tests had no
// production caller, so a test that went through it was testing a path
// nobody takes.

import type { BlockManager, BlockRecord, GetLineFn } from '../scrollback/blocks'
import { mintDomain, type IntegrationDomain } from '../lifecycle/domains'

const domain = mintDomain({
  lane: 'test-lane',
  lifecycle: 'prompt_ready',
  domain: 'test-domain',
  epoch: 1,
}) as IntegrationDomain

let minted = 0

/** Complete the running block with `exitCode` and deliver its block.closed.
 *  A block not yet bound to an attempt is bound to a fresh one first, as the
 *  authenticated start would have. `getLine` is the terminal buffer the close
 *  is handed — a test that asserts the block never reads it passes one with
 *  text in it. Answers the closed record, or null when nothing is running. */
export function closeRunningBlock(
  manager: BlockManager,
  exitCode = 0,
  getLine: GetLineFn = () => undefined,
): BlockRecord | null {
  const rec = manager.runningBlock
  if (rec === null) return null
  let id = rec.attemptId
  if (id === undefined) {
    id = `att-closed-${++minted}`
    manager.bindAttempt(id)
  }
  manager.freezeFromAttempt(
    { id, domain, state: 'completed', exitCode, fence: 'f'.repeat(64) },
    getLine,
    0,
  )
  manager.blockClosed(id)
  return manager.blockForAttempt(id)
}
