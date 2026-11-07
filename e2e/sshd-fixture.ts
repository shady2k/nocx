// The in-process SSH fixture, as a module rather than a fourth private copy.
//
// `cmd/e2e-sshd` is a REAL sshd: it authenticates, runs `bash` on a real PTY,
// serves the SFTP subsystem from the real filesystem — which is what makes it
// usable as the far side of an upload, the bytes nocx sends land in a
// directory this process can read back — and forwards TCP in both directions,
// so a request routed through a connection actually crosses it.
//
// Three specs already start it and each spells the same handshake out again
// (connection-password.spec.ts:112, shell-mode.spec.ts:32,
// nocxify-journey.spec.ts:84). None of the three EXPORTS its helper, so there
// was no existing answer to extend from a fourth spec; this file is where that
// answer can live, and consolidating the three onto it is a change to files
// this task does not own — recorded rather than done here.
//
// ## Two things this helper decides that the private copies do not
//
// **The remote HOME.** The local backend installs `~/.nocx` and its shell rc
// hooks into its own HOME on every start, so an sshd that shares that HOME
// spawns a shell that sources them and the connection comes up integrated by
// accident (shell-mode.spec.ts:44 pays for this). A caller states the remote
// home; nothing is inherited.
//
// **The remote CWD.** `startCommand` sets no `cmd.Dir`, so the shell inherits
// the SERVER PROCESS's working directory — which, spawned from Playwright, is
// the repo root. A tab's cwd is where a dropped file goes, so leaving that to
// inheritance means uploading into the checkout. The caller states it.
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync } from 'node:fs'
import { createConnection } from 'node:net'
import path from 'node:path'

import type { Page } from '@playwright/test'

export interface SshdFixture {
  proc: ChildProcess
  /** The host the server bound, from its own ADDR line. */
  host: string
  port: number
  /** Private key file for the user the fixture accepts. */
  userKey: string
  /** Synthetic account identifier; never copied from the invoking account. */
  user: string
  /**
   * The RSA user key the fixture minted and also accepts, when it was started
   * with `-rsa-key`, in the OpenSSH encoding.
   *
   * It exists for the credential shape whose public key TYPE is not the
   * algorithm a modern server negotiates (`ssh-rsa` is SHA-1, excluded from
   * `PubkeyAcceptedAlgorithms` since OpenSSH 8.8): a spec stores this path's
   * bytes in the vault and opens a pane with it (nocx-1ebrs.17).
   */
  rsaKey?: string
  /** A known_hosts line for the host key this spawn minted. */
  knownHosts: string
  /**
   * Every machine-readable line the server has printed so far, in arrival
   * order — the handshake lines above and everything after READY.
   *
   * It exists for `TCPIP=<host:port>`, which the server prints once it has
   * connected the far end of a `direct-tcpip` channel. A spec routing an HTTP
   * request through this fixture polls for that line: when both endpoints sit
   * on this machine's loopback, a request that went through the connection and
   * one that went around it look identical at the destination, so the server's
   * own account is the only thing that tells them apart.
   *
   * A live array, not a snapshot: it keeps filling as the server prints, so a
   * caller polls it rather than waiting a while and reading once.
   */
  lines(): readonly string[]
}

export interface StartSshdOptions {
  /** HOME (and the XDG roots under it) for the shells this server spawns. */
  home: string
  /** Working directory for the server, and therefore for the shells it
   *  spawns — the cwd a connected tab reports over OSC 7. */
  cwd: string
  /** Extra flags, e.g. `['-password', 'secret']`. */
  args?: string[]
}

/**
 * The fixture's SFTP link, shaped on purpose.
 *
 * A helper install is 15.3 MB and over loopback it is over before a spec can
 * look at it. The stand's SFTP server reads uploads at `bytesPerSecond` while
 * `until` does not exist, and goes to full speed the moment the spec creates
 * that file — so "the install takes as long as this assertion needs" is a
 * fixture state and not a sleep, and the same spec can then watch the same
 * install finish (nocx-3an1s.2.10).
 */
export interface SlowSftp {
  /** Path that must not exist for the pacing to apply. */
  until: string
  /** Upload (client to server) rate in bytes per second. */
  bytesPerSecond: number
}

/** The argv that shapes the fixture's SFTP link, or none. */
function sftpArgs(slow: SlowSftp | undefined): string[] {
  if (slow === undefined) return []
  return ['-sftp-slow-until', slow.until, '-sftp-upload-bps', String(slow.bytesPerSecond)]
}

/**
 * Build (once per process) and spawn the fixture; resolve when it is READY.
 *
 * The build output is keyed by pid so concurrent runs never write each other's
 * binary mid-exec, and reused within a run because the compile costs seconds.
 */
