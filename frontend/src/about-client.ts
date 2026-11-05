/**
 * about-client — the renderer's end of `app.about` (nocx-8bbp).
 *
 * What this build is: three fields the linker stamped and three the process
 * read. It is constant for the life of the backend, so this asks once and the
 * caller holds the answer; there is no change to subscribe to and nothing to
 * invalidate.
 *
 * The type is the generated one and nothing is re-declared here — a renderer
 * type that wants a field the wire does not carry cannot be written
 * (contracts/README.md).
 */

import type { Dispatcher } from './dispatcher'
import type { AppAbout } from './generated/app.about'
import type { AppDiagnostics } from './generated/app.diagnostics'

export type { AppAbout, AppDiagnostics }

/** Reads the build description. The rejection is deliberately not swallowed:
 *  the About page has a state for "could not read this", and a client that
 *  answered with a fabricated default would render six rows of nothing that
 *  look exactly like a build with no identity. */
export class AboutClient {
  constructor(private dispatcher: Dispatcher) {}

  load(): Promise<AppAbout> {
    return this.dispatcher.call<AppAbout>('app.about', {})
  }

  /**
   * loadDiagnostics reads the bounded in-memory record (nocx-3an1s.1.8): the
   * recent and pending opens with their stages, the recent refusals with their
   * holders, and the process facts. It is a separate call from load() on
   * purpose — the build description is constant and this is a snapshot of NOW,
   * so a page that cached both together would paste a stale answer. The
   * rejection is not swallowed: the About page copies the build facts anyway
   * and says the rest could not be read.
   */
  loadDiagnostics(): Promise<AppDiagnostics> {
    return this.dispatcher.call<AppDiagnostics>('app.diagnostics', {})
  }
}
