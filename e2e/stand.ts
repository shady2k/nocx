/**
 * The stand: the backend and the frontend the suite runs against, owned by
 * Playwright itself.
 *
 * # Why this exists
 *
 * The suite used to run two ways. `npx playwright test` started `wails dev`;
 * `e2e/headless-run.sh` started a headless backend plus vite and set NOCX_WS_PORT,
 * which switched the config to a second arrangement. Seven specs could only
 * run on the second, so a hand-written testIgnore list kept them off the first
 * and a separate CI job ran them — which is how seven files once failed on
 * their first line while the shards stayed green (nocx-azxe.2).
 *
 * Two arrangements is also what produced three separate defects in one day:
 * specs that answered "where is the home" or "where do documents live" for
 * themselves, and got it right on one path and wrong on the other. A known_hosts
 * written to the wrong home is a host key the backend never sees.
 *
 * So there is one stand and Playwright owns it. `npx playwright test` is the
 * whole command, on a developer's machine and in CI, and there is no second
 * entry point that can drift from it.
 *
 * # Why the backend is the shipped binary
 *
 * It is `cmd/nocx-server`, the coordinator the desktop app spawns, and not a
 * harness of its own. The harness it replaces was 51 lines of `app.New()` + `Start`
 * — the same two calls — so the suite and production were two similar things
 * and the suite proved the one nobody ships (design D11). It is gone.
 *
 * The visible consequence is that the port and the token are no longer
 * printed. The coordinator hands them out over its discovery socket and
 * nowhere else, because a token on stdout is what design §6 forbids. What the
 * stand waits for now is the socket PATH on the server's own readiness line,
 * and e2e/coordinator.mts does the handshake that follows.
 *
 * The keystore stance came with the move and stopped being a variable anybody
 * has to remember. A build without `-tags nocx_login_session` makes no claim
 * to a login session, so it takes the file provider and never probes the OS
 * keystore — no `NOCX_NO_SYSTEM_KEYSTORE` set on one launcher and missing on
 * the other (nocx-nhhr), and no keychain dialog on a macOS host (nocx-o4hg).
 * A spec that is ABOUT the OS keystore asks for the OTHER build by name; see
 * `serverLoginSession`.
 *
 * # Why a manifest file rather than environment variables
 *
 * The backend mints its token at startup, and a child process cannot put a
 * value back into its parent's environment — so `webServer.env` cannot carry
 * something that does not exist until after the server starts. The stand
 * publishes what it made to `.e2e/stand.json` and the harness reads it there.
 * One authoritative answer, written once, instead of a port and a token
 * travelling separately through three processes.
 *
 * The file is also the account of a run that has finished: what home it used,
 * what port it held, which binary it built.
 *
 * # What it does NOT do
 *
 * It does not start wails. The wails host has its own project and its own
 * fixture, because what that proves — the injected bindings, the assets wails
 * serves, a clean shutdown — is a different subject from what the suite is
 * about, and it needs the real window.go rather than a stub.
 */
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { awaitCoordinator } from './coordinator.mjs'
import { createHomeIsolation } from './home-isolation'

const repoRoot = path.resolve(__dirname, '..')

/**
 * SOMEBODY IS AT THIS MACHINE FOR THE WHOLE RUN, and this socket is what
 * says so.
 *
 * The vault seals when the last client detaches (design D9,
 * internal/vault/presence.go): a count of zero that stays zero for ten
 * seconds is read as the person having left, and the root key goes. That is
 * right for the product — the coordinator outlives the window on purpose, so
 * the alternative is a key sitting in a live process heap for days.
 *
 * It is wrong for a suite that keeps ONE backend for the whole run. Playwright
 * runs its projects in sequence, so every page of the chromium project closes
 * before the first page of the webkit project opens, and measured in this
 * stand that gap is six and a half minutes. The vault sealed 10 s into it, and
 * the second project then ran every one of its specs against a vault the first
 * project had set up and nobody had unlocked. Two failed that way and neither
 * says anything about the vault: connections-settings could not click the
 * Authentication tab, because an unlock sheet is broadcast to every attached
 * client and covered the form; snippets waited for "could not be resolved"
 * from a secret lookup that raised a dialog instead of answering.
 *
 * A person who leaves the app open does not get sealed on, and the suite is
 * that person. So the stand holds one control-plane connection from before the
 * first spec until after the last, and the count never reaches zero at a
 * project boundary. It sends nothing and reads nothing; being registered in
 * the connection set is its entire job (internal/transport/client_presence.go
 * counts len(conns)).
 *
 * WHAT THIS DOES NOT HIDE. Every spec about sealing raises a backend of its
 * own through VaultBackend — vault.spec.ts, vault-settings.spec.ts,
 * prompt-vault.spec.ts, vault-sealed-probe.spec.ts, history-persistence.spec.ts
 * — so D9 is still exercised end to end, against a backend where the departure
 * is the one the spec staged rather than one Playwright's scheduler happened
 * to create.
 */
