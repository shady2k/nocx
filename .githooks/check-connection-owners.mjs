#!/usr/bin/env node
/**
 * Connection-owner ratchet — a NEW Go site outside the SSH transport that
 * decides a fact about an SSH connection is refused (nocx-oalo1.3.4).
 *
 * ADR-ba6xet1b says a fact about an SSH connection — its dial, its liveness,
 * its closing, its exit cause, its identity, which panes share it — has ONE
 * owner: the backend's connection manager behind the Carrier interface
 * (.internal/specs/2026-11-06-ssh-foundation-design.md §5.1, ADR-akhhb098).
 * The owner's decision of 2026-11-13 makes that a check rather than a review:
 * "there must be an architectural ban on second surfaces".
 *
 * # Identity, not spelling
 *
 * This check does not read lines. It asks `cmd/conncheck`, a small Go program
 * that loads the tree with golang.org/x/tools/go/packages and reports every
 * REFERENCE to a connection object, resolved by package path and name through
 * go/types. A reviewer's round found seven ways the previous line-regex
 * passed a second owner — an aliased `net` import, an aliased x/crypto import,
 * a SendRequest with any name, a `// "unix"` comment on the dial's line, a
 * helper package re-exporting `net.Dial`, an allowlisted file that also
 * dialled, and a baseline keyed by text — and each is now a fixture in
 * check-connection-owners.test.mjs that FAILS.
 *
 * WHAT IT REFUSES, by rule id (the baseline and the failure message name the
 * rule, so an entry says what it is):
 *
 *   xcrypto-ssh-import   a reference to an object of golang.org/x/crypto/ssh
 *                        or its agent/knownhosts subpackages outside the
 *                        transport, EXCEPT the key-format objects listed for
 *                        that file in ALLOWLIST below. The library IS the
 *                        transport; a second package reaching into it is a
 *                        second place that can dial, sign or probe. The
 *                        allowlist names OBJECTS, not files: a dial added to
 *                        an allowlisted file is still reported.
 *   net-dial             a reference to net.Dial, DialTimeout, DialUDP,
 *                        DialTCP, DialIP, DialUnix, the net.Dialer type, or a
 *                        dial method of it, where the network is not the
 *                        CONSTANT "unix" — the constant is evaluated by
 *                        go/types, so a comment saying unix exempts nothing.
 *                        A unix socket is not an SSH connection (the MCP
 *                        bridge's, the coordinator's, the helper endpoint's).
 *   exit-error-read      a reference to the ssh.ExitError TYPE outside the
 *                        transport, whatever the import is called: the exit
 *                        cause of a remote process, decided away from the
 *                        connection that carried it. Design §5.1.2 moves it
 *                        into the x/crypto carrier, which maps it to a neutral
 *                        cause.
 *   keepalive-request    a call of SendRequest on an *ssh.Client or an
 *                        ssh.Conn outside the transport, WHATEVER the request
 *                        name is: a second prober on a connection that already
 *                        has one — design §5.3 replaces the keepalive verdict
 *                        with the supervisor's.
 *   transport-re-export  inside a transport package, an exported package-level
 *                        var holding a net dial function, or an exported func
 *                        whose WHOLE body is a single `return <dial call>`: a
 *                        helper that hands a dial out makes every caller a
 *                        second owner without naming one. This is a SHAPE
 *                        check and it is deliberately not chased further (see
 *                        the limits below).
 *   helper-decider       a reference to the helper client's Dial, to the host
 *                        mode store's Lookup, to the machine consent store's
 *                        Lookup, or to git/helper's NewFactory outside
 *                        internal/farhelper: the two inputs and the one dial
 *                        that decide whether a far helper serves a connection,
 *                        and the Git factory that must be built on the client
 *                        that decision produced (ALX-641, ADR-727g63tr
 *                        decision 2, design §5.10 C23). The decision has one
 *                        owner, and a second reader of the inputs is a second
 *                        decider. The helper protocol's own local dial is
 *                        licensed BY NAME below (internal/helper/local's
 *                        openOnce), never by directory: a dial added beside it
 *                        in the same package is still a second decider.
 *   panel-host-path      a reference to ssh.NewFSConn, to proto.OpSamplePorts,
 *                        to a lease acquisition ((*client.Client).
 *                        AcquireProbeLease) or to a ProbeLease exec (Uname,
 *                        Home, SamplePorts, Completion, Inflate, CommandNames)
 *                        outside internal/farhelper: the roads by which a
 *                        panel reaches a host without the far helper (design
 *                        §5.10 C24). A panel takes the connection's helper
 *                        client from internal/farhelper; the helper protocol's
 *                        own packages are the road the far helper rides. The
 *                        acquisition is here because the execs usually travel
 *                        on the returned lease as an interface the analyser
 *                        cannot chase — the acquisition is the concrete seam.
 *
 * Scope: every tracked non-test .go file except the transport packages, which
 * are `internal/ssh/`, `internal/helper/sshsvc/` (which serves the carrier over
 * the helper protocol), `internal/helper/sshconn/` (the carrier) and
 * `internal/sshtest/` (the fault proxy, which speaks SSH because it must). The
 * supervisor, `internal/hostconn/`, is deliberately NOT among them: it is
 * carrier-neutral and may not name x/crypto at all. The tree is analysed TWICE — as the coordinator is
 * built, and with -tags nocx_local_ssh — because the helper's dial half exists
 * only in the second, and a file behind a build tag is still a second owner.
 *
 * WHAT IT CANNOT SEE, the same admission the sibling ratchets make:
 *
 *   - A dial behind an interface whose implementation lives elsewhere: the
 *     call is a method on an interface, and no static analyser can say what it
 *     becomes. The construction of the concrete dialer is reported instead.
 *   - A value carried into another package through a struct field, a method, a
 *     channel or a callback, and a re-export in any shape other than the two
 *     the rule names: an exported package-level var holding a dial function is
 *     reported, and an exported func whose body is a single `return
 *     <dial call>` is reported; a func that assigns first, returns through a
 *     helper, stores the dial in a struct and hands that out, or exports a
 *     method is NOT. That is the STATED LIMIT, not a bug to be closed by
 *     chasing shapes: the transport packages are the one owner of a
 *     connection, and what they export is reviewed with them — a second
 *     heuristic here would grow without bound and still be outrun by the next
 *     spelling.
 *   - A network named by a variable that is not a constant. A dial with a
 *     computed network string is reported, not exempted: the exemption is the
 *     constant "unix" and nothing else.
 *   - For C23/C24, an exec on a lease held through THIS repository's own
 *     interface: internal/app's probeCommands is an interface, so the adapter's
 *     `l.lease.SamplePorts` resolves to the interface's method, which is not
 *     the helper client's object. A panel that reaches a ProbeLease exec, a
 *     helper-client dial, a Git factory or a store read directly is refused.
 *     The acquisition that hands such an adapter the concrete lease IS reported
 *     (`(*client.Client).AcquireProbeLease`), which is what keeps the road
 *     visible instead of hiding behind the interface; what remains unreviewed
 *     is only the adapter's own translation, the same division the transport
 *     facts already make.
 *   - A tree that does not compile. A load error fails the check: a tree that
 *     cannot be analysed cannot be proven clean.
 *   - The renderer, which nocx-oalo1.3.5 owns and checks itself.
 *   - Comments and prose: nothing here reads a line of text, which is why
 *     `// net.Dialer never performs the lookup` is no longer a false finding.
 *
 * COST. The analysis is a type-checked load, so its first run after a cold
 * checkout pays for the module's export data (~50 s on the 4-core CI-class
 * VM this was developed on, with cgo) and every later run is ~3 s. The
 * pre-commit hook runs it after golangci-lint, whose build has already warmed
 * the same cache.
 *
 * Invocation: node .githooks/check-connection-owners.mjs   (from the repo root)
 * Regenerate: node .githooks/update-connection-owners-baseline.mjs
 */

