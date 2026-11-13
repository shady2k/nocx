// Negative fixture for nocx/no-connection-open (ADR-ba6xet1b,
// nocx-oalo1.3.5): an element access is decided by its key's TYPE, not by the
// spelling the rule can read. `client[`openSSHSession`]` (a substitution-free
// template) and `client[BY_NAME]` (a const) name the entry; `client[key]` with
// a `string` key could name ANY entry, and an unknown access to the client is
// refused rather than waved through.
//
// The SAME source is linted inside src/connection/ by the fixture gate, where
// the owner is allowed the access.

import type { WSClient } from './ipc'

const BY_NAME = 'openSSHSession'

export function openByKey(client: WSClient, key: string): void {
  void client[`openSSHSession`]
  void client[BY_NAME]
  void client[key]
}