let keepalive: WebSocket | null = null

/** Hold a client open against the stand's backend for the lifetime of the run.
 *  Throws rather than warns: a run whose keepalive silently failed to connect
 *  is the run this exists to prevent, and it would fail later, elsewhere, in
 *  two specs that mention neither the vault nor the socket. */
async function openKeepalive(port: number, token: string): Promise<WebSocket> {
  const ws = new WebSocket(`ws://127.0.0.1:${port}/session`, `nocx.token.${token}`)
  await new Promise<void>((resolve, reject) => {
    const failed = (): void =>
      reject(new Error(`e2e: the stand's keepalive client was refused on port ${port}`))
    ws.addEventListener('open', () => resolve(), { once: true })
    ws.addEventListener('error', failed, { once: true })
    ws.addEventListener('close', failed, { once: true })
  })
  return ws
}

/** Where the run publishes what it built. Under the repo, git-ignored, and
 *  deliberately not a mkdtemp: a finished run leaves it behind to be read. */
export const MANIFEST = path.join(repoRoot, '.e2e', 'stand.json')

export interface StandManifest {
  /** The backend's WebSocket port, as the backend reported it. */
  port: number
  /** The token the backend minted for this run. */
  token: string
  /** The disposable HOME the backend resolved. */
  home: string
  /** Where vite is serving the frontend. */
  baseURL: string
  /** The nocx-server build this run made, for specs that start their own. */
  server: string
  /**
   * The same server compiled with `-tags nocx_login_session` — a build that
   * DECLARES it runs inside a login session, so the vault reaches the real
   * per-user keystore and the startup probe is allowed to run (design D10).
   *
   * One spec uses it, and only where a Secret Service is actually reachable:
   * the silent vault setup, whose whole subject is a machine whose OS key can
   * carry the vault without a passphrase sheet. Every other spec takes the
   * default build, which has no keystore to reach — which is the point, and
   * is why nothing has to remember to switch it off.
   */
  serverLoginSession: string
}

/** Read what the stand published. Throws with the reason rather than a
 *  TypeError on undefined, because "the stand is not up" and "the stand is up
 *  and the port is wrong" are different failures and only one is the caller's
 *  fault. */
export function readStand(): StandManifest {
  try {
    return JSON.parse(readFileSync(MANIFEST, 'utf8')) as StandManifest
  } catch (err) {
    throw new Error(
      [
        `e2e stand: no usable manifest at ${MANIFEST} (${(err as Error).message}).`,
        '',
        'The stand is started by playwright.config.ts globalSetup, so this means',
        'either the run did not start it or it failed on the way up. Its logs are',
        'kept under test-results/stand/.',
      ].join('\n'),
    )
  }
}

/**
 * Take the launching shell's `$SHELL` out of what the backend inherits.
 *
 * It no longer decides anything, and that is the point of still removing it.
 * Since nocx-wwz0 the backend asks the OS ACCOUNT DATABASE which shell this
 * user logs in with (`internal/loginshell`) and treats `$SHELL` as the
 * fallback for when that cannot be read — because a Dock-launched app inherits
 * launchd's environment, where `$SHELL` is absent or stale. So a stale claim
 * from whatever started the suite must not be the thing the fallback picks up
 * if the account lookup ever fails on a runner; stripping it keeps the fallback
 * honest instead of silently host-dependent.
 *
 * What this does NOT do any more is pin the shell. The suite drives the host's
 * real login shell — bash for the container's root, zsh on a stock Mac — which
 * is the whole reason nocx-wwz0 exists: what the product does on macOS is what
 * has to be tested on macOS. Both tiers emit the OSC 636 command snapshot as of
 * script v37 (nocx-qduc), so completion.spec.ts is a fair question to ask of
 * either.
 *
 * This is deliberately NOT in home-isolation's restricted list. That list is
 * the home boundary, and overriding one of its keys raises. `$SHELL` cannot
 * reach outside the boundary — whichever shell starts reads its rc files from
 * the disposable home — so this is determinism, not containment, and the two
 * should not share a mechanism that refuses.
 */