import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
export const PROJECT_ROOT = resolve(__dirname, '..')
export const BASELINE_PATH = resolve(__dirname, 'connection-owners-baseline.json')

/**
 * The SSH transport: the only packages allowed to speak x/crypto at all.
 *
 * `internal/helper/sshconn` is the carrier — the one implementation of
 * hostconn.Carrier — and `internal/helper/sshsvc` serves it over the helper
 * protocol. They are here because they ARE the one owner the ban names: a
 * package that implements the transport's own seam decides a connection fact by
 * definition, and refusing it for doing its job would leave the owner outside
 * its own allowlist.
 *
 * `internal/hostconn` is NOT here, and stage 2 had added it: design §5.1.1 gives
 * the supervisor `monoclock` and `log` and forbids it `internal/ssh` and
 * `x/crypto`, because it is carrier-neutral — the seam exists so that the next
 * transport is a package and one line at the composition root. An allowlist that
 * held the y would license exactly the second transport the seam is for, and the
 * review refused the set for widening where the requirement was to narrow
 * (nocx-oalo1.4 review, finding 13).
 */
export const ALLOWED_DIRS = [
  'internal/ssh/',
  'internal/helper/sshsvc/',
  'internal/helper/sshconn/',
  'internal/sshtest/',
]

/**
 * Sites that are not an SSH connection and never become one, each with its
 * reason. `objects` names the objects ONE file may use — the check reads what
 * those files actually reference, so an entry is exact and a new object in the
 * same file is a new finding. `func`, when written, narrows the licence to one
 * enclosing function: an entry that allowed a file would also licence a second
 * caller in it, and the point of an exemption is to name the caller that is
 * not a second owner.
 */
