// The credit handshake a test backend has to answer.
//
// Every socket double that drives the real Dispatcher now sees
// `transport.credit` as the connection's first frame (ADR-8eej3e66 §7). A
// double that recorded it as a call, or ignored it, would leave every ordinary
// call in the renderer's queue and make tests fail for a reason that has
// nothing to do with what they test. So the handshake is answered here, and
// kept OUT of the socket's recorded calls: a test asserting on the wire still
// reads the calls it means.

/** The advertisement the doubles answer with. Same shape and same kinds of
 *  values as the backend's (contracts/transport.credit.schema.json). */
export const TEST_CREDIT_ADVERTISEMENT = {
  window: 8,
  exempt: [
    'ack',
    'vault.unlockResolved',
    'connections.passwordResolved',
    'agent.runResolved',
    'host.resolved',
    'agent.laneInteractivity',
    'transport.ping',
    'rpc.cancel',
    'app.diagnostics',
  ],
  reserved: ['vault.unseal'],
}

/** The response a socket should deliver when `data` is the credit request,
 *  or null for every other frame. */
export function creditHandshakeAnswer(data: string): {
  jsonrpc: string
  id: number
  result: typeof TEST_CREDIT_ADVERTISEMENT
} | null {
  let frame: { id?: unknown; method?: unknown }
  try {
    frame = JSON.parse(data) as { id?: unknown; method?: unknown }
  } catch {
    return null
  }
  if (frame.method !== 'transport.credit' || typeof frame.id !== 'number') return null
  return { jsonrpc: '2.0', id: frame.id, result: TEST_CREDIT_ADVERTISEMENT }
}
