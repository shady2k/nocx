# The SSH foundation — design

- **Feature:** nocx-oalo1. **Task:** nocx-oalo1.2. **Decision record:** [ADR-akhhb098](../../docs/decisions/akhhb098-an-ssh-connection-has-one-owner-and-the-shell-comes-first.md) (proposed).
- **Status:** design, reviewed by another model (see the end). No code until the busy-terminal milestone has finished.
- **Read at:** `c89b9f1b9`. `[R]` is read in the source, `[H]` is a hypothesis with what settles it. Nothing here was run.

## 1. Frame (owner-confirmed, 2026-11-06)

**For whom and what for.** A developer who lives on remote servers: the terminal always works, and it is always clear what the connection is doing. Nobody goes to another terminal because of a nocx failure.

**When something must give, in this order:** the terminal works; security (no silent connect over a changed host key, no keys handed to a foreign host); honest state; the session survives; convenience.

**Main path.** Open a saved connection or type `ssh host`. See the stages. Land in blocks on a healthy host, or in a working raw shell on a struggling one. After a drop or a closed lid, come back by yourself.

**Success signal.** The scenarios in §3 pass in one end-to-end check, and the owner works through a day of loaded hosts and closed lids without opening another terminal.

**Owner's rules, not reopened here:**

- An SSH tab holds only an SSH session and stays on a dropped connection. A clean `exit` closes it, as today.
- A typed `ssh` stays in the same tab, as a block.
- Two things exist: **our transport**, and **the operating system's plain ssh**, which we try to wrap. If we cannot open a host through ours, the person can still open it the plain way.
- Connect first, integrate afterwards. The helper is never pushed at a struggling host.
- Open order is the delayed fallback (§5.2).
- Helper consent stays: one question at first connect with the helper preselected, no silent install.
- A dropped connection reconnects by itself, stops and explains on a changed host key or a rejected sign-in, and never re-runs a command.
- Status and Reconnect live in the head panel, beside Stop.

**Out (vision, on the bead):** connect card actions, notifications, connection panel, server health, DevTools-like timeline, `ssh -vvv` diagnostics, Duplicate, manual raw mode, plain-language error catalogue, nested ssh through the far helper, disconnected drafts, compose editor on a hotkey.

## 2. What exists today

### Runtime of a saved open `[R]`

```
renderer  open(profileId, pane)                       dispatcher.call: no timeout
coordinator  helper_git.go: probe platform over the local helper   (selection dial)
   consent asked here when unanswered (ADR-0068)
   ├─ far helper:   install → hold → lifecycle → tool socket → spawn (30 s) → attach (30 s) → adopt
   └─ otherwise:    local helper: hold → publish → lifecycle → spawn → adopt
```

A failed install, far spawn or far attach fails the whole open (`openFarHelper`, `helper_git.go:997`, `:1131`). A failed probe or publish, the 30 s setup and the 10 s hello only degrade integration. The person's shell is the **last** thing to exist.

### Who holds which state `[R]`

| Fact           | Holder today                                                                                                                                                        |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Open stages    | `internal/diagnostics`: bounded, in memory, for Copy diagnostics. Dial, handshake and sign-in are not split                                                         |
| Connection     | `internal/ssh` pool in the local helper. Keepalive armed once per connection, first armer's values win (`pool.go:276`)                                              |
| Reachability   | `session.liveness`: `alive \| unknown`, `roundTripMs`, `slow`; per session, fed by `registry.ObserveHost` from the local-helper route only (`helper_hosted.go:354`) |
| Session ended  | `exit` event, with cause                                                                                                                                            |
| Integration    | `lifecycle.changed` and `shellStateFromLifecycle` (`capability.ts`); reason on the integration axis (`ws_integration.go`)                                           |
| Renderer folds | `connection-condition.ts`: `reachable \| slow \| unreachable \| lost`; `_sessionLost`, `_liveness`, `_shellState`, attachment, `_reconnecting`, the auto loop       |
| Reconnect      | Renderer: `reconnect()` closes the handle and issues a new `open`. Always a new shell                                                                               |
| Typed `ssh`    | A process inside the local PTY: OpenSSH with `ControlMaster`, proven by mux (`typed_line.go`); overlay chosen in the renderer (`resolveSshProfileOverlay`)          |
| Wake           | `wake-source.ts`; only the diagnostic reporter and the update schedule consume it. No backend observer                                                              |

### What is wrong, against the frame `[R]`

- No "opening" state exists. A pane mid-open folds to `reachable`, which is the empty screen.
- `ClientConfig.Timeout` bounds TCP only. Handshake and sign-in are bound only by request cancellation, so slow is permitted there, but the open dies later at 30 s far-helper limits or the install's SFTP handshake (`fsHardTimeout`).
- A silent probe waits `interval × countMax` = 90 s and then closes at once (`ssh_keepalive.go:200-233`), because the parked `SendRequest` holds `globalSentMu`. A blackhole reads as silent only at the moment it is closed, up to about 2 minutes in.
- **Far-helper panes have no prober on their connection unless another holder armed it.** The lane acquires with `0, 0, nil` (`sshsvc/lane.go`), and `OnLiveness` is registered only on the local route. Read, not measured; the first harness test settles it.
- `_maybeAutoReconnect` has no caller outside its own retry, so `Automatically` does nothing (nocx-oalo1.1).
- The far helper keeps its shell (unclaimed TTL 24 h), but only coordinator start re-adopts it (`Readopt`). A network drop reads as lost.
- A file-based `sk-` key cannot parse on `x/crypto` (`keys.go`: unhandled key type) and discovery skips it at debug level. Agent-held should work and is unexercised `[H: delayed-agent fixture plus the owner's key]`.
- Our transport ignores ProxyCommand, ProxyJump from `ssh -G`, CertificateFile, PKCS11Provider and IdentityAgent.

### What already decided this ground