export const ALLOWLIST = [
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/transport/ws.go',
    objects: [
      'golang.org/x/crypto/ssh.ParseRawPrivateKey',
      'golang.org/x/crypto/ssh.NewSignerFromKey',
      'golang.org/x/crypto/ssh.FingerprintSHA256',
      'golang.org/x/crypto/ssh.PassphraseMissingError',
    ],
    reason:
      'reads a vault key as a KEY FORMAT for the secrets panel (ParseRawPrivateKey, ' +
      'NewSignerFromKey, FingerprintSHA256): it opens no connection',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/transport/ws_secrets.go',
    objects: ['golang.org/x/crypto/ssh.ParsePrivateKeyWithPassphrase'],
    reason:
      'checks a stored key with a passphrase (ParsePrivateKeyWithPassphrase): a key ' +
      'format, not a transport',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'internal/app/helper_reverse.go',
    objects: [
      'golang.org/x/crypto/ssh.ParsePublicKey',
      'golang.org/x/crypto/ssh.PublicKey',
      'golang.org/x/crypto/ssh.FingerprintSHA256',
    ],
    reason:
      'parses the helper host key and prints its fingerprint for consent (ADR-0034), ' +
      'and dials nothing',
  },
  {
    rule: 'xcrypto-ssh-import',
    file: 'cmd/e2e-sshd/main.go',
    objects: [
      'golang.org/x/crypto/ssh.NewSignerFromKey',
      'golang.org/x/crypto/ssh.Signer',
      'golang.org/x/crypto/ssh.MarshalPrivateKey',
      'golang.org/x/crypto/ssh/knownhosts.Line',
      'golang.org/x/crypto/ssh.ServerConfig',
      'golang.org/x/crypto/ssh.NewServerConn',
      'golang.org/x/crypto/ssh.UnknownChannelType',
      'golang.org/x/crypto/ssh.ConnectionFailed',
      'golang.org/x/crypto/ssh.DiscardRequests',
      'golang.org/x/crypto/ssh.Conn',
      'golang.org/x/crypto/ssh.Channel',
      'golang.org/x/crypto/ssh.Request',
      'golang.org/x/crypto/ssh.NewChannel',
      'golang.org/x/crypto/ssh.ConnMetadata',
      'golang.org/x/crypto/ssh.Permissions',
      'golang.org/x/crypto/ssh.PublicKey',
      'golang.org/x/crypto/ssh.Marshal',
      'golang.org/x/crypto/ssh.Unmarshal',
    ],
    reason: 'the e2e stand SSH SERVER: the peer a test connects to, never a connection nocx holds',
  },
  {
    rule: 'net-dial',
    file: 'cmd/e2e-sshd/main.go',
    reason: 'the e2e stand SSH server dials a direct-tcpip destination for the test, as sshd does',
  },
  {
    rule: 'net-dial',
    file: 'internal/mcpstdio/mcpstdio.go',
    reason: 'the MCP bridge over a unix socket: its one dial passes the constant "unix"',
  },
  {
    rule: 'net-dial',
    file: 'internal/tunnel/remote.go',
    reason:
      'a remote forward relays an arriving connection to its destination ON THIS MACHINE ' +
      '(OpenSSH -R semantics), not to the SSH host',
  },
  {
    rule: 'net-dial',
    file: 'internal/update/download.go',
    reason: 'the updater downloads an artefact over HTTPS; no SSH connection is involved',
  },
  {
    rule: 'helper-decider',
    file: 'internal/app/hosted_shell_carrier.go',
    func: 'github.com/shady2k/nocx/internal/app.hostedShellCarrier.connectLocked',
    objects: ['github.com/shady2k/nocx/internal/helper/client.Dial'],
    reason:
      "a hosted shell's carrier stays in internal/app (architect, ALX-641 variant B): the " +
      'pane screen, its inventory and the session bookkeeping ride this one helper client, ' +
      'and the session closes it — no panel decides it. The entry names the FUNCTION, so a ' +
      'dial added to another function of the same file is still a second decider',
  },
  {
    rule: 'panel-host-path',
    file: 'internal/app/helper_probeops.go',
    func: 'github.com/shady2k/nocx/internal/app.helperLeaseAcquirer.AcquireProbeLease',
    objects: ['github.com/shady2k/nocx/internal/helper/client.(*Client).AcquireProbeLease'],
    reason:
      'the helper lease serves completion, command enumeration, platform probing and helper ' +
      'install/publication work beyond the Ports panel (nocx-50w7p.9); the composition root ' +
      'only wires these consumers, so its named seam is licensed while the Ports connector is ' +
      'guarded independently by the scheduler argument fact',
  },
  {
    rule: 'helper-decider',
    file: 'internal/helper/local/local.go',
    func: 'github.com/shady2k/nocx/internal/helper/local.openOnce',
    objects: ['github.com/shady2k/nocx/internal/helper/client.Dial'],
    reason:
      "the local helper's own reach for THIS machine's daemon over its unix socket " +
      '(openOnce): a local helper is not a far-helper decision, and the licence names the ' +
      'function so the protocol code beside it — a Git or pane road moved into ' +
      'internal/helper — is still a second decider',
  },
  {
    rule: 'helper-decider',
    file: 'internal/app/far_helper.go',
    func: 'github.com/shady2k/nocx/internal/app.farHelperDial.Dial',
    objects: ['github.com/shady2k/nocx/internal/helper/client.Dial'],
    reason:
      "the composition root's Dialer seam for internal/farhelper (design §5.10: internal/app " +
      'only wires): the axis decides and this adapter performs the dial it asked for, turning ' +
      'a resolved destination into a lane and a hello. It reads no mode and no consent, so it ' +
      'is a mechanism and not a decider (nocx-oalo1.6.2)',
  },
  {
    rule: 'helper-decider',
    file: 'internal/app/hosted_shell_carrier.go',
    func: 'github.com/shady2k/nocx/internal/app.hostModeFor',
    objects: ['github.com/shady2k/nocx/internal/hostmode.(*Store).Lookup'],
    reason:
      "the host's mode is read to STAMP an open (ADR-727g63tr decision 1: the mode is the " +
      "host's, and every reader of the config takes it from there) and to decide whether the " +
      'connect-time ask is due. Neither is a decision about whether a helper serves a live ' +
      'connection',
  },
  {
    rule: 'helper-decider',
    file: 'internal/app/hosted_shell_carrier.go',
    func: 'github.com/shady2k/nocx/internal/app.helperConsentAskForProbeFailure',
    objects: ['github.com/shady2k/nocx/internal/helper/consent.(*Store).Lookup'],
    reason:
      "the connect-time ask's condition (ADR-0068, ADR-0105), reached from a failed platform " +
      'probe: it reads whether the machine has already answered to decide whether to ASK, and ' +
      'ADR-727g63tr leaves the ask at connect, owned by the open path',
  },
  {
    rule: 'helper-decider',
    file: 'internal/app/consent.go',
    func: 'github.com/shady2k/nocx/internal/app.resolver.forMachine',
    objects: ['github.com/shady2k/nocx/internal/helper/consent.(*Store).Lookup'],
    reason:
      'the machine-keyed half of the connect-time ladder (ADR-0068, ADR-0105): at connect it ' +
      'answers whether to ask, and every other caller treats ConsentRequired as Refused. The ' +
      "ask is the open path's by the owner's decision; internal/farhelper reads the recorded " +
      'answer instead of consulting this ladder',
  },
  {
    rule: 'helper-decider',
    file: 'internal/transport/ws_host_mode.go',
    func: 'github.com/shady2k/nocx/internal/transport.hostModeHandlers.handleHostMode',
    objects: ['github.com/shady2k/nocx/internal/hostmode.(*Store).Lookup'],
    reason:
      'the connection editor reading the host record it also writes (ADR-727g63tr decision ' +
      '1): it answers which method the host keeps, and decides nothing about whether a ' +
      'helper serves a live connection',
  },
  {
    rule: 'helper-decider',
    file: 'internal/transport/ws_shell_footprint.go',
    func: 'github.com/shady2k/nocx/internal/transport.footprintHandlers.handleFootprintHelperUninstall',
    objects: ['github.com/shady2k/nocx/internal/helper/consent.(*Store).Lookup'],
    reason:
      'the footprint screen reads whether the machine had granted before it revokes, so a ' +
      'failed local cleanup can restore the answer (ADR-0034): an uninstall, not a decision ' +
      'about a connection',
  },
]

