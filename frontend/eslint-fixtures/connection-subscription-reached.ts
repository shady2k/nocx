// Negative fixture for nocx/no-connection-subscription (ADR-ba6xet1b,
// nocx-oalo1.3.5; narrowed to pane.connection by stage 5). The call is found by
// the SIGNATURE it resolves to — the dispatcher's subscribe — so a destructured
// binding is the same call as `dispatcher.subscribe`. An event name must be a
// literal union: `'pane.' + 'connection'` and a `string` parameter are unknown,
// and an unknown name is refused because it may be `pane.connection`.
//
// The SAME source is linted inside src/connection/, where the owner may
// subscribe.

import { Dispatcher } from './dispatcher'

export function watch(dispatcher: Dispatcher, dynamicName: string): void {
  const { subscribe } = dispatcher
  subscribe('pane.connection', () => {})
  dispatcher.subscribe('pane.' + 'connection', () => {})
  dispatcher.subscribe('pane.connection', () => {})
  dispatcher.subscribe(dynamicName, () => {})
}