| Record                       | What it decided                                                                                            | Effect here                                                                                                              |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| AD-1                         | PTY bytes never in JSON-RPC                                                                                | Status uses JSON-RPC, bytes do not move                                                                                  |
| AD-4                         | `x/crypto`, ref-counted pool                                                                               | Supervisor grows from the pool                                                                                           |
| AD-6                         | `sessionruntime`'s emulator owns terminal state; terminal bytes create no lifecycle or execution authority | Plain ssh stays opaque; no connection state is derived from terminal bytes, so a hung shell is not diagnosed from them   |
| AD-7                         | One PTY or channel per tab; session id is server-authoritative                                             | Pane state keyed by pane id, because no session id exists while opening. **Amended by this feature's last stage** (§5.7) |
| AD-8                         | One owner per behaviour                                                                                    | One supervisor; lifecycle stays integration's owner (§6)                                                                 |
| AD-9                         | WebSocket drop replays                                                                                     | Not this design's loss                                                                                                   |
| ADR-0004                     | Fail open; no echo suppression, no termios inference                                                       | No editor on a raw shell; input never waits on integration                                                               |
| ADR-0015                     | `ssh -G` is the config oracle                                                                              | The resolver asks it, never parses config                                                                                |
| ADR-0025                     | Domain request fixed at host/user/port                                                                     | Typed options reach the resolver, never a composed line                                                                  |
| ADR-0034, ADR-0068, ADR-0105 | Consent keyed by host key, asked at connect; mode is not consent                                           | Consent stays a `waiting_person` stop                                                                                    |
| ADR-0049                     | Typed ssh keeps the user's OpenSSH process                                                                 | Superseded for lines our transport can honour; stands for plain ssh wrapping                                             |
| ADR-0069                     | Connect-time ask chooses a method the connection keeps                                                     | Kept                                                                                                                     |
| ADR-8eej3e66                 | Every operation has a declared lifetime                                                                    | §5.5                                                                                                                     |

## 3. Quality scenarios

Numbers are owner-confirmed. Each is a check in §10.

| #   | Stimulus and condition                                                                                       | Response                               | Measure                                                                        |
| --- | ------------------------------------------------------------------------------------------------------------ | -------------------------------------- | ------------------------------------------------------------------------------ |
| Q1  | Open any SSH tab                                                                                             | Stage visible                          | Under 1 s, no empty screen                                                     |
| Q2  | Host silent 5 minutes after TCP accept, then answers                                                         | Connects unaided, on that same attempt | No failure, no action                                                          |
| Q3  | Link lost during work                                                                                        | Reads as silent                        | Within 30 s                                                                    |
| Q4  | Lid opened                                                                                                   | Every tab reads correctly              | Within 5 s of the wake signal                                                  |
| Q5  | Host returns, including after a reboot behind a silently dead flow                                           | Reconnects unaided                     | Within a minute of its return, when sign-in needs no person; no command re-run |
| Q6  | Six tabs on one principal and route, connection lost                                                         | One sign-in, one vault prompt          | Counted at server and vault                                                    |
| Q7  | Integration broken in any way                                                                                | Raw terminal takes input               | In every fault scenario                                                        |
| Q8  | Host key changed at reconnect                                                                                | Stops and explains                     | Zero signatures sent                                                           |
| Q9  | One host with exactly one matching saved connection, by profile and by typed `ssh` with no explicit identity | One key                                | Asserted at the server                                                         |
| Q10 | Healthy host                                                                                                 | Blocks, no raw screen ever painted     | Asserted; repeat open stays near today's 1,436 ms at 50 ms RTT                 |

## 4. Options

**Shared assumption of A and B:** nocx holds the connection itself on `x/crypto`. C rejects it.

### A. Host connection supervisor, with panes as projections (chosen)

Patterns: **Supervisor** (Erlang/OTP) over the pool, **State** machine, Release It!'s **Timeouts** and **Circuit Breaker** semantics for retry, **Happy Eyeballs** (RFC 8305) for the open order. One owner per `poolKey` (host, port, user, identity, jump route) in the local helper; the coordinator projects it onto panes.

- **Steady:** one prober per connection, silent on a healthy link.
- **Growth:** tabs add projections, not attempts; ten times the tabs costs no more sign-ins.
- **Failure:** survives a coordinator restart because the helper holds it; a helper death reads as a loss with its own cause.
- **Security:** one place to stop on a changed key.
- **Operability:** one journal per connection.
- **Weak point:** a new long-lived block with a wide blast radius, and the helper protocol grows.

### B. Per-pane state machine in the coordinator, pool unchanged

Patterns: **State** per session, **Retry** with backoff in each pane. Smallest change: `internal/session` owns everything and the helper stays a dumb carrier.

- **Steady:** fine.
- **Failure:** six tabs make six attempts. Serialising them inside the pool's `AcquireDial` hides but does not remove six vault prompts and six stories about one machine, which fails Q6. Keepalive still belongs to a connection, so each pane holds a guess about a shared fact, which is the AD-8 shape this repository keeps paying for.
- **Rejected:** cheaper this week, and it fails Q6 by construction.

### C. System OpenSSH as the only transport, as kitty's ssh kitten does

Patterns: **Adapter** over a process, `ControlMaster`, `ServerAliveInterval`, askpass, an agent for vault keys. The strongest case: every config option, ProxyCommand and FIDO key work on day one; one mechanism; no reimplementation; no "plain fallback" concept at all.

- **Failure:** state comes from `ssh -v` text and `ssh -O check`, which AD-6 does not forbid but which is a parser of someone else's prose. No banner or stage stream without it; no authoritative host-key stop before a signature; the helper's session service would need a rewrite of its dial half (nocx-50w7p moved every connection there).
- **Security:** vault keys must be served through an agent to every SSH process, against AD-4's reason for existing.
- **Rejected:** supersedes AD-4 and undoes nocx-50w7p, and Q8's "zero signatures" could not be asserted by us. It survives as our plain-ssh path, which is the owner's framing.

### Stopping

Handing the outcome to work already filed (nocx-9le.7, nocx-mh6r, nocx-yrgxc): each fixes one symptom, and none creates the owner of the state, so the empty screen stays. Stopping here keeps the milestone's fixes and the present failure modes.