/** What each rule is for, printed beside a failure so the refusal explains itself. */
export const RULES = {
  'xcrypto-ssh-import': 'x/crypto/ssh is the transport, and belongs to the transport packages',
  'net-dial': 'dials a network outside the transport',
  'exit-error-read':
    "reads x/crypto's ExitError, so decides a remote exit away from its connection",
  'keepalive-request':
    'sends a keepalive outside the transport, where the connection has one prober',
  'transport-re-export':
    'hands a dial function out of a transport package, where every caller becomes an owner',
  'helper-decider': 'decides whether a far helper serves a connection away from internal/farhelper',
  'panel-host-path': 'reaches a host for a panel by a road other than the far helper',
}

/**
 * The directories each C23/C24 rule exempts, by rule.
 *
 * `internal/farhelper` is the one owner both rules name. `internal/helper` is
 * the helper protocol's own home and the binary that runs it — the op names in
 * internal/helper/proto and internal/helper/client are the wire vocabulary the
 * far helper rides, and internal/ssh is the transport doing its own SFTP.
 *
 * `helper-decider` does NOT exempt internal/helper as a directory. The only
 * lawful helper-client dial there is `internal/helper/local`'s own reach for
 * THIS machine's daemon, and it is licensed by NAME in ALLOWLIST
 * (`openOnce`). A directory exemption licensed every package under
 * internal/helper, so a Git or pane road moved there would have been dropped
 * after the analyser found it — the licence would have hidden a real fact.
 *
 * An exemption here is a statement about a DIRECTORY, so it is kept as small as
 * the protocol allows: a new fact outside these paths needs either
 * internal/farhelper or an allowlist entry with a reason.
 */
