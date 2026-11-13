// Negative fixture for nocx/no-connection-subscription (ADR-ba6xet1b,
// nocx-oalo1.3.5). Both subscriptions name a connection answer, and one of
// them does it through a const — a rule that reads literals at the call site
// cannot see it.
//
// The call is made on the REAL dispatcher: the rule identifies it by the
// signature it resolves to, so a fixture with its own `subscribe` interface
// would prove nothing about the surface the renderer actually uses.
//
// The SAME source is linted inside src/connection/ by the fixture gate, where
// the owner is allowed to subscribe.

import { Dispatcher } from './dispatcher'

const CONNECTION_EVENT = 'exit'

export function watch(dispatcher: Dispatcher): void {
  dispatcher.subscribe(CONNECTION_EVENT, () => {})
  dispatcher.subscribe('session.liveness', () => {})
}