function withoutHostShell(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  return Object.fromEntries(Object.entries(env).filter(([key]) => key !== 'SHELL'))
}

let backend: ChildProcess | null = null
let vite: ChildProcess | null = null
let logDir = ''

function waitFor(what: string, probe: () => boolean, proc: ChildProcess, log: () => string) {
  return new Promise<void>((resolve, reject) => {
    const started = Date.now()
    const tick = () => {
      if (probe()) return resolve()
      if (proc.exitCode !== null) {
        return reject(new Error(`e2e stand: ${what} exited before it was ready:\n${log()}`))
      }
      if (Date.now() - started > 120_000) {
        return reject(new Error(`e2e stand: ${what} was not ready within 120s:\n${log()}`))
      }
      setTimeout(tick, 100)
    }
    tick()
  })
}

/** Bring the stand up and publish it. Idempotent per process. */
export async function startStand(): Promise<StandManifest> {
  const root = path.join(repoRoot, '.e2e')
  logDir = path.join(repoRoot, 'test-results', 'stand')
  mkdirSync(logDir, { recursive: true })

  // A FRESH home every run. The home used to survive from one run to the next,
  // so a spec's preconditions were whatever the last run happened to leave —
  // an installed-facts document, a saved profile, a vault. That is half of
  // nocx-8rda, and the half a run can fix for itself: a spec asserting "this
  // machine has never done X" is only meaningful against a home where nothing
  // has. What it does NOT fix is one spec's writes reaching the next spec
  // WITHIN a run; that needs a home per test and a backend to match.
  //
  // The directory is still under the repo rather than a mkdtemp, so a failure
  // can be inspected afterwards — it is removed on the way UP, not on the way
  // down, which keeps the evidence of the run that just failed.
  rmSync(path.join(root, 'home'), { recursive: true, force: true })

  // The boundary, from the one module that owns it — not a second hand-copied
  // list of variables to strip. It carries no keystore switch any more: the
  // stance is the BUILD's (design D10), so a server compiled without
  // `-tags nocx_login_session` has no OS keystore to reach and there is
  // nothing here to forget (nocx-nhhr, nocx-o4hg).
  const isolation = createHomeIsolation({
    inheritedEnv: withoutHostShell(process.env),
    root,
  })

  // The remote helper's artifacts, before anything that embeds them. The
  // deploy package pulls them in with //go:embed all:artifacts, so the server
  // must be compiled AFTER this or it carries an empty artifacts directory and
  // Artifact answers ErrArtifactsNotBuilt — which the git panel renders,
  // correctly and uselessly for a test, as "this platform can't run the nocx
  // helper".
  //
  // Here rather than in a make target or a CI step, because those are two
  // places and this is one. `make ci-e2e` had `helpers` as a prerequisite
  // while ci.yml's ci-e2e job runs e2e/run-in-container.sh straight after a
  // bare checkout, and the artifacts are gitignored — so the developer running
  // the make target saw green and CI would have seen red at the same commit,
  // with two SSH git specs waiting out their timeouts on a consent card that
  // could never render. The suite needs them, so the suite builds them
  // (nocx-eoijp).
  //
  // `make helpers`, not a second copy of the build matrix: HELPER_TARGETS and
  // the CGO_ENABLED=0 static recipe have one owner, and it is the Makefile.
  execFileSync('make', ['helpers'], { cwd: repoRoot, stdio: 'inherit' })

  // Built, not `go run`: go run wraps the binary in a child that survives a
  // kill of the parent, and an orphaned backend holds its profile's discovery
  // lock against the next run.
  //
  // TWO BUILDS, one tag apart. The default is what every spec drives and what
  // a release ships to a host with no login session; the second DECLARES one,
  // and exists for the single spec whose subject is the OS keystore (see
  // StandManifest.serverLoginSession). Both are built here rather than
  // wherever they are first needed, for the reason `make helpers` is: a build
  // that happens in a spec is a build CI does not do until that spec runs, and
  // the failure then lands in whichever file drew the short straw.
  const server = path.join(root, 'nocx-server')
  execFileSync('go', ['build', '-o', server, './cmd/nocx-server'], {
    cwd: repoRoot,
    stdio: 'inherit',
  })
  const serverLoginSession = path.join(root, 'nocx-server-login-session')
  execFileSync(
    'go',
    ['build', '-tags', 'nocx_login_session', '-o', serverLoginSession, './cmd/nocx-server'],
    { cwd: repoRoot, stdio: 'inherit' },
  )

  const webPort = Number(process.env.NOCX_WEB_PORT ?? 5173)
  const baseURL = `http://127.0.0.1:${webPort}`

  let backendLog = ''
  // NO ARGUMENTS AND NO ADDRESS. nocx-server binds loopback on a port the OS
  // picks and takes no flags at all, which is what keeps a token off argv
  // (design §6); where it landed is the discovery socket's to say. The stand
  // no longer pins a port, and no longer needs to — nothing in the suite
  // reaches the backend except through this manifest.
  backend = spawn(server, [], {
    cwd: repoRoot,
    env: isolation.env,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  backend.stdout?.on('data', (b: Buffer) => (backendLog += b.toString()))
  backend.stderr?.on('data', (b: Buffer) => (backendLog += b.toString()))

  const endpoint = await awaitCoordinator({
    readLog: () => backendLog,
    alive: () => backend !== null && backend.exitCode === null,
    what: 'nocx-server',
    timeoutMs: 120_000,
  })
  const { port, token } = endpoint

  let viteLog = ''
  vite = spawn('npx', ['vite', '--host', '127.0.0.1', '--port', String(webPort), '--strictPort'], {
    cwd: path.join(repoRoot, 'frontend'),
    env: process.env,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  vite.stdout?.on('data', (b: Buffer) => (viteLog += b.toString()))
  vite.stderr?.on('data', (b: Buffer) => (viteLog += b.toString()))
  await waitFor(
    'vite',
    () => /ready in|Local:/i.test(viteLog),
    vite,
    () => viteLog,
  )

  const manifest: StandManifest = {
    port,
    token,
    home: isolation.isolatedHome,
    baseURL,
    server,
    serverLoginSession,
  }
  // Written through a temp name: a reader that catches the file half-written
  // gets a parse error blamed on the stand rather than on itself.
  const tmp = `${MANIFEST}.tmp`
  writeFileSync(tmp, JSON.stringify(manifest, null, 2))
  renameSync(tmp, MANIFEST)

  // After the manifest, so a keepalive that is refused fails a stand that has
  // already published what it built — the log and the port are readable while
  // the failure is being read.
  keepalive = await openKeepalive(port, token)

  const flush = () => {
    writeFileSync(path.join(logDir, 'backend.log'), backendLog)
    writeFileSync(path.join(logDir, 'vite.log'), viteLog)
  }
  flush()
  standFlush = flush

  return manifest
}

let standFlush: (() => void) | null = null

/** Take the stand down and keep its account. */
export async function stopStand(): Promise<void> {
  standFlush?.()
  // Before the backend is signalled: closing it after would be a detach
  // reported by a transport that is already going away.
  keepalive?.close()
  keepalive = null
  for (const proc of [vite, backend]) {
    if (proc === null || proc.exitCode !== null) continue
    proc.kill('SIGTERM')
  }
  // Wait for them to actually go before anything removes the directory they
  // are writing into: on macOS the shell integration is still flushing into
  // $HOME as the process dies, and the race reports "Directory not empty".
  await Promise.all(
    [vite, backend].map(
      (proc) =>
        new Promise<void>((resolve) => {
          if (proc === null || proc.exitCode !== null) return resolve()
          const hard = setTimeout(() => proc.kill('SIGKILL'), 10_000)
          proc.on('exit', () => {
            clearTimeout(hard)
            resolve()
          })
        }),
    ),
  )
  vite = null
  backend = null
  rmSync(MANIFEST, { force: true })
}