export const RULE_EXEMPT_DIRS = {
  'helper-decider': ['internal/farhelper/'],
  'panel-host-path': ['internal/farhelper/', 'internal/helper/', 'internal/ssh/'],
}

/** Whether `file` is one of the transport packages. */
export function isTransportFile(file) {
  return ALLOWED_DIRS.some((dir) => file.startsWith(dir))
}

export function isAllowlisted(rule, file, func, object) {
  return ALLOWLIST.some((entry) => {
    if (entry.rule !== rule || entry.file !== file) return false
    if (entry.func && entry.func !== func) return false
    if (!entry.objects) return true // a site-level exemption (the net-dial entries)
    return entry.objects.includes(object)
  })
}

// ─── the toolchain the analysis runs under ───────────────────────────────
//
// The analysis is a TYPE-CHECKED load, and the reader inside
// golang.org/x/tools understands export data only up to the Go release it was
// written for. On a host whose `go` is newer than that release (1.27 today) the
// load fails with "export data version N greater than maximum supported
// version M" — so a check that inherited the developer's `go` would be green or
// red depending on the machine, which is not a check at all.
//
// The toolchain is therefore PINNED BY THE MODULE and the host is irrelevant:
// `go.mod` names it in `toolchain`, one line, and a bump of the language
// version moves it with the same edit. The `go` directive alone is not enough
// to run under: it names a language version (`1.26`) and GOTOOLCHAIN refuses a
// version without a patch ("go1.26 is a language version but not a toolchain
// version"). A go.mod that names no toolchain, or one on another language line,
// is refused rather than guessed at.

