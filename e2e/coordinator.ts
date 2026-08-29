/**
 * The suite's side of the coordinator's discovery socket.
 *
 * # Why the suite needs this at all
 *
 * The e2e stand used to be `cmd/nocx-server`, which printed `WSPORT=` and
 * `WSTOKEN=` on stdout for a runner to grep. `nocx-server` does not, and will
 * not: a token on stdout is exactly what the coordinator's threat model
 * forbids (design §6 — not to disk, not to argv, not to a log line). The token
 * leaves that process through one route, a `0600` unix socket whose peer uid
 * has been checked, and so this is the only way a test runner can learn where
 * the backend is.
 *
 * That is not a cost the move imposed; it is the point of the move. The suite
 * now drives the SHIPPED binary through the SHIPPED handshake, so "the
 * discovery socket works" is asserted by every spec rather than by one.
 *
 * # One implementation, three callers
 *
 * e2e/stand.ts (the suite's backend), e2e/harness.ts (a spec that starts its
 * own) and scripts/dev-web.sh (the browser dev stand) all need the same two
 * facts from the same socket. A second copy of this exchange would agree with
 * the first until the day the protocol version moved.
 *
 * Runnable directly for the shell caller:
 *
 *   node e2e/coordinator.ts <socket-path>
 *
 * which prints `WSPORT=` and `WSTOKEN=` — the shape the shell already parsed,
 * now derived from the socket instead of from the backend's stdout. Node 24
 * strips the types; nothing is compiled first.
 */
import net from 'node:net'

/**
 * The discovery protocol version this client speaks. It MUST equal
 * `ProtocolVersion` in internal/coordinator/handshake.go.
 *
 * The daemon makes no decision on a client's stated version today, so a
 * mismatch here would not be refused — it would be accepted and mean the
 * wrong thing, which is why the constant carries the reference rather than
 * being written as a bare 1.
 */
const PROTOCOL_VERSION = 1

/** What the backend is and where it answers. */
export interface CoordinatorEndpoint {
  /** The loopback port the WebSocket is listening on. */
  port: number
  /** The capability that opens it, minted per launch. */
  token: string
  /** host:port as the daemon reported it, unparsed. */
  wsAddress: string
  /** The daemon's build version, for a runner that logs which backend it drove. */
  version: string
}

/**
 * Where a running `nocx-server` said its discovery socket is.
 *
 * READ OFF THE SERVER'S OWN STDERR rather than rebuilt from `$HOME`. The path
 * is `<data dir>/run/srv.sock` and the data dir is resolved by
 * internal/storage, which is platform-shaped (`~/Library/Application Support`
 * on darwin, `$XDG_DATA_HOME` or `~/.local/share` elsewhere) and carries the
 * `-dev` suffix from the build tag. e2e/harness.ts's own `documentDir` comment
 * records what the second derivation of that answer cost: a spec that wrote a
 * profile where nothing read it, green in the container and red on every Mac.
 * The server states the path in its readiness line; that is one owner.
 *
 * The line is `slog`'s text format, so a path containing a space arrives
 * quoted. Null until the line appears — a caller polls.
 */
export function socketPathFrom(serverLog: string): string | null {
  const quoted = /\bsocket="((?:[^"\\]|\\.)*)"/.exec(serverLog)
  if (quoted) return quoted[1].replace(/\\(.)/g, '$1')
  const bare = /\bsocket=(\S+)/.exec(serverLog)
  return bare ? bare[1] : null
}

/**
 * Ask the daemon on `socketPath` who it is and where its WebSocket is.
 *
 * One line out, one line back, then the connection is dropped — the daemon
 * serves requests until the peer hangs up, and a runner that held the socket
 * open would be a client the daemon counts for no reason.
 */