## 5. The design

### 5.1 Building blocks

```
┌ renderer ───────────────────────────────────────────────┐
│ connection-condition.ts: one fold, draws heads and mark │
└───────▲ pane.connection, lifecycle.changed, exit ───────┘
┌ coordinator ────────────┴───────────────────────────────┐
│ Connection resolver     profile | typed line → plan     │
│ Pane connection driver  plan → open order → adopt,      │
│                         reconnect, reattach             │
│ Journal store           per connection, bounded         │
└───────▲ connection events (helper protocol) ────────────┘
┌ local helper ───────────┴───────────────────────────────┐
│ Connection supervisor   per host+identity: dial, stages,│
│   (grows from the pool) prober, retries, wake, close    │
└─────────────────────────────────────────────────────────┘
```

| Block                  | Responsibility                                             | Owns                                     | Status                                                                                                                                                                       |
| ---------------------- | ---------------------------------------------------------- | ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Connection supervisor  | One `poolKey`'s connection life and transport state        | Transport state, journal events          | New package `internal/helper/sshconn`, over the `internal/ssh` pool as its connection table; replaces first-armer keepalive                                                  |
| Connection resolver    | One entry: answers `ours{config}` or `plain{reason}`       | Nothing                                  | Extends `internal/connection/resolver.go` and `ssh -G`; replaces `resolveSshProfileOverlay` and `reopenSSH`'s string match                                                   |
| Pane connection driver | Runs the open order, re-opens or reattaches after a return | Pane → connection binding, attempt state | New package `internal/panedriver`, with `openFarHelper`, the local opener and `session_readopt.go` adapted to it from `internal/app`; replaces the renderer's reconnect loop |
| Journal store          | Events per connection, bounded and in memory               | Journal                                  | Extends `internal/diagnostics`                                                                                                                                               |
| Status fold            | The one function surfaces draw from                        | Nothing                                  | Extends `connection-condition.ts`; `_liveness` and the auto loop go                                                                                                          |

A new block beside the pool, instead of methods on it: a pool entry today exists only while it has a connection, and a supervisor must exist while there is none (reconnecting). That is why the pool alone cannot serve. The pool remains the supervisor's connection table.

### 5.1.1 Packages, files and the direction of imports

Added after the review, on the owner's question. **Read against `go list -tags 'gtk3 nocx_local_ssh'` of 2026-11-06; nothing here has been compiled.** Go refuses an import cycle at build time, so the risk is not a cycle shipping but a design that cannot compile, or logic pushed somewhere odd to dodge one. Three such traps exist, and each is settled here:

| Where                                                                                                             | What goes there                                                                                                        | May import                                              | May never import                                              |
| ----------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------- | ------------------------------------------------------------- |
| `internal/helper/proto/connection.go`                                                                             | Wire events and state names between helper and coordinator                                                             | Nothing, as today                                       | Anything internal                                             |
| `internal/helper/sshconn/` (`machine.go`, `supervisor.go`, `prober.go`, `look.go`, tagged `nocx_local_ssh`)       | Pure state machine, one supervisor per `poolKey`, prober, look-only dial                                               | `internal/ssh`, `helper/proto`, `monoclock`, `log`      | `helper/sshsvc`, `helper/session`, `helper/host`, `transport` |
| `internal/helper/sshsvc/`                                                                                         | Serves `sshconn` over the helper protocol; shell and lane acquire through it                                           | `sshconn`, as well as what it imports today             | `helper/session` (which imports it)                           |
| `internal/connection/resolve.go`                                                                                  | One entry: `ours{config}` or `plain{reason}`                                                                           | As today: `credential`, `profile`, `ssh`, `vault`       | `panedriver`, `transport`, `app`                              |
| `internal/panedriver/` (`driver.go`, `order.go`, `return.go`)                                                     | Open order, reconnect, reattach, behind interfaces it declares itself                                                  | `connection`, `helper/proto`, `monoclock`, `log`        | `transport`, `app`, `session`, `helper/client`                |
| `internal/diagnostics/journal.go`                                                                                 | The journal, fed by the coordinator from proto events                                                                  | `log`, as today                                         | Anything else                                                 |
| `internal/transport/ws_pane_connection.go`, `contracts/pane.connection.schema.json`                               | The notification and the wake call. Replaces `ws_liveness.go`; the seam `transport` calls is an interface it declares  | As today                                                | `panedriver`, `app`                                           |
| `internal/app/pane_driver.go`                                                                                     | Composition only: adapts the far and local openers and readopt to `panedriver`, and `panedriver` to `transport`'s seam | Everything, and nothing but `cmd/` imports it (checked) | n/a                                                           |
| `frontend/src/connection/` (`connection-store.ts`, `connection-head.tsx`), `frontend/src/connection-condition.ts` | Subscription, the one fold, the head's text and button                                                                 | `ui/`, `generated/`, `ipc` types                        | `panes.ts`, `terminal-content.ts`                             |

**The three traps.**

1. **The driver cannot live in `internal/app`, as the reviewed text had it, and cannot import `transport`.** `app` is the composition root and `helper_git.go` is already 2,170 lines of open logic there; the driver would be untestable without the whole root. But its openers return `transport.HostedSessionOpen`, and `transport` calls the driver, so `panedriver` importing that type closes a cycle. So `panedriver` declares its own `Shell` and `Opener` interfaces, and `app/pane_driver.go` adapts in both directions (AD-8).
2. **The supervisor cannot ask who holds a shell.** The silence rule needs to know whether a connection carries a plain shell, and `helper/session`, which knows, imports `sshsvc`. Asking it would close a cycle. So each holder declares what it is when it acquires: `PooledSpec` gains a `Holds` field (plain shell or not), and the supervisor counts its own holders. The far helper's lane declares it holds no plain shell. `pool.go` gets no import of `sshconn`; it calls back through a function value, as `LivenessObserver` does today.
3. **The far helper must not link any of this.** `sshconn` carries the tag, and `TestDeployedHelperDependencyGraphHasNoSSHClient` already guards the graph; `sshconn` joins what it forbids. `ssh_keepalive.go` is untagged today and its prober moves out, so the check is worth rerunning at that step.