/**
 * toolchainForGoMod answers the `toolchain` value a go.mod declares, refusing
 * one that is missing or that sits on a different language line than the
 * `go` directive.
 */
export function toolchainForGoMod(text) {
  const goLine = /^go\s+(\d+\.\d+(?:\.\d+)?)\s*$/m.exec(text)
  if (!goLine) throw new Error('go.mod names no `go` directive')
  const tcLine = /^toolchain\s+(\S+)\s*$/m.exec(text)
  if (!tcLine) {
    throw new Error(
      'go.mod names no `toolchain` line. The connection-owner analysis runs under the ' +
        "module's own toolchain, and `go 1.26` is a language version with no patch to run: " +
        'add `toolchain go1.26.x` beside it, so a host whose go is newer cannot change the answer.',
    )
  }
  const tc = tcLine[1]
  const tcMatch = /^go(\d+\.\d+)\.\d+$/.exec(tc)
  if (!tcMatch) {
    throw new Error(
      `go.mod's toolchain ${JSON.stringify(tc)} is not a go<major>.<minor>.<patch> version`,
    )
  }
  const goMinor = goLine[1].split('.').slice(0, 2).join('.')
  if (tcMatch[1] !== goMinor) {
    throw new Error(
      `go.mod's toolchain ${tc} is Go ${tcMatch[1]} but its go directive says ${goLine[1]}: ` +
        'the analysis would run under a language version the module does not declare',
    )
  }
  return tc
}

/** The toolchain this repository's analysis runs under. */
export const ANALYSIS_TOOLCHAIN = toolchainForGoMod(
  readFileSync(resolve(PROJECT_ROOT, 'go.mod'), 'utf8'),
)

/**
 * The environment every analyser invocation gets. `GOTOOLCHAIN` is the
 * module's, never the host's: that is the whole of the fix for a developer
 * whose go is newer than the one the analysis can read export data from.
 */
export function analysisEnv(env = process.env) {
  return { ...env, GOTOOLCHAIN: ANALYSIS_TOOLCHAIN }
}

/**
 * analysisGOFLAGS answers GOFLAGS for one analysis. The overlay is spelled
 * RELATIVE to the module root — the working directory of every go command the
 * analysis starts — because GOFLAGS is split on spaces before any go command
 * reads it. An absolute path in a clone under, say, `/home/dev/My Projects/nocx`
 * arrives as `-overlay=/home/dev/My` plus `Projects/nocx/…` as a `non-flag`
 * argument, and `go run` refuses before a fact is read (review #5, ALX-757).
 */
export function analysisGOFLAGS(module, overlay) {
  return `-overlay=${relative(module, overlay)}`
}

// ─── the analysed facts ──────────────────────────────────────────────────

/**
 * Run cmd/conncheck for one build-tag set and answer its facts. `opts.module`
 * and `opts.scope` exist for the tests: a fixture is a package inside the
 * module with its own root, so its file labels come out as the real product
 * paths and the policy applies unchanged. `opts.overlay` is for a fixture whose
 * package has to exist at a path it does not occupy on disk — the owner's own —
 * and reaches the analyser's `go list` through GOFLAGS, the one place a build
 * flag applies to every go command the analysis starts.
 */