export function hello(socketPath: string, timeoutMs = 10_000): Promise<CoordinatorEndpoint> {
  return new Promise<CoordinatorEndpoint>((resolve, reject) => {
    const conn = net.connect({ path: socketPath })
    let buffered = ''
    let settled = false
    const finish = (err: Error | null, value?: CoordinatorEndpoint) => {
      if (settled) return
      settled = true
      conn.destroy()
      if (err) reject(err)
      else resolve(value!)
    }
    conn.setTimeout(timeoutMs, () =>
      finish(new Error(`e2e coordinator: ${socketPath} did not answer within ${timeoutMs}ms`)),
    )
    conn.on('error', (err) =>
      finish(new Error(`e2e coordinator: ${socketPath}: ${(err as Error).message}`)),
    )
    conn.on('connect', () => {
      conn.write(
        `${JSON.stringify({
          type: 'hello',
          client: { version: 'e2e', commit: '', protocol: PROTOCOL_VERSION },
        })}\n`,
      )
    })
    conn.on('data', (chunk: Buffer) => {
      buffered += chunk.toString('utf8')
      const nl = buffered.indexOf('\n')
      if (nl < 0) return
      let parsed: {
        hello?: { wsAddress?: string; wsToken?: string; build?: { version?: string } }
        error?: string
      }
      try {
        parsed = JSON.parse(buffered.slice(0, nl))
      } catch (err) {
        return finish(new Error(`e2e coordinator: unparseable answer: ${(err as Error).message}`))
      }
      // A refusal carries no payload, so the error is checked FIRST: a client
      // that read `hello` without looking would take a refused peer-uid check
      // for an empty answer and then dial a port of zero.
      if (parsed.error) return finish(new Error(`e2e coordinator: refused: ${parsed.error}`))
      const wsAddress = parsed.hello?.wsAddress
      const token = parsed.hello?.wsToken
      if (!wsAddress || !token) {
        return finish(new Error('e2e coordinator: the hello carried no address or no token'))
      }
      const port = Number(wsAddress.slice(wsAddress.lastIndexOf(':') + 1))
      if (!Number.isInteger(port) || port <= 0) {
        return finish(new Error(`e2e coordinator: unusable address ${wsAddress}`))
      }
      finish(null, { port, token, wsAddress, version: parsed.hello?.build?.version ?? '' })
    })
    conn.on('close', () =>
      finish(new Error(`e2e coordinator: ${socketPath} closed before it answered`)),
    )
  })
}

/**
 * Wait for a server that is still starting: poll the log it is writing until
 * it names its socket, then hello.
 *
 * READINESS IS "IT ANSWERED", not "the file exists" — the same rule the Go
 * launcher states. The daemon binds a temporary name and renames it into
 * place, so a socket that exists is listening, and the handshake is still the
 * only thing that proves the backend behind it came up.
 *
 * `alive` is what turns a dead server into its own error instead of a
 * timeout: a second `nocx-server` in one profile directory exits 3 rather than
 * serving, and that is the readiest failure in the whole arrangement.
 */
export async function awaitCoordinator(opts: {
  readLog: () => string
  alive: () => boolean
  what: string
  timeoutMs?: number
  pollMs?: number
}): Promise<CoordinatorEndpoint> {
  const timeoutMs = opts.timeoutMs ?? 60_000
  const pollMs = opts.pollMs ?? 100
  const deadline = Date.now() + timeoutMs
  let last = ''
  while (Date.now() < deadline) {
    if (!opts.alive()) {
      throw new Error(`e2e coordinator: ${opts.what} exited before it served:\n${opts.readLog()}`)
    }
    const socketPath = socketPathFrom(opts.readLog())
    if (socketPath) {
      try {
        return await hello(socketPath)
      } catch (err) {
        // The path is published before the first connection is accepted only
        // in the sense that the rename precedes accept(); a dial that lands
        // in that instant is retried rather than reported.
        last = (err as Error).message
      }
    }
    await new Promise<void>((r) => setTimeout(r, pollMs))
  }
  throw new Error(
    `e2e coordinator: ${opts.what} did not serve a discovery socket within ${timeoutMs}ms` +
      `${last ? ` (last: ${last})` : ''}:\n${opts.readLog()}`,
  )
}

// The shell entry point — see the module comment. `require.main` is undefined
// under Playwright's loader and defined when node runs this file directly, so
// importing it costs nothing.
if (typeof require !== 'undefined' && require.main === module) {
  const socketPath = process.argv[2]
  if (!socketPath) {
    console.error('usage: node e2e/coordinator.ts <socket-path>')
    process.exit(2)
  }
  hello(socketPath).then(
    (endpoint) => {
      process.stdout.write(`WSPORT=${endpoint.port}\nWSTOKEN=${endpoint.token}\n`)
    },
    (err: unknown) => {
      console.error(String(err))
      process.exit(1)
    },
  )
}
