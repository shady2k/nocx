// Negative fixture for nocx/no-connection-open, computed-key half
// (ADR-ba6xet1b, nocx-oalo1.3.5; the entry moved to the connection owner in
// stage 5). An element access is decided by its KEY'S TYPE: a literal, a
// const and a substitution-free template name the same entry; a key typed
// `string` names any of them and is refused rather than skipped.
//
// The SAME source is linted inside src/connection/, where the owner may reach
// the client.

import type { ConnectionClient } from './connection/connection-client'

export function reconnect(client: ConnectionClient, dynamicKey: string): void {
  const literalKey = 'reconnectPane'
  client['openProfile']()
  client[literalKey]()
  client[dynamicKey]()
}