export function analyze(opts = {}) {
  const module = opts.module ?? PROJECT_ROOT
  const scope = opts.scope ?? module
  const args = ['run', './cmd/conncheck', '-module', module, '-scope', scope]
  if (opts.tags) args.push('-tags', opts.tags)
  if (opts.pkgs) args.push('-pkgs', opts.pkgs)
  const env = analysisEnv()
  if (opts.overlay) env.GOFLAGS = analysisGOFLAGS(module, opts.overlay)
  let out
  try {
    out = execFileSync('go', args, {
      cwd: module,
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
      stdio: ['ignore', 'pipe', 'pipe'],
      env,
    })
  } catch (err) {
    const detail = (err.stderr ?? '').toString().trim() || err.message
    throw new Error(
      'the connection-owner analyser could not run (cmd/conncheck). It needs the Go ' +
        `toolchain the module is built with.\n${detail}`,
    )
  }
  return JSON.parse(out).facts ?? []
}

/** Facts from both build configurations, deduplicated by position and object. */
export function collectFacts() {
  const seen = new Map()
  for (const tags of ['', 'nocx_local_ssh']) {
    for (const f of analyze({ tags })) {
      const key = `${f.rule}:${f.file}:${f.line}:${f.column ?? 0}:${f.func}:${f.object}:${f.detail ?? ''}`
      if (!seen.has(key)) seen.set(key, f)
    }
  }
  return [...seen.values()]
}

/**
 * The policy: drop the transport's own use of the library (except a
 * re-export), and drop the exact allowlisted object sites. Everything else is
 * a violation.
 */
export function violationsFromFacts(facts) {
  const licensed = new Set()
  return (facts ?? []).filter((f) => {
    const exempt = RULE_EXEMPT_DIRS[f.rule]
    if (exempt) {
      if (exempt.some((dir) => f.file.startsWith(dir))) return false
      if (!isAllowlisted(f.rule, f.file, f.func, f.object)) return true
      if (f.rule !== 'helper-decider') return false
      const key = `${f.rule}:${f.file}:${f.func}:${f.object}`
      if (licensed.has(key)) return true
      licensed.add(key)
      return false
    }
    if (isTransportFile(f.file)) return f.rule === 'transport-re-export'
    return !isAllowlisted(f.rule, f.file, f.func, f.object)
  })
}

/** Stable key: rule + file + enclosing function + the object referenced. */
export function violationKey(v) {
  return `${v.rule}:${v.file}:${v.func}:${v.object}`
}

/**
 * Group facts into baseline rows: one row per key, with the number of
 * occurrences and the first line that carries one. A second identical site in
 * one function is a second occurrence, which the count makes visible — the
 * previous baseline was a set keyed by text and could not see it.
 */
export function aggregate(violations) {
  const rows = new Map()
  for (const v of violations) {
    const key = violationKey(v)
    const row = rows.get(key)
    if (row) {
      row.count += 1
      continue
    }
    rows.set(key, {
      rule: v.rule,
      file: v.file,
      func: v.func,
      object: v.object,
      count: 1,
      line: v.line,
      detail: v.detail ?? '',
    })
  }
  return [...rows.values()].sort((a, b) => violationKey(a).localeCompare(violationKey(b)))
}

/** Every violation across the scanned tree, aggregated and sorted. */
export function collectViolations() {
  return aggregate(violationsFromFacts(collectFacts()))
}

/** Load the committed baseline: {violations: [{rule,file,func,object,count,reason}]}. */
export function loadBaseline(path = BASELINE_PATH) {
  const data = JSON.parse(readFileSync(path, 'utf8'))
  return data.violations
}

/**
 * Split the current rows against the baseline:
 *   - `fresh`: a key the baseline does not hold, or one whose count GREW;
 *   - `stale`: a key whose count SHRANK, or one the tree no longer has.
 * Both fail the check. Growth is a new second owner; shrinkage or absence
 * means the baseline still licenses a site that is gone, and a licence nobody
 * uses is how a stale exemption hides the next real one.
 */
export function evaluate(rows, baseline) {
  const previous = new Map(baseline.map((entry) => [violationKey(entry), entry]))
  const current = new Map(rows.map((row) => [violationKey(row), row]))
  const fresh = []
  const stale = []
  for (const row of rows) {
    const before = previous.get(violationKey(row))
    if (!before) fresh.push(row)
    else if (row.count > before.count) fresh.push({ ...row, was: before.count })
  }
  for (const before of baseline) {
    const row = current.get(violationKey(before))
    if (!row) stale.push({ ...before, now: 0 })
    else if (row.count < before.count) stale.push({ ...before, now: row.count })
  }
  return { fresh, stale }
}

