// One owner for "the local port is taken, so try an allocated one". The Ports
// panel's row action and the palette's "Forward a port" make the same
// decision on the same wire cause, and both used to make it by matching the
// message text (`/address already in use|EADDRINUSE/i`). Prose is not a
// discriminator: a reworded message silently turns the retry off.

import { rpcRefusalCause } from './dispatcher'
import type { TunnelOpenResult } from './generated/tunnel.open'
import type { TunnelError } from './generated/tunnel.error'

/** Open a forward on `port`, and when the ONLY typed refusal says the local
 *  port is held, open the same forward again on a port the OS allocates (0).
 *  Exactly one retry: a second `address-in-use` is a real failure and reaches
 *  the caller, because retrying a port that is still taken cannot end.
 *
 *  The trigger is the wire's `address-in-use` cause
 *  (contracts/tunnel.error.schema.json), read through the same accessor the
 *  Git store uses for `git.error.reason` — never the message.
 *
 *  A caller already asking for port 0 has nothing left to fall back to, so
 *  there is no second attempt to make. */
export async function openForwardWithFallback(
  open: (port: number) => Promise<TunnelOpenResult>,
  port: number,
): Promise<TunnelOpenResult> {
  try {
    return await open(port)
  } catch (e) {
    if (port === 0) throw e
    if (rpcRefusalCause<TunnelError['reason']>(e) !== 'address-in-use') throw e
    return open(0)
  }
}
