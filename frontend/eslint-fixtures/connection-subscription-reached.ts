// Negative fixture for nocx/no-connection-subscription (ADR-ba6xet1b,
// nocx-oalo1.3.5). The call is found by the SIGNATURE it resolves to — the
// dispatcher's subscribe — so a destructured binding is the same call as
// `dispatcher.subscribe`. An event name must be a literal union: `'ex' + 'it'`
// and a `string` parameter are unknown, and an unknown name is refused because
// it may be `exit` or `session.liveness`.
//
// The SAME source is linted inside src/connection/, where the owner may
// subscribe.

import { Dispatcher } from './dispatcher'

export function watch(dispatcher: Dispatcher, dynamicName: string): void {
  const { subscribe } = dispatcher
  subscribe('exit', () => {})
  dispatcher.subscribe('ex' + 'it', () => {})
  dispatcher.subscribe('session.liveness', () => {})
  dispatcher.subscribe(dynamicName, () => {})
}
