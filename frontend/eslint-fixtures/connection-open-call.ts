// Negative fixture for nocx/no-connection-open (ADR-ba6xet1b, nocx-oalo1.3.5).
// The SAME source is linted twice by src/connection-owner-gate.test.ts: at a
// path outside frontend/src/connection/ it must report, and at a path inside
// it must stay silent — that is the whole rule, in one file.
//
// The reference is made on the REAL ipc client type (`./ipc` here, `../ipc`
// from src/connection/). That is deliberate: the rule asks the checker which
// symbol the member declares, not what the member is called, so a fixture
// with its own `interface Client { openSSHSession() }` would prove nothing.
//
// Two spellings of the same reference: a method bound off the client (a
// `.bind`, which a call-name rule cannot see) and a destructuring declaration
// (which names the member without a call at all).

import type { WSClient } from './ipc'

export function reopen(client: WSClient): void {
  const launch = client.openSSHSession.bind(client)
  const { openSSHSessionByHost } = client
  void launch
  void openSSHSessionByHost
}