`frontend/src/terminal-content.ts` is 10,960 lines and is where the renderer's connection state lives now. The new code goes beside it and the old fields leave it; none is added to it beyond the calls.

**The frontend has no cycle check at all:** no `madge`, no `import/no-cycle`, no dependency rule in its lint or `package.json`. A cycle there does not fail the build, it fails at load order. So stage 2 adds one, scoped to `src/connection/` at first (C18).

### 5.2 Open order (owner's decision)

```
resolve → sign in (connection held by supervisor)
   └─ channel 1: integrated attempt
        (script integration, or the helper if already installed; never an install)
        ├─ proves itself within the pause → blocks; no raw screen ever painted
        └─ pause expires → channel 2: plain shell, shown; attempt continues
              ├─ nothing typed → switch quietly to blocks, close plain
              ├─ typed         → head says blocks are ready; a new shell only on request
              └─ attempt fails → raw stays, reason in one line
```

- **The pause starts at 3 s**, tuned from stage records. It begins when sign-in is done, so a person typing a password never spends it.
- **"Proves itself" is the accepted domain reaching `prompt_ready`**, past `HELLO` and `ACCEPT` (ADR-0024 §3). A `HELLO` followed by a failed acceptance leaves the fallback intact. Nothing reads the stream.
- **"Nothing typed"** means no byte went to that channel. A resize does not count. Any type-ahead released to the plain shell counts as typed.
- **The candidate holds no pane.** One visible shell at a time; the hidden channel is an attempt, not a session the renderer knows. During the race two channels briefly serve one tab, which AD-7 does not describe, so the AD-7 amendment (§5.7) names it.
- **Rebinding uses what exists:** the swap is `_bindSession('rebind')`, and scrollback carries over only from a shell that painted.
- **Second channel refused (MaxSessions):** a started channel cannot become a login shell, because the carrier chooses `Shell()` or `Start(command)` once (`sshsvc/shell.go:263`). So the candidate is closed, its release confirmed, and a plain channel opened in its place. If that is refused too, the transport reads `failed(no-channel)` with the server's refusal, and Reconnect retries. A candidate that has already proved itself is never closed for this.
- **The helper installs in the background** on a separate channel, only when `quality` is normal and consent is granted, as an Operations row (nocx-3an1s.2.10). It applies to the next session or to an explicit switch. A far helper already installed is one attempt candidate like any other, so a far spawn that hangs costs the pause and not the open.

This moves install **out of the open path**. The owner's reason: a struggling server must not receive a 15 MB upload before the person has a shell.

### 5.3 Transport state machine

```
           ┌──────────────── person answered ──────────────┐
           ▼                                               │
 ┌───► opening(stage) ── needs a person ──► waiting_person(kind)
 │        │     │                                 │ refused/changed key
 │     signed in└── refused/changed key ──► failed(cause) ◄┘
 │        ▼                                   ▲
 │    connected ◄── answers ── stalled(silentMs)
 │        │  └── silent ───────►  │
 │   loss │                       │ close decided
 │        ▼                       ▼
 └── reconnecting(attempt, nextIn) ◄──────┘
          │ policy ask/never
          ▼
      ended(cause)
```

- `opening.stage` is one of `resolve`, `tcp`, `ssh-version`, `key-exchange`, `sign-in`, `shell`. `x/crypto` has no stage callback; the boundaries come from a `net.Conn` wrapper (first bytes read), `HostKeyCallback` and the auth methods, so no fork is needed `[H: confirm during the first stage; a coarser stage list is the fallback]`.
- `waiting_person.kind` is one of `host-key`, `helper-consent`, `vault-unlock`, `passphrase`, `password`, `second-factor`, `key-signature`. **The no-progress clock stops here.** `key-signature` is an unanswered agent sign call, and it cannot be told from a slow agent, so the text says "waiting for the key's signature; it may need a touch".
- **The state may last; each ask is bounded.** ADR-8eej3e66's `answer` pauses `max` and has its own finite bound, so every prompt and sign call is a catalogued operation: 5 minutes for a prompt, 60 s for a sign call (a key's touch prompt does not outlast that in practice `[H]`). On expiry the ask settles and the state stays `waiting_person` with no ask outstanding. A new ask comes from the head's Reconnect, not from a timer, so an unattended tab does not raise prompts or fail sign-ins.
- `failed.cause` needs a person: `host-key-changed`, `auth-rejected`, `no-auth-method`, `plain-only`, `profile-gone`. Nothing retries from here. Reconnect is the way out.
- `reconnecting` retries with jittered backoff to a cap of 15 s of waiting between attempts, for as long as a pane holds the connection. The bead's earlier "capped at a minute" could not meet Q5, so the value is derived again from Q5's budget below. Only network-class failures retry. A retry that reaches sign-in with material that needs a person becomes `waiting_person`, not a loop, so an unattended tab waits rather than accumulating failed sign-ins.
- `ended` is the rest state under policy `ask` or `never`. `ssh.reconnect` keeps its three values; the default becomes `auto`, the three-attempt cap goes.

