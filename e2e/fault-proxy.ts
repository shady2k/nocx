// A network between the app and a fixture that can be made to misbehave on cue.
//
// This is the e2e twin of `internal/ssh/netfault_test.go`, and it exists as a
// second implementation for a reason worth stating rather than discovering.
// That one is a Go type in a `_test.go` file: `faultProxy` is unexported, it is
// compiled into no non-test binary, and `newFaultProxy` takes a `*testing.T`
// and registers `t.Cleanup`. There is no export, flag or build tag that would
// let a Playwright spec reach it — the spec is a different process in a
// different language. So the choice was to write this, or to add an exported
// relay package plus a control protocol on `cmd/e2e-sshd`; a fifty-line Node
// server beside the specs that use it is the smaller thing to own.
//
// The VOCABULARY is deliberately identical — pass, blackhole, slow, cut — so
// the two read as one concept in two places rather than as two ideas. If the
// Go one grows a mode, this is where the same mode belongs.
//
// It forwards BYTES, not packets, so it stages the application's view: a write
// that returns success and a reply that never arrives. That is what an SSH
// client sees through a black-holed flow, and it is deliberately stronger than
// a suspended laptop's socket — the kernel's own TCP keepalive cannot rescue a
// client whose peer is still answering at the TCP layer. Everything the
// product must notice, it has to notice from the SSH layer alone.
//
// WHY NOT SIGSTOP THE FIXTURE, which needs no code at all: a stopped sshd
// stops answering on every connection AND stops accepting new ones, so the
// reconnect the spec exists to watch could not succeed. The whole journey
// needs the far side alive while one flow is dead, and only a per-flow fault
// gives that.
import net from 'node:net'

/** What the network is doing right now. */
export type FaultMode = 'pass' | 'blackhole' | 'slow'

export interface FaultProxy {
  /** The loopback port to point a profile at. */
  readonly port: number
  /** Forward both directions unchanged. */
  pass(): void
  /**
   * Keep every socket open and forward nothing.
   *
   * Reads continue and the bytes are discarded, so the writer's send never
   * blocks and never fails: the connection looks perfectly healthy from the
   * outside and is entirely deaf. This is the suspended laptop.
   */
  blackhole(): void
  /** Forward everything, late. A loaded host, not a lost one. */
  slow(delayMs: number): void
  /** Destroy every live socket. The LOUD loss, for contrast. */
  cut(): void
  /** Stop listening and drop everything. Idempotent. */
  close(): Promise<void>
}

/**
 * Start a relay in front of `upstream` (host, port) and return it in `pass`.
 *
 * The mode is read per chunk rather than captured per connection, so a switch
 * takes effect on flows that are already open — which is the whole point: the
 * spec changes the network under a live session.
 */
export async function startFaultProxy(
  upstreamHost: string,
  upstreamPort: number,
): Promise<FaultProxy> {
  let mode: FaultMode = 'pass'
  let delayMs = 0
  const sockets = new Set<net.Socket>()

  const track = (s: net.Socket) => {
    sockets.add(s)
    s.on('close', () => sockets.delete(s))
    // A relayed socket dying is ordinary — the peer closed, or `cut` did it.
    // Without a handler Node raises it to an unhandled 'error' and fails the
    // whole spec for something the spec asked for.
    s.on('error', () => void s.destroy())
  }

  /** One direction. Discards while black-holed; delays while slow. */
  const pump = (src: net.Socket, dst: net.Socket) => {
    src.on('data', (chunk: Buffer) => {
      if (mode === 'blackhole') return
      if (mode === 'slow' && delayMs > 0) {
        // LATENCY, not throttling, and the difference is the whole test.
        //
        // Pausing the source until each chunk's delay elapsed would serialize
        // them: N chunks would cost N x delayMs, so a keepalive reply queues
        // behind whatever the shell happened to be printing and blows the
        // prober's budget. That is not a slow host, it is a narrow pipe, and
        // it made "answering late" indistinguishable from "not answering at
        // all" — measured: the tab read `Connection lost` where the test
        // asserted `slow`.
        //
        // Every chunk is therefore delayed by the SAME constant and they fly
        // concurrently, which is what a long link does. Order is preserved
        // because equal timers fire in the order they were scheduled.
        setTimeout(() => {
          if (!dst.destroyed) dst.write(chunk)
        }, delayMs)
        return
      }
      if (!dst.destroyed) dst.write(chunk)
    })
    // Do NOT propagate end/close across the relay while black-holed: a flow
    // that is deaf must not also look closed, or the client learns of the loss
    // the loud way and the fault stops being the one being staged.
    src.on('close', () => {
      if (mode !== 'blackhole' && !dst.destroyed) dst.destroy()
    })
  }

  const server = net.createServer((client) => {
    track(client)
    const upstream = net.connect(upstreamPort, upstreamHost)
    track(upstream)
    pump(client, upstream)
    pump(upstream, client)
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })

  const address = server.address()
  if (address === null || typeof address === 'string') {
    throw new Error('fault proxy: listen did not yield a TCP address')
  }
  const port = address.port

  return {
    port,
    pass() {
      mode = 'pass'
      delayMs = 0
    },
    blackhole() {
      mode = 'blackhole'
    },
    slow(ms: number) {
      mode = 'slow'
      delayMs = ms
    },
    cut() {
      for (const s of sockets) s.destroy()
    },
    async close() {
      for (const s of sockets) s.destroy()
      sockets.clear()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}