export function startSshd(opts: StartSshdOptions & { slowSftp?: SlowSftp }): Promise<SshdFixture> {
  const bin = path.resolve(process.env.TMPDIR ?? '/tmp', `nocx-e2e-sshd-${process.pid}`)
  if (!existsSync(bin)) {
    execFileSync('go', ['build', '-o', bin, './cmd/e2e-sshd'], {
      cwd: path.resolve(__dirname, '..'),
    })
  }
  const proc = spawn(bin, [...(opts.args ?? []), ...sftpArgs(opts.slowSftp)], {
    stdio: ['ignore', 'pipe', 'inherit'],
    cwd: opts.cwd,
    env: {
      ...process.env,
      HOME: opts.home,
      ZDOTDIR: opts.home,
      XDG_CONFIG_HOME: path.join(opts.home, '.config'),
      XDG_DATA_HOME: path.join(opts.home, '.local', 'share'),
      XDG_CACHE_HOME: path.join(opts.home, '.cache'),
    },
  })

  const { promise, resolve, reject } = Promise.withResolvers<SshdFixture>()
  let addr = ''
  let userKey = ''
  let rsaKey = ''
  let knownHosts = ''
  const lines: string[] = []
  // A line can split across pipe chunks; hold the partial tail so READY is
  // never missed (the same hazard nocxify-journey.spec.ts records).
  let remainder = ''
  const timer = setTimeout(
    () => reject(new Error(`e2e-sshd did not print READY: ${lines.join('|')}`)),
    30_000,
  )
  proc.stdout?.on('data', (chunk: Buffer) => {
    remainder += chunk.toString()
    const parts = remainder.split('\n')
    remainder = parts.pop() ?? ''
    for (const line of parts) {
      const trimmed = line.trim()
      if (!trimmed) continue
      lines.push(trimmed)
      if (trimmed.startsWith('ADDR=')) addr = trimmed.slice(5)
      if (trimmed.startsWith('USERKEY=')) userKey = trimmed.slice(8)
      if (trimmed.startsWith('RSAKEY=')) rsaKey = trimmed.slice(7)
      if (trimmed.startsWith('KNOWNHOSTS=')) knownHosts = trimmed.slice(11)
      if (trimmed === 'READY' && addr && userKey && knownHosts) {
        clearTimeout(timer)
        const [host, port] = addr.split(':')
        resolve({
          proc,
          host,
          port: Number(port),
          userKey,
          user: randomUUID().replaceAll('-', ''),
          // Only when the fixture minted one: undefined is "this host accepts
          // no RSA key", which is every caller that predates `-rsa-key`.
          rsaKey: rsaKey === '' ? undefined : rsaKey,
          knownHosts,
          lines: () => lines,
        })
      }
    }
  })
  proc.on('exit', (code) => {
    clearTimeout(timer)
    reject(new Error(`e2e-sshd exited early (${code}): ${lines.join('|')}`))
  })
  return promise
}

/** Prove the fixture still serves SSH immediately before a caller opens it.
 * READY proves startup, not that a later stage left the listener alive. Read
 * the server's banner without authenticating, so connection-count assertions
 * still count only the product's authenticated connections. */
export async function assertSshdReady(fixture: SshdFixture): Promise<void> {
  const { proc, host, port } = fixture
  if (proc.exitCode !== null || proc.signalCode !== null || proc.killed) {
    throw new Error(
      `e2e-sshd is not running: exit=${proc.exitCode}, signal=${proc.signalCode}, killed=${proc.killed}`,
    )
  }
  await new Promise<void>((resolve, reject) => {
    const socket = createConnection({ host, port })
    let banner = ''
    const finish = (error?: Error) => {
      proc.off('exit', exited)
      socket.removeAllListeners()
      socket.destroy()
      if (error) reject(error)
      else resolve()
    }
    const exited = (code: number | null, signal: NodeJS.Signals | null) =>
      finish(new Error(`e2e-sshd exited during readiness: exit=${code}, signal=${signal}`))
    proc.once('exit', exited)
    socket.setTimeout(10_000, () => finish(new Error('e2e-sshd did not send an SSH banner')))
    socket.on('error', (error) => finish(new Error(`e2e-sshd readiness failed: ${error.message}`)))
    socket.on('end', () => finish(new Error('e2e-sshd closed before its SSH banner')))
    socket.on('data', (chunk: Buffer) => {
      banner += chunk.toString()
      if (
        banner
          .split('\n')
          .slice(0, -1)
          .some((line) => line.startsWith('SSH-2.0-'))
      )
        finish()
    })
  })
}

/**
 * Call one JSON-RPC method over the real backend socket, from the page.
 *
 * The page is the only place in this suite with a WebSocket client, and the
 * socket is token-gated, so a spec that wants to seed state the way Settings
 * would goes through it. Same shape as shell-mode.spec.ts:113.
 */
export async function rpc<T>(
  page: Page,
  endpoint: { port: number; token: string },
  method: string,
  params: unknown,
): Promise<T> {
  return page.evaluate(
    ({ port, token, method, params }) =>
      new Promise<T>((resolve, reject) => {
        const ws = new WebSocket(`ws://127.0.0.1:${port}/session`, [`nocx.token.${token}`])
        const timer = setTimeout(() => reject(new Error(`rpc ${method} timed out`)), 10_000)
        ws.onopen = () => ws.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }))
        ws.onmessage = (ev: MessageEvent) => {
          const msg = JSON.parse(String(ev.data)) as { result?: T; error?: { message?: string } }
          clearTimeout(timer)
          ws.close()
          if (msg.error) reject(new Error(`${method}: ${msg.error.message ?? 'rpc error'}`))
          else resolve(msg.result as T)
        }
        ws.onerror = () => {
          clearTimeout(timer)
          reject(new Error(`${method}: websocket error`))
        }
      }),
    { port: endpoint.port, token: endpoint.token, method, params },
  )
}
