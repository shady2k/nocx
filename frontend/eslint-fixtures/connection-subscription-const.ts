// Negative fixture for nocx/no-connection-subscription (ADR-ba6xet1b,
// nocx-oalo1.3.5). Both subscriptions name a connection answer, and one of
// them does it through a const — a rule that reads literals at the call site
// cannot see it.
//
// The SAME source is linted inside src/connection/ by the fixture gate, where
// the owner is allowed to subscribe.

interface Dispatcher {
  subscribe(name: 'exit' | 'session.liveness', handler: () => void): void
}

const CONNECTION_EVENT = 'exit'

export function watch(dispatcher: Dispatcher): void {
  dispatcher.subscribe(CONNECTION_EVENT, () => {})
  dispatcher.subscribe('session.liveness', () => {})
}