/** The keys a baseline no longer justifies (kept for the updater's callers). */
export function staleEntries(rows, baseline) {
  return evaluate(rows, baseline).stale
}

/** A baseline entry with no reason is an unexplained exception: it names a
 * site that decides a connection fact, and nobody wrote down why it may. */
export function unreasonedEntries(baseline) {
  return baseline.filter((entry) => typeof entry.reason !== 'string' || entry.reason.trim() === '')
}

/** The rules whose allowlist entries name objects, never a whole file. */
const OBJECT_RULES = new Set(['xcrypto-ssh-import', 'helper-decider', 'panel-host-path'])

/** An allowlist entry with no reason is a silent licence; refuse it. */
export function allowlistProblems() {
  const problems = []
  for (const entry of ALLOWLIST) {
    if (typeof entry.reason !== 'string' || entry.reason.trim() === '') {
      problems.push(`${entry.file} (${entry.rule}) is allowlisted with no reason`)
    }
    if (!Object.hasOwn(RULES, entry.rule)) {
      problems.push(`${entry.file} is allowlisted for an unknown rule ${entry.rule}`)
    }
    if (OBJECT_RULES.has(entry.rule) && !Array.isArray(entry.objects)) {
      problems.push(
        `${entry.file} allowlists ${entry.rule} with no object list: this rule is allowed ` +
          'object by object, so that a second reference added to the same file is still ' +
          'reported',
      )
    }
    if (entry.func !== undefined && (typeof entry.func !== 'string' || entry.func.trim() === '')) {
      problems.push(`${entry.file} allowlists an empty function name`)
    }
  }
  return problems
}

// ─── CLI entry point ─────────────────────────────────────────────────────
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const problems = allowlistProblems()
  if (problems.length > 0) {
    for (const p of problems) console.error(`allowlist: ${p}`)
    process.exit(1)
  }
  const baseline = loadBaseline()
  const unreasoned = unreasonedEntries(baseline)
  if (unreasoned.length > 0) {
    for (const entry of unreasoned) {
      console.error(`baseline: ${violationKey(entry)} — no reason written`)
    }
    console.error(
      '.githooks/connection-owners-baseline.json is a list of sites that decide a connection ' +
        'fact today; each entry says why it may still, and which stage removes it.',
    )
    process.exit(1)
  }
  let rows
  try {
    rows = collectViolations()
  } catch (err) {
    console.error(err.message)
    process.exit(2)
  }
  const { fresh, stale } = evaluate(rows, baseline)
  if (stale.length > 0) {
    console.error(
      `${stale.length} baselined site(s) no longer decide a connection fact, or decide it ` +
        'fewer times. A baseline entry that nothing uses is a stale exemption, and it must be ' +
        'dropped deliberately: node .githooks/update-connection-owners-baseline.mjs',
    )
    for (const entry of stale) {
      console.error(`  STALE ${violationKey(entry)}  (baseline ${entry.count}, now ${entry.now})`)
    }
  }
  if (fresh.length > 0) {
    console.error(
      `${fresh.length} new site(s) decide a connection fact outside its one owner. A fact ` +
        'about a connection has one owner — the backend connection manager behind the Carrier ' +
        `interface (${ALLOWED_DIRS.join(', ')} for a transport fact, ADR-ba6xet1b, design §5.1) ` +
        "— and so does the far helper's reachability and a panel's road to the host " +
        '(internal/farhelper, ADR-727g63tr, design §5.10 C23/C24). Take it through the owner, ' +
        'or add it deliberately: node .githooks/update-connection-owners-baseline.mjs --allow-growth',
    )
    for (const row of fresh) {
      const line = row.line === undefined ? '' : `:${row.line}`
      const grew = row.was === undefined ? '' : ` (baseline ${row.was}, now ${row.count})`
      console.error(`  NEW ${row.rule}: ${row.file}${line} ${row.func} → ${row.object}${grew}`)
    }
  }
  if (fresh.length > 0 || stale.length > 0) process.exit(1)
  console.log(
    `OK — connection-owner ratchet: ${baseline.length} baselined site(s) outside the transport, ` +
      '0 new, 0 stale',
  )
}