**Silence and closing** (replaces today's keepalive verdict):

- Probe interval default 15 s; each probe has a soft deadline of 15 s, after which the supervisor reports `stalled` while the call stays parked. Worst case 30 s, which is Q3. The old default was 30 s, so this doubles probes: one small packet per connection per 15 s.
- **Silence never ends a shell that would die with the connection. That is the person's choice (owner, 2026-11-06: "let us not decide for the user; let them choose to wait or to reconnect; checking in the background that a new connection works, we can of course do").** An unanswered probe closes nothing by itself. A transport error closes at once and reconnects unaided: nothing is left to lose. A connection on which every shell is helper-owned closes on the evidence below, or after `silenceCloseBudget` = 5 minutes of silence, chosen from the owner's "three minutes and then it connected", because reattaching loses nothing. A connection carrying any plain shell has no such budget: it stays `stalled` until it answers, the transport errors, or a person presses Reconnect. A profile's keepalive settings still apply and map onto these.
- **While `stalled`, the supervisor tries a look-only dial every 15 s:** TCP and the SSH version exchange on a new connection, no key exchange and no sign-in, on the same route. The answer is evidence for the person and for helper-owned shells, never a verdict on a plain one. If it is answered and the established connection stays silent for 30 s more: helper-owned, the supervisor closes and goes to `reconnecting`; carrying a plain shell, the head says that the server answers new connections while this session has been silent for this long, and offers Reconnect, saying it ends that shell. Waiting is doing nothing. `sshd` forks per connection, so a starved session process and a dead flow look alike from here, which is why the person decides. A loaded host that answers neither stays `stalled`.
- **Q5's budget, worst case, where nocx acts alone:** 15 s to the next look, 30 s grace, up to 15 s to sign in on a host that has just returned and is healthy. Sixty seconds, none spare, measured from the look being answerable, on a helper-owned connection. A connection already in `reconnecting` is faster: at most 15 s to the next attempt. A plain shell after a reboot usually needs no choice either: the parked probe's retransmission reaches a host holding no state for that flow, the reset is a transport error, and it reconnects unaided `[H: retransmission backoff decides how soon; the harness measures it]`. A flow lost in a firewall or NAT stays silent and asks. A look can be logged as a preauth close, so its 15 s cadence stops as soon as it is answered once and the evidence carries when it was answered; fail2ban's preauth filters are a named fault test.
- **On a wake signal** the supervisor sends one probe at once, unless one is already parked: a parked `SendRequest` holds `globalSentMu`, so probes never accumulate and the parked one is the evidence. Unanswered for 5 s: `stalled`, which is Q4, and it means missing evidence, not a loss. Unanswered for 30 s: a helper-owned connection closes and reconnects, because a socket that crossed a sleep is usually dead; a connection carrying a plain shell follows the rule above and asks.
- **sshd answers while the shell is hung:** nothing authoritative can see this for a plain channel, since terminal bytes create no authority (AD-6), and the design does not guess. The head stays `connected`, input is not blocked, and Stop and Reconnect work. On a helper-owned session the helper can say whether its process exists; that is the limit of the foundation.

### 5.4 After a return

The driver asks, in order:

1. **Far helper holds the shell** (inventory has this pane's session): reattach, using the `session_readopt.go` path. Same shell, no divider, gap named by the existing `hostWindow` hole. Revision-consistent snapshots under AD-9 come from that path as they do for a restart; painted scrollback alone proves nothing.
2. **Inventory unavailable:** keep trying and say so. Only **confirmed absence** justifies another shell, or a slow helper costs a person a second shell beside a live one.
3. **Confirmed absent, or no far helper:** the open order again, with a divider saying this is a new shell and that what the old one started may still be running.

`session_readopt.go:318` requires a `ProfileID` and refuses a route that changed. So a pane row persists its complete route (host, port, account, identity reference, jump route, profile id when there is one), and the path gains the same refusal for an unsaved route. A profile edited to another machine still refuses: that shell is reported as out of reach, not replaced silently.

Commands are never re-run. The drafted line in the editor stays and is not sent.

Reconnect is the head button:

- After a drop it acts at once.
- Over a live shell without a helper it asks first, because it ends that shell. On a shared connection the question names how many plain shells end with it, since one transport serves them all. When `stalled` has already said so on the head, the press is the answer.
- Over a live helper-owned shell it re-dials and reattaches without asking.

### 5.5 Lifetimes (ADR-8eej3e66)

`open` is declared at 300 s with the note "no explicit bound today", and a call that outlives it is an expired mutation. Waiting indefinitely on a person or a host cannot be one RPC.

- `open` for SSH **answers when the pane's connection is registered**, within seconds, with the binding. Progress arrives on `pane.connection`. The session id arrives by the existing adoption path when a shell exists. The catalogue row's write bound becomes a real one.
- Each supervisor attempt is a catalogued internal operation with its own bound: TCP 30 s (inherited from `ProbeTimeout`); handshake and sign-in `silence` 10 minutes, renewed by each stage boundary and paused by `answer`, so Q2's five silent minutes plus the rest of the handshake fit in one attempt; channel open 30 s (inherited from `farHelperAnswerTimeout`, but only ever costing an attempt).
- A silent attempt that reaches its bound is retried, not failed. A new attempt on a server that delays everyone restarts its queue, so the bound is long on purpose, and the head has been saying what is happening throughout.
- Cancelling is closing the tab, or Stop on the connection block's head.

### 5.6 Contract: `pane.connection`

`session.liveness` is retired in the same change as its renderer reader (clean-only). `exit` keeps ownership of "this session ended"; `lifecycle.changed` keeps integration. The schema has `additionalProperties: false`, like all of `contracts/`.

```jsonc
{
  "paneId": "…", // AD-7: no session id exists while opening
  "connectionId": "…", // stable per supervisor entry; several panes share one
  "connectionEpoch": 12, // monotonic per pane, minted at observation; late ones refused
  "instanceId": "…",
  "sessionId": "…", // absent until a shell is adopted
  "transport": {
    "state": "opening|waiting_person|connected|stalled|reconnecting|failed|ended|plain",
    "stage": "tcp", // opening only
    "person": "key-signature", // waiting_person only
    "cause": "host-key-changed", // failed, ended
    "attempt": 3, // reconnecting only
    "stateMs": 72000, // time in this state at observation; renderer ticks locally
    "lookAnsweredAt": "2026-11-06T12:00:00Z", // stalled only: a new connection answered
    "plainShells": 1, // stalled only: shells that end if this connection closes
  },
  "quality": { "slow": true, "roundTripMs": 1200 }, // as session.liveness carries them
  "attempt": {
    "shown": "none|integrated|plain",
    "candidate": "trying|ready|failed|none",
    "reason": "…",
  },
  "observedAt": "2026-11-06T12:00:00Z",
}
```

- `plain` means the system's ssh process: nocx sees only the process and its exit status. Code 255 is OpenSSH's own error, which covers sign-in and configuration as well as a lost connection, so it reads as "ssh ended with an error", never as a drop nocx did not observe.
- Pane rows hold `profileId`, and a restored pane binds by it. This retires the string match (nocx-yrgxc).

**Bent on purpose.** The agreed shape was "three axes in one notification". Integration stays on `lifecycle.changed`, because the authenticated kernel is its one owner (ADR-0024) and a second published copy is two derivations of one concept. The renderer's one fold reads both, so surfaces still have one source. `attempt` only describes the open order's candidates; it asserts nothing about whether a shell is integrated.

### 5.7 The resolver and typed `ssh`

Input is a `profileId` or a typed plan from `ssh-transition.ts` (which keeps its refusals). The output:

- `ours{config, profileId?}`
- `plain{reason}` when the line or `ssh -G` names what our transport cannot do: a typed option outside the honoured set, ProxyCommand, ProxyJump unresolvable to a profile, CertificateFile, PKCS11Provider, IdentityAgent, or a `sk-` identity file. The tab says so; nothing is silent.
- A failure on our transport never falls through automatically. A rejected password would become more failed sign-ins and a changed key would become someone else's prompt. **The head offers "Open with plain ssh" on `failed`**, the one action beyond Reconnect in the foundation, because the owner's rule is that a host must open somehow. OpenSSH applies its own known-hosts refusal, so this does not weaken a changed-key stop.

**Choosing the credential.** An explicit typed identity wins and yields no profile. Otherwise a destination matching exactly one saved connection by host, user and port takes its identity through the same resolver call a profile open makes. Several matches apply none: the tab says so and `ssh -G` decides, as today's overlay refuses to choose. Matching moves to the backend unchanged in meaning, and any later choice dialog is vision. Q9 holds for the one-match case.

**Typed in a local tab, on `ours`.** The line does not reach the local shell. The block's head shows `ssh host` and the pane binds a hosted remote session over the parked local one; `exit` rebinds the local session, which has been at its prompt throughout. This is new: **no concept of two sessions in one pane exists today** `[R]`. The lifecycle's domain stack stacks domains, not sessions. It is the feature's last stage, with its own prototype before it.

It amends AD-7 deliberately, in `docs/architecture.md` when that stage lands: a tab has one _active_ PTY or channel, and may hold parked ones beneath it. Input and resize go only to the active one. A parked session's output is recorded by its own runtime. Closing the tab ends every session it holds, top first. Each session keeps its own goroutine and id. The race in §5.2 is named by the same amendment. It carries the costs below:

- Local shell history gets the line only if written deliberately.
- `$?` in the local shell does not see the remote status.

**Typed on `plain`, or in a pane without integration.** Exactly today's path, ADR-0049 wrap included. That wrap is still "try to wrap it, and if that fails, leave it".

**Inside a remote session.** A nested `ssh` remains whatever the far host has. Out of the foundation.

### 5.8 What a person sees

The status sits on the head of whatever is active, from one fold:

```
> ssh prod-db     ◌ Connecting · 0:02 · waiting for the SSH answer    [↻]
> ssh prod-db     ◌ Waiting for the key's signature; it may need a touch
> ssh prod-db     ⟳ Waiting for the server · attempt 14               [↻]
> make build      ◌ Running · 4m 12s · ⚠ no answer for 45 s       [■] [↻]
root@prod-db : /root     ⚠ slow, 1.2 s
```

- **Connection block.** An SSH tab opens with one, painted before any network, which is Q1. While opening it lists stages with timings and any pre-login banner (`BannerCallback`, new capture). Once a shell shows, it collapses to one line, `✓ Connected · 1.4 s`, and stays above as history; the fuller timeline is vision.
- **Bytes.** Banner text is server-supplied, so it is drawn as text in the block and never interpreted.
- **Type-ahead.** Keys pressed before a shell exists are held in the renderer. They go to the composer draft if the shell proves integrated, and to the shell's stdin if it is raw, in the order typed. They are dropped with a line saying so if the connection fails. Nothing is sent while the state is `waiting_person`: those keys go to the prompt that asked. Never into a password dialog from a buffer.
- **Kit.** Status in a head extends `command-block-frame`, with a new typed variance rather than a surface repaint. A mark on the tab already exists.
- **Plain.** The head shows the process and `plain ssh` with its one reason.

### 5.9 Journal

Per connection: stage entered and left with durations, server version, chosen algorithms, host-key decision (fingerprint), methods offered and accepted (no secret material), probe outcomes on grade changes, cause of the close, and each attempt. Bounded in memory like `internal/diagnostics`, whose privacy rule stands for anything copied: no host, user, address, path, fingerprint or typed input leaves through Copy diagnostics. The connection's own screen may show them, later. **Never** a password, passphrase, key bytes or terminal bytes. Decrypted traffic is not available, and "raw" means protocol events.

## 6. Runtime scenarios

**Struggling host, first visit.** TCP is fast and `ssh-version` waits 40 s; the head says so with its clock. Sign-in. Script integration tries, and the pause expires at 3 s; a plain shell shows and takes input. The domain reaches `prompt_ready` at 25 s, after the person typed: the head says blocks are ready, and nothing is swapped. No install is attempted while `slow`.

**Healthy host, helper installed.** About 1.4 s; the far spawn proves itself inside the pause; blocks paint; the raw shell was never opened. The connection block collapses.

**Lid closed for an hour.** Wake signal. All supervisors probe. At 5 s tabs read `stalled`. At 30 s helper-owned connections close and go to `reconnecting`, one per `poolKey`. A connection carrying a plain shell stays `stalled` and offers Reconnect, unless a transport error has already settled it; the offer is one per connection, not one per tab. Vault locked: one `waiting_person(vault-unlock)`, drawn on every tab of that host and asked once. Host answers: helper-held shells reattach, others open anew with a divider.

**Changed key at reconnect.** The supervisor's `HostKeyCallback` refuses before any signature. `failed(host-key-changed)`. All tabs on that host say so. Nothing retries. The existing host-key recovery applies.

**Coordinator restarts.** The helper kept supervisors and sessions. `Readopt` re-binds panes; `pane.connection` is republished from the supervisor's current state under a new `instanceId`.

**Local helper dies.** Transport state is gone with it. The coordinator reads `reconnecting(cause: helper-gone)`, restarts the helper by its existing path and re-runs the driver. Local shells held by the helper are outside this design.

**A file-based FIDO key.** The resolver answers `plain{sk-identity-file}` at resolve time, so OpenSSH handles the touch in the raw terminal. The tab says why.

**A profile edited during a session.** The supervisor's key is `poolKey`, which isolates port, account, identity and jump route (`pool.go:358`), so deduplication never shares sign-in across principals or routes. A live connection stays with what it was opened with; the next reconnect resolves anew, which may yield a different connection, and the divider names the new endpoint (the nocx-9le.7 acceptance).

## 7. Consequences and principles bent

- **KISS is bent** by a new long-lived block and a new contract, because Q6 and Q3 cannot be met with a pane-level answer.
- **DRY is held at the cost of the agreed "one notification":** §5.6.
- **Least astonishment is bent** in two places: a typed `ssh` no longer runs in the shell it was typed into, and a person mid-wait can see a raw shell turn into blocks when nothing was typed.
- **YAGNI is bent** once: the journal lands before its viewer, because the head's stage text and the review of a failed open both read it.
- **Probes double** for each connection by default, and a silent connection adds a look-only dial every 15 s until one is answered.
- **Priority 4 is held over Q5 for a plain shell:** on a silent flow nocx asks instead of closing it, so an unattended tab can sit `stalled` until someone comes back. The owner's choice: waiting costs a click, a wrong close costs the work.
- **`open` changes meaning:** its answer no longer means a shell exists. Every caller of `open` for SSH, workers included, moves to adoption.
- **Two shells on a struggling host** remain possible for the seconds of the race, which is the price of the owner's order; no double on a healthy host, which is Q10.

## 8. Assumptions and what would invalidate them

| Assumption                                                  | Invalidated by                                                                                            |
| ----------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Script integration is cheap enough to race on a loaded host | Stage records showing it costing more than the fallback's benefit: lower its priority or race plain first |
| 3 s suits most healthy hosts                                | Healthy-host stage records above 3 s, painting raw on healthy opens (fails Q10): raise the pause          |
| `net.Conn` plus callbacks mark stage boundaries             | A boundary unobservable: merge stages                                                                     |
| Agent-held `sk-` keys sign on `x/crypto`                    | The owner's key failing: route `sk-` to plain regardless of holder                                        |
| One pane can park a session under another                   | The prototype failing: the owner decides between a new tab and keeping typed lines on plain               |

## 9. Constraints (closed)

| #   | Requirement                                                                                               | Source                               | Check                                                                                                    |
| --- | --------------------------------------------------------------------------------------------------------- | ------------------------------------ | -------------------------------------------------------------------------------------------------------- |
| C1  | An SSH pane paints a stage under 1 s, before any network                                                  | Q1                                   | C-open-paints-first                                                                                      |
| C2  | Input reaches a shown shell whatever integration does                                                     | ADR-0004, Q7                         | C-raw-input in every fault row                                                                           |
| C3  | No signature leaves after a changed host key                                                              | Priority 2, Q8                       | C-key-change-no-signature                                                                                |
| C4  | One sign-in and one vault prompt per `poolKey` across **six** tabs                                        | Q6                                   | C-six-tabs (fixture: 6 panes on one key, plus 1 on another user that must sign in apart)                 |
| C5  | A silent link reads as `stalled` within 30 s                                                              | Q3                                   | C-blackhole-30s                                                                                          |
| C6  | Wake yields a right state within 5 s                                                                      | Q4                                   | C-wake-5s                                                                                                |
| C7  | Commands are never re-run                                                                                 | Owner                                | C-no-rerun: a marker file count stays 1                                                                  |
| C8  | One host by profile and typed ssh uses one key                                                            | Q9                                   | C-same-key, asserted at the server                                                                       |
| C9  | No helper install while `quality.slow`, or before a shell                                                 | Owner                                | C-no-install-first                                                                                       |
| C10 | The wire matches the schema                                                                               | AGENTS rule 5                        | `…_OverTheWireConformsToContract` for `pane.connection`                                                  |
| C11 | No transport state derives from terminal bytes                                                            | AD-6                                 | Review, plus the supervisor's package does not import `sessionruntime`                                   |
| C12 | No secret or typed input in the journal                                                                   | Security                             | C-journal-privacy: scan with planted secrets                                                             |
| C13 | Every attempt and every ask has a declared finite lifetime; `waiting_person` stops the no-progress clock  | ADR-8eej3e66, owner                  | Catalogue completeness test, C-person-stops-clock, C-ask-expires-no-loop                                 |
| C14 | A plain host still opens                                                                                  | Owner                                | C-plain-fallback for each `plain` reason, and from `failed` by the head's action                         |
| C15 | Blocks show only after an accepted domain; a failed acceptance keeps the fallback                         | ADR-0024 §3                          | C-hello-then-refused                                                                                     |
| C16 | Another shell opens only on confirmed absence                                                             | Frame, priority 4                    | C-inventory-unavailable-no-second-shell                                                                  |
| C17 | Imports run only as §5.1.1 says                                                                           | AD-8, owner's question of 2026-11-06 | A Go test over `go list` refuses each forbidden edge, in the shape of `helper/deploy/dependency_test.go` |
| C18 | `frontend/src/connection/` is in no import cycle and imports neither `panes.ts` nor `terminal-content.ts` | AD-8, owner's question of 2026-11-06 | A lint rule added in stage 2, run by the frontend gate                                                   |

## 10. Fault harness and checks

**Harness.** Promote `faultProxy` from `internal/ssh/netfault_test.go` (pass, blackhole, slow, cut) into a shared test package, and add: hold-banner, one-way loss, reset, refuse, bandwidth. Extend `cmd/e2e-sshd` with switches for the server-side faults, and put the proxy between the e2e stand and it. Time is an injected clock or a released hold: a test **waits on an observable**, and "five minutes" is the clock's, never a sleep's. Real OpenSSH servers stay in the `live_sshd` suites for what the fake cannot do.

**Faults, by stage, each asserting the state and C2:**

- **Before SSH:** no DNS, closed port, no route, non-SSH service, locked vault, dead local helper.
- **Handshake:** silent after accept (Q2), MaxStartups reset, no common algorithms, new key, changed key (also at reconnect).
- **Sign-in:** key rejected, passphrase, empty agent, wrong password, 2FA, MaxAuthTries, closed port after bans, forced password change, LoginGraceTime, delayed fake agent for a touch.
- **Shell:** `nologin`, `ForceCommand`, hung `.bashrc`, noisy `.bashrc`, no PTY, MaxSessions, full disk and noexec `/tmp` (install fails in background, session unaffected), other architecture, old helper, interrupted install.
- **During work:** blackhole, one-way loss, reset, reboot behind a silent flow (Q5), a look answered while the old session process is starved (a plain shell stays alive and is offered Reconnect; a helper-owned one reattaches), a plain shell after a reboot (reconnects on the probe's reset), fail2ban preauth filter against looks, wake with a probe already parked, sshd restart (existing session lives) versus reboot, new address, idle cutoff, sleep and clock jump via an injected wake source and clock, far helper crash, jump host down, sshd answering while the shell is hung (Reconnect works, nothing claims a diagnosis).
- **Ours:** update, WebSocket drop, six tabs, profile edited, reconnect needing a password with nobody present (waits; no failed sign-in accrues).

**The epic's check (rule 2).** `e2e/ssh-foundation.spec.ts`: one host, by profile and typed, one key at the server; hold-banner; slow; cut; state right each time; raw input works. It is written first and stays red until the stages land.

**Named checks the stages carry:**

| Check                                                                                                      | Stage   |
| ---------------------------------------------------------------------------------------------------------- | ------- |
| `-whylive` on the supervisor from `main`                                                                   | 1       |
| Far-pane prober present (settles the hypothesis)                                                           | 1       |
| C5, C6, C13, C3, Q2, Q5 detection                                                                          | 1       |
| C4                                                                                                         | 1       |
| C10, C1                                                                                                    | 2       |
| Far-helper reattach after a blackhole, with a marker count proving the same process; an unsaved route; C16 | 3       |
| C9, C15, C2 race (nothing typed versus typed), MaxSessions, Q10                                            | 4       |
| C7, C12, C14, C8                                                                                           | 5 and 6 |

## 11. Hand to stages

Sketch for `to-stages`, leaves carrying the checks above:

1. **Supervisor in the helper:** connection table, states, prober rewrite, wake, journal events, arming on every connection.
2. **Contract and fold:** `pane.connection`, driver skeleton, `open` answering early, heads and tab mark; `session.liveness` and `_liveness` go. **This is when the empty screen ends.**
3. **Reconnect:** auto default and backoff, reattach through the far helper, divider, head button; the auto loop and three-attempt cap go.
4. **Open order:** delayed fallback, install out of the open path, type-ahead.
5. **Resolver:** one entry, `plain` reasons, `profileId` on pane rows; `resolveSshProfileOverlay` and the string match go.
6. **Typed on our transport:** prototype first (a pane holding two sessions), then the AD-7 amendment, the rebind and the block. ADR-akhhb098 is accepted here; if the prototype fails, the owner decides.

Stages 1–3 stand alone if later ones slip. Harness work lands with stage 1.

## 12. Review

Reviewer: OpenAI `gpt-6.1-sol` through `codex exec` read-only, one round, in ATAM's manner: validate against the frame, walk Q1–Q10 and the runtime scenarios. It reported 9 blocking findings and 3 comments. No third round, per the review rule; the fix below has not been re-reviewed.

| Finding                                                                                         | Outcome                                                                                                    |
| ----------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| Q2: a 5-minute attempt bound leaves no time after 5 silent minutes                              | Fixed: `silence` 10 min renewed by stage boundaries (§5.5)                                                 |
| Q5: a silently dead flow can stay open 5 minutes past a reboot; a one-minute cap cannot meet Q5 | Fixed: look-only dial and a 15 s cap, with the budget written out (§5.3). The cap changes the bead's value |
| AD-7 claimed unchanged while parking a session                                                  | Fixed: a deliberate amendment in the last stage (§5.7)                                                     |
| `HELLO` is not the proof ADR-0024 asks for                                                      | Fixed: accepted domain at `prompt_ready`, C15                                                              |
| Unbounded `waiting_person` against ADR-8eej3e66's finite `answer`                               | Fixed: the state may last, each ask is bounded, C13                                                        |
| A started channel cannot be reused for a login shell                                            | Fixed: close, confirm release, open another (§5.2)                                                         |
| Readopt requires `ProfileID` and cannot tell unavailable from absent                            | Fixed: full route on the pane row; only confirmed absence opens a shell, C16                               |
| One resolver does not establish one key                                                         | Fixed: credential rule (§5.7); Q9 scoped to one match                                                      |
| "Host+identity" is ambiguous and could share sign-in across principals                          | Fixed: `poolKey`; Q6 scoped                                                                                |
| Wake evidence and parked probes (comment)                                                       | Fixed: no accumulation; `stalled` means missing evidence                                                   |
| AD-6 inventory wording (comment)                                                                | Fixed: the emulator's ownership stated; C11 reworded                                                       |
| Exit 255 is not proof of a drop (comment)                                                       | Fixed: reads as an ssh error (§5.6)                                                                        |

**After the review, by the owner (2026-11-06).** The reviewed text closed a silent connection after a 30 s grace or 5 minutes, whatever it carried. The owner moved that decision to the person for a plain shell. The change was not reviewed again.

**Also after the review, by the owner's question.** §5.1.1 and C17–C18 were added, moving the driver out of `internal/app` and the supervisor into its own package. Not reviewed again and not compiled.
