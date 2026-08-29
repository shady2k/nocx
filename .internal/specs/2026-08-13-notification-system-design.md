# Notification system — design

- **Date:** 2026-08-13 (revised 2026-08-14 after three adversarial review rounds)
- **Status:** Accepted — ADR-0047 accepted 2026-08-14; AD-1 amended
- **Brainstorming session:** `nocx-uz7f`
- **Depends on:** **ADR-0047**, which decides the category this feature belongs to — a
  program-initiated effect: what it may cause and what it may never choose — and adds
  presentation requests to AD-1's enumeration. **Accepted 2026-08-14.**
- **Epics to create:** A1, A2, A3, B (§9)

## What a user can do that they could not before

Run something that takes a while — a build, an agent, a remote deploy — look away, and be
told when it wants you. On the desktop as a banner, on the dock as a number that stays until
you look, and on your phone through a service you already use. Including when the thing that
wants you is a program on a machine you reached over ssh, which is the case no amount of
local process-watching can cover.

## The boundaries this crosses, and what they already decided

Per AGENTS.md, a brief that crosses a boundary names it before it says what to build.

| Binding document           | What it already decided                                                                                                 | What this design does with it                                                                                                                                                                                                                    |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **AD-1**                   | OSC markers stay frontend-side; ledger facts may cross as typed records.                                                | **Enumeration extended by ADR-0047.** The 2026-08-02 ledger-facts amendment already permits this shape; presentation requests join its closed list, under the rules of ADR-0047 §2.2.                                                            |
| **AD-6**                   | The backend never interprets the **bytes** a session produces; render state lives in the frontend.                      | **Untouched.** Parsing stays in xterm.js; AD-6 governs the byte stream, and never spoke to what the backend does with a typed value (ADR-0047 §4.2).                                                                                             |
| **AD-2**                   | Go backend service as the one core.                                                                                     | Outbound HTTP and the OS calls live in the backend, behind a host port with a Wails adapter (§2.2).                                                                                                                                              |
| **AD-3**                   | Wails v2 as the MVP desktop shell — thin and swappable.                                                                 | The Wails runtime is reached only through that port; devharness binds an unavailable adapter.                                                                                                                                                    |
| **AD-8**                   | Interface-first + DI, one owner per behaviour.                                                                          | Source, router and sink are three interfaces at one composition root. The router owns "where" — stated in ADR-0047 §2.3.                                                                                                                         |
| **ADR-0024** (`nocx-u7uh`) | PTY output is render-only; a program's output cannot drive your terminal.                                               | **Untouched.** Its prohibitions are about authority — an execution attempt, an exit status, input ownership, history — and a presentation request asserts none (ADR-0047 §4.1). OSC 52 is the standing precedent for an effect caused by output. |
| **ADR-0017**               | A connection references a secret; nothing is called a credential.                                                       | A target references a secret the same way, including when the provider wants it in the URL (§4.1).                                                                                                                                               |
| **ADR-0011** §4            | The delete cascade prefers a brief unreachable orphan over metadata pointing at a missing secret.                       | Target creation and deletion follow the same ordering (§6.3).                                                                                                                                                                                    |
| **ADR-0003**               | No Developer ID, ever; ad-hoc signature only.                                                                           | A bundle identifier is **necessary**; that it is **sufficient** is a hypothesis with an experiment attached (§8, §9).                                                                                                                            |
| **`nocx-ywhp`**            | Program-scoped grants; OSC 52 is the first program-initiated action needing consent, and the next one reuses the model. | Deliberately not reused — ADR-0047 §4.5 records why, as the reuse rule requires.                                                                                                                                                                 |
| **`nocx-sb3f`**            | The transport has three delivery classes and models two.                                                                | Epic B depends on it (§6.4).                                                                                                                                                                                                                     |
| **`nocx-2x8x`**            | Redaction covers scrollback, ledger and clipboard — for secrets **injected from the vault**.                            | Insufficient here, and §7 says so rather than cross-referencing past the problem.                                                                                                                                                                |

## 1. Decisions taken with the owner

| #   | Question                                | Decision                                                                                                                     |
| --- | --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| 1   | Direction of "webhook"                  | **Outbound only.** Event → HTTP POST to a URL the user configured. No listening socket.                                      |
| 2   | Bark / ntfy / Telegram                  | **One sink, typed presets.** Not three integrations, and not one free-form template either (§4.1).                           |
| 3   | History / notification centre           | **None.** A notification is transient. The dock badge is what replaces it (§4.3).                                            |
| 4   | May a program-sourced event reach push? | **Yes** — the user chooses the route. Safety is the destination being user-configured, enforced by ADR-0047, not by a grant. |
| 5   | Scope of the deliverable                | **The pipeline**, not a list of events. A new source is one registration.                                                    |
| 6   | Sinks                                   | **Five**: in-app toast, OS banner, sound, dock badge + bounce, push. Push is epic B; the rest are A1–A3.                     |
| 7   | Trust                                   | Every event carries a `trust` class stamped by its source adapter (§3.1). A guess cannot do what an attested fact can.       |
| 8   | Dock badge                              | Counts **tabs with unseen activity**, reusing the existing `hasActivity`. No new state, no new lifecycle.                    |
| 9   | Secrets in URLs                         | A provider secret may occupy a URL position (Telegram's token is a path segment). That makes the URL secret-bearing (§4.1).  |

## 2. Architecture: the pipeline

```
sources ──▶ Event ──▶ router ──▶ [sink, sink, …]
```

**Event** — a closed record. Who may set each field is the point:

| field           | set by                            | on the wire?                                                               |
| --------------- | --------------------------------- | -------------------------------------------------------------------------- |
| `sessionId`     | the renderer's terminal adapter   | **yes** — addressing, not attribution; rejected if not live on that socket |
| `title`, `body` | the source; may be stream-derived | **yes**                                                                    |
| `kind`          | the source adapter                | no — stamped from the method invoked                                       |
| `trust`         | the source adapter                | no — stamped from the source                                               |
| `level`         | nocx                              | no — a program cannot forge `danger`                                       |
| `attribution`   | nocx                              | no — from the authenticated session context                                |
| `at`            | nocx                              | no                                                                         |

Provenance is **structural**: the protected fields are not on the wire to be forged. A schema
proves a record's shape, never who assigned a field, so validating authorship was never the
answer (ADR-0047 §2.2).

`sessionId` is the exception and it is deliberate. An earlier revision removed it along with the
protected fields, which was a regression: AD-1 multiplexes many server-assigned sessions over one
WebSocket, so a record carrying only `{title, body}` cannot say which terminal spoke — and the
session is what keys the debounce, decides suppression, stamps attribution and receives focus on
click. It is **addressing**: the backend rejects an id not live on that connection and derives
every attributed field from its own registry, never from the record.

**Ingress authority is closed** (ADR-0047 §2.2): no renderer-callable method can produce an
`attested` event. `notify.raise` and BEL are always `programRequest`; `block.finished` originates
only at the lifecycle publication boundary and `session.ended` only at the session registry.
Without that rule, "stamped from the method invoked" would only move the forging one level up.

**Router** — the only holder of "where". Maps `(kind, trust)` through a **default-deny** table
to the enabled sinks and targets, and the same table governs the ad-hoc subscription route
(§5). **Resolution completes before any sink is invoked**; a sink receives an immutable
resolved destination and can never select a target, credential, method, retry or redirect.

**Sink** — validates, redacts, encodes, delivers. Nothing else.

### 2.1 Where each part lives

- **OSC parsing: renderer.** `parser.registerOscHandler(9, …)` beside the existing 7, 52, 133,
  636 and 1337 in `frontend/src/renderers/xterm.ts`. The renderer raises `notify.raise` as a
  JSON-RPC **request**.
- **Router: backend.** It holds the targets and their secrets.
- **Toast: renderer** (`showToast`, already in the kit). Everything else: backend.

### 2.2 The host port

`runtime.SendNotification`, the dock badge and the attention bounce are all host-context-bound.
They are reached through one `AttentionHost` interface with a Wails adapter; `cmd/devharness`
and any future web host bind an adapter that reports itself unavailable. Without this seam the
"one core" of AD-2 is welded to the AD-3 shell, and the pipeline is untestable outside a
desktop build.

## 3. Sources

| `kind`              | `trust`          | Origin                                                                    |
| ------------------- | ---------------- | ------------------------------------------------------------------------- |
| `block.finished`    | `attested`       | block ledger (ADR-0024) — exit code and duration; needs shell integration |
| `session.ended`     | `attested`       | `lifecycle.changed`                                                       |
| `program.notify`    | `programRequest` | OSC 9 (plain), OSC 777;notify — renderer                                  |
| `bell`              | `programRequest` | BEL, via the existing `onBell`                                            |
| `pane.workFinished` | `heuristic`      | `detectAgentStatus`: `working → idle` held for 5 s (§3.4)                 |

### 3.1 What each trust class may reach

| `trust`          | May reach                                                          |
| ---------------- | ------------------------------------------------------------------ |
| `attested`       | every sink; and completion subscriptions                           |
| `programRequest` | every sink; never a completion subscription                        |
| `heuristic`      | local attention only — toast, dock badge, tab dot. **Never push.** |

**Only an `attested` event may match, activate, deliver through, or disarm a completion
subscription** (ADR-0047 §3). Saying only that a guess cannot _close_ one leaves it able to
match, borrow the subscription's sinks and its suppression override, deliver, and leave the
subscription armed so the real completion delivers again. The matching attested event **consumes**
the subscription once every selected sink has returned, whatever the outcome — consuming only on
success would leave it armed to fire on something unrelated later, and a failed delivery is
already visible as a `danger` toast.

### 3.2 OSC 9 is overloaded — the parser must discriminate

- `ESC ] 9 ; text` — a notification request.
- `ESC ] 9 ; 4 ; state ; pct` — the ConEmu **progress** protocol.

OSC 9;4 is recognised and **produces no event**; it exists in the parser only so it cannot be
mistaken for the first form. A naive handler on 9 turns a progress tick into a push.

### 3.3 OSC 1337 is not a new source

`xterm.ts:388` already owns 1337, and its comment states the rule: _"One handler owns OSC 1337
(ADR-8): the recovery fence is the same ident with a different payload kind, so it dispatches
from here."_ If iTerm2's `RequestAttention` is ever wanted, it discriminates **inside** that
handler. Out of scope for now — OSC 9 and 777 cover the case.

### 3.4 `pane.workFinished` is new work, not an existing signal

An earlier draft called this "a subscription to an existing signal". That was wrong.
`detectAgentStatus` (`frontend/src/agent-status.ts`) is a **stateless classifier**, and its
caller (`frontend/src/tabs.ts:271`) has no timer and calls `markActivity()` whenever the new
value is `idle`, regardless of the previous one — so `null → idle` fires today. Three rules,
all of them new state machine:

1. **Only the `working → idle` edge.** Never `null → idle`. The module says why: _"A title that
   never mentions an agent is not an idle agent."_
2. **Idle held for 5 s.** Claude Code's title oscillates ✳ ↔ spinner every 1–3 s between tool
   calls; a bare edge fires on each. The 5 s settle window is termic's, with its reasoning
   recorded in their source. Cancel on `idle → working`, on the title going `null`, on tab
   close and on session replacement.
3. **Named honestly.** `BRAILLE_SPINNER` matches `⠀-⣿` in any title — `npm install` with ora,
   `docker pull`, half of all TUIs. The label is **"работа в панели завершилась"**, never
   "агент закончил", and its `trust` is `heuristic` for exactly this reason.

### 3.5 Claude Code has a better path than the heuristic

Claude Code has a "Send notification" setting that emits **OSC 9** (termic handles it as the
first entry in their dialect list). Once the OSC 9 handler exists, Claude notifies with its own
text and no Claude-specific code in nocx — at `programRequest` rather than `heuristic`. **To
verify in the first iteration** (§11); if it holds, §3.4 is a fallback for agents that stay
silent rather than the primary path.

### 3.6 Deliberately not a source

- **Agent events (`agent.done`, `agent.needsInput`)** — they belong to `nocx-dw3` and arrive as
  a child of that epic, one registration against this pipeline. This is what keeps these epics
  unblocked: they touch no common code, so a blocking edge would be the "not yet" edge AGENTS.md
  had to strip 13 times out of 20.
- **Deeper per-agent title classification.** `agent-status.ts` is deliberately minimal — "kept
  to the markers we can actually verify". Growing it is a separate decision.

## 4. Sinks and targets

### 4.1 The push target, and three categories of field

| field                | note                                                                        |
| -------------------- | --------------------------------------------------------------------------- |
| `id`, `name`         |                                                                             |
| `preset`             | `bark` \| `ntfy` \| `telegram` \| `custom`                                  |
| `endpoint`           | a **template**, user-supplied, with a slot for the secret where one belongs |
| `secretRef`          | reference into the vault (ADR-0017); never inline, never stored composed    |
| preset-specific keys | Telegram `chatId`; ntfy `topic`; Bark `deviceKey` (a secret)                |
| `accepts`            | which `kind`s and which `trust` classes this target takes                   |

What may appear where in a URL has three answers, not two:

| Category                    | Example                                  | May appear in the URL?                    |
| --------------------------- | ---------------------------------------- | ----------------------------------------- |
| stream-derived              | `title`, `body`                          | **never**, in any position                |
| user-configured, not secret | host, ntfy topic, Telegram `chatId`      | anywhere                                  |
| user-configured **secret**  | Telegram bot token, ntfy token, Bark key | where the provider requires — with §4.1.1 |

**There is no `urlTemplate` and no payload variable in URL construction.** An earlier draft had
one accepting `{{body}}`, which handed the URL authority to program output —
`https://{{body}}/notify`, and `https://gateway/?next={{body}}` through a query parameter. The
revision after that granted Bark a payload-derived path segment and contradicted its own rule
in the same document. Both are gone: the rule is absolute, and **Bark uses its JSON endpoint
`POST /push`**, carrying `device_key`, `title` and `body` in the body. Telegram already fits —
its token is a _secret_ in the path, not stream-derived — and ntfy fits with the topic in the
path, the token in an `Authorization` header and the message in the body.

#### 4.1.1 A secret-bearing URL is handled as a secret

Telegram's `bot<TOKEN>/sendMessage` puts a credential in the path, which is the provider's
design and not ours to fix. **The permission is narrow: one fixed component that the provider
requires, declared by a typed preset — never the scheme, userinfo, host, port, query or
fragment.** A secret in the host would leak through DNS and TLS SNI before any code of ours runs.
A `custom` target may put its secret only in a fixed header or body field. Five consequences, none
optional:

- The stored `endpoint` is a template; the composed URL exists only for the duration of a
  request and is **never persisted**.
- The composed URL is **never logged** — not in a diagnostic, not in an error, not in a trace.
- **The error carries it even when we do not.** `http.Client.Do` returns `*url.Error`, whose
  `Error()` prints the URL; Go redacts a userinfo password but not a path segment, so a single
  `fmt.Errorf("push failed: %w", err)` puts a bot token in the log. `*url.Error`, redirect errors,
  transport errors and recovered panics are classified **inside the adapter** and replaced with a
  target-named error before anything logs, wraps, traces or presents them. The request uses a
  dedicated client with no ambient proxy selection, no tracing hooks and no wrapping transport
  that could observe the composed URL. Tests use a sentinel secret and assert its absence from
  logs, wrapped errors, traces, panic output and fixture failure output.
- A delivery failure names the **target by name**. Never its URL, or a Telegram bot token
  appears in a toast.
- Redirects are refused — the second and independent reason, since following one hands the
  token to a stranger.

The presets differ in schema, not configuration: Telegram needs a `chatId` distinct from the
token, ntfy a topic distinct from the server, Bark a device key distinct from the message. One
generic substitution engine cannot represent all three safely, so each preset declares its
payload position, its encoder, and where its secret goes.

### 4.2 Encoding is the sink's only freedom

Each sink declares maximum encoded sizes and permitted presentation characters. **CR, LF and
NUL are rejected everywhere.** A path field percent-encodes exactly one segment; a JSON field
goes through a JSON encoder; a header goes through HTTP field-value validation; a raw body sets
a fixed content type; OS fields are bounded before the platform call. **An invalid payload
fails visibly and never falls back to string concatenation**, and a sink-level rejection is a
failed delivery — it never removes the sink from the resolved set (ADR-0047 §2.2).

**Every sink invocation is synchronous and carries a finite-deadline context.** Expiry
_cancels_ the invocation; the closing event is the invocation's **return**, not expiry itself — a
timeout is a logical result, not proof that a goroutine stopped writing. Finalization is one-shot,
so a late callback is ignored rather than double-finalising.

**The router holds global limits** on in-flight invocations, on queued and coalesced instances,
and on retained payload bytes; admission beyond a limit is a visible failed delivery. The per-key
debounce bounds one key, not the aggregate — many sessions each delivering slowly but inside their
deadline is otherwise unbounded growth.

Injection vectors to test: CRLF, `%2F`, `?`, `#`, invalid UTF-8, NUL, bidi controls, oversized
payloads.

### 4.3 Local sinks

Toast, OS banner, sound, and **dock badge + bounce** are built-in rows of the same routing
table with the same per-`kind` switches.

The badge and the bounce need **our own cgo** — Wails v2.13 exposes neither (`dockTile`,
`badgeLabel` and `requestUserAttention` are absent from the entire module, and
`NotificationOptions` has no badge field). About thirty lines of ObjC behind the
`AttentionHost` port of §2.2.

They earn it: with no notification centre, they are the **only** surface that persists until the
user looks. The badge counts **tabs with unseen activity** — `hasActivity` already exists, is
already set, and already clears when the tab is visited, so there is no new state and no new
lifecycle. Zero such tabs, no badge. The bounce fires once (`NSInformationalRequest`) on a
`danger` event while unfocused; never `NSCriticalRequest`, which bounces until you come, and a
terminal that does that is Clippy.

## 5. Ad-hoc subscriptions

A user gesture attaches a one-shot notification to a block or a tab, through the kit's existing
`ContextMenu`:

- on a **block** — "Уведомить, когда закончится"
- on a **tab** — "Уведомить, когда сессия завершится"

Only an `attested` event may match, activate, deliver through or disarm one (§3.1).

**If the signal does not exist, the menu item is absent — not disabled.** A block subscription
needs shell integration on that session; a tab subscription needs only lifecycle, so it is
always offered. Tabby does exactly this, gating on `await tab.getCurrentProcess()`
(`~/repos/tabby/tabby-core/src/tabContextMenu.ts:174`).

The subscription does not survive a restart — there is no store, which follows from decision #3.

## 6. Suppression, rate and failure

### 6.1 Suppression, and its interaction with an explicit request

Nothing is delivered about the tab the user is looking at, in a focused window.

**An ad-hoc subscription outranks suppression**, because it is an explicit gesture: if the user
asked to be told when this block finishes and is watching when it does, it fires to the local
sinks and disarms. Silently disarming a suppressed subscription defeats the gesture; leaving it
armed makes it fire on some unrelated later event. The override is reachable only by an
`attested` event, which is what stops a guess borrowing it (§3.1).

### 6.2 Rate

- **Debounce, keyed `{sessionId, kind}`** — 8 s to start, termic's number, adopted rather than
  invented. Keyed by session and not by kind alone, so two tabs never collapse into one
  notification and lose their attribution.
- **Coalescing** within the window produces one notification naming the count, carrying the
  attribution of the session it was keyed on. Memory is bounded.

Both are payload-independent, which is what keeps ADR-0047's noninterference invariant true:
neither the key nor the count depends on `title` or `body`.

Without these, `while true; do printf '\e]9;spam\a'; done` is ten thousand pushes and a banned
Bark key.

### 6.3 Failure paths, as intervals

AGENTS.md rule 3 wants both ends named, not a list of terminal errors.

| Interval                                          | What is true throughout, and how the next start recovers                                                                                                                                                                                                                                                              |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Target creation: secret written, document not yet | The secret is an orphan from the vault write until the document commits. On failure the secret is deleted; a failed delete leaves an orphan for `nocx-2x8x`'s janitor. **Never a dangling `secretRef`.**                                                                                                              |
| Target deletion: document removed, secret not yet | Document first, per ADR-0011 §4 — a brief unreachable orphan beats metadata pointing at nothing.                                                                                                                                                                                                                      |
| Target edited while the router holds it           | Resolution takes an immutable snapshot per event; an edit affects the next event, never a delivery in progress.                                                                                                                                                                                                       |
| Store committed, in-memory routing not refreshed  | The refresh is part of the commit's publication, as settings already do. Disk and runtime never disagree past the commit.                                                                                                                                                                                             |
| Notification instance created → released          | The closing event is every selected invocation **returning** after success, failure or cancellation — expiry cancels, it does not by itself end the interval (§4.2). Finalization is one-shot; a late callback is ignored. Global admission limits bound the aggregate. At-most-once; no retry survives process exit. |
| Subscription armed → consumed                     | It exists from the gesture until the matching **attested** event has had every selected invocation return, or the tab closes. That event then consumes it regardless of outcome; the failure stays visible as a toast. A non-attested event cannot match, activate, deliver through or consume it.                    |
| Vault locked when a push needs its token          | The push fails loudly by §6.4, and does not silently skip.                                                                                                                                                                                                                                                            |

Independently failing and each needing a test: template rendering, URL composition, header
validity, JSON encoding, DNS, TLS, redirect refusal, oversized payload, response read, deadline
expiry, cancellation; and `InitializeNotifications`, the authorization request, the send, the
click-callback decode, the tab lookup, the focus call, and the sound invocation.

### 6.4 A failed delivery must be visible, and the transport can eat it

A push returning non-2xx, timing out, or hitting its deadline raises a local toast at `danger`
**naming the target by name** (§4.1.1). With no history, this is the only place a failed
delivery is visible — required by "a soft degrade must be visible in the product, not only in a
log".

But that toast is a backend → renderer notification with **no successor**, which is exactly the
class `nocx-sb3f` describes: today it rides the refreshable queue and is dropped under
saturation, so the visible failure vanishes silently. **Epic B depends on `nocx-sb3f`** — a
legitimate blocking edge, since both live in the outbound queueing of `internal/transport`.

Permission failure is not one state but three: `IsNotificationAvailable` false (the row is
unavailable and says why), authorization never requested (the control requests it), and
authorization denied (the control says macOS is suppressing display and points at System
Settings, because nocx cannot re-prompt after a denial).

## 7. Security

The invariants live in **ADR-0047** and are binding rather than aspirational: structural
provenance, differential noninterference, the absolute destination rule, secret-bearing URLs,
and retention with a closing event. §4.1 and §4.2 are their design-level consequences.

**Redaction is not covered by `nocx-2x8x`, and saying so is the point.** That epic masks secrets
_injected from the vault_, in scrollback, ledger and clipboard — not an HTTP body, and not a
secret that was never nocx's to know. Program-chosen text can carry anything. So epic B must
either extend the redaction contract to the push sink explicitly, or state in the target UI that
unredacted terminal content leaves the machine. A cross-reference does neither.

**Residual risk, accepted deliberately:** ADR-0047 §4.6.

## 8. macOS specifics — researched, not assumed

**`UNUserNotificationCenter`** (`UserNotifications.framework`) is the correct API.
`NSUserNotification` has been deprecated since macOS 11 and is what silently drops the banner
sound — the reason termic plays sound separately via `afplay`.

**Wails v2.13.0 already implements it.** `pkg/runtime/notifications.go` exposes
`InitializeNotifications`, `IsNotificationAvailable`, `RequestNotificationAuthorization`,
`CheckNotificationAuthorization`, `SendNotification` (with an `opts.Data` payload),
`SendNotificationWithActions`, `RegisterNotificationCategory`, `OnNotificationResponse`, and the
removal calls. Underneath, `internal/frontend/desktop/darwin/WailsContext.m` imports
`<UserNotifications/UserNotifications.h>`, installs a `UNUserNotificationCenterDelegate`, and
calls `requestAuthorization` with `Alert|Sound|Badge` plus
`getNotificationSettingsWithCompletionHandler`.

| Comparable product's wall                                                     | Answer here                                                                                           |
| ----------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| termic: osascript has no click callback, so they keep a focus-edge heuristic  | `OnNotificationResponse` + `opts.Data` — the same payload idea, without the heuristic                 |
| orca: a native Swift binary purely to learn whether notifications are enabled | `CheckNotificationAuthorization`                                                                      |
| termic: the deprecated API swallows the banner sound                          | not applicable — this is not that API                                                                 |
| Windows / Linux                                                               | Wails carries both; `go-toast/v2` is indirect in `go.mod` because it **is** the Wails Windows backend |

**What Wails does not give us:** the dock badge and the attention bounce (§4.3).

**A bundle identifier is necessary. That it is sufficient is a hypothesis.** Wails returns
`"notifications require a valid bundle identifier"`, which establishes necessity for that
implementation and nothing more. Whether macOS preserves authorization for an ad-hoc-signed
bundle across an update, and whether a `wails dev` re-sign resets it the way it re-triggers the
keychain prompt (`nocx-o4hg`), is unknown. §9 makes it an acceptance condition of A1, not a note.

**Two nocx facts to fix before A1 ships:**

1. `build/darwin/Info.plist` and `Info.dev.plist` both carry the Wails template default
   `com.wails.{{safeBundleID .Name}}`. macOS keys notification authorization to the bundle
   identifier, so **taking our own identifier must land first** — renaming later resets every
   user's permission.
2. Both carry the **same** identifier, so the dev stand and the shipped app are one identity to
   macOS. Notification permission does not follow the `nocx` / `nocx-dev` split that settings
   and the vault follow (`internal/storage/appdir.go`).

## 9. Epic decomposition

The first draft had one epic A carrying the pipeline, five sources, four sinks, native
notification lifecycle, permissions, bundle identity, suppression, debounce and two context-menu
features. That is an area, not a deliverable. Four epics:

### A1 — "Программа из панели дозвалась до тебя"

The pipeline (event, router with its default-deny table, sink interfaces, the `AttentionHost`
port), the OSC 9 / 777 receiver, `program.notify` and `bell`, the toast / OS banner / sound
sinks, attribution, suppression, debounce and coalescing. Plus the bundle identifier.

**DONE WHEN — automated:** a program on a real pty through `cmd/devharness` prints OSC 9;
`notify.raise` crosses the real socket conforming to its contract and carrying `title` and
`body` and nothing else; the router resolves the expected sinks; and the `AttentionHost` fake is
invoked with the exact title, body and backend-stamped attribution. Plus the differential
noninterference property test (ADR-0047 §2.2), and — through the port's fake — the click-callback
decode, the originating-tab lookup, the focus call, and the unavailable and denied authorization
states.

**DONE WHEN — a packaged-build experiment, and this is a gate rather than a note.** On a clean
macOS profile the manual actions are exactly three: granting authorization when macOS prompts,
confirming the banner is visible, and clicking it. Everything around them is automated: the
harness records authorization before replacement, installs the update under the same bundle
identifier, relaunches and asserts authorization is still granted; it then emits a notification,
waits for the operator's click, and asserts callback decoding, originating-tab lookup and focus.
It records authorization before and after a `wails dev` re-sign. The observations and the build
identities are the acceptance evidence.

Granting permission is itself an unavoidable manual OS interaction on a clean profile — an
earlier revision listed only the banner and the click, which understated it. macOS UI automation
exists and could in principle drive all three; it is too fragile and permission-heavy for this
project's CI, which is a choice rather than an impossibility. Without this gate a green suite over
a fake adapter reports a working feature across a platform seam nobody exercised, which is the
`nocx-rtg0` failure exactly.

### A2 — "Ты видишь, что пропустил, не открывая ничего"

Dock badge (counting tabs with unseen activity) and the single attention bounce behind the
`AttentionHost` port; plus `pane.workFinished` at `trust: heuristic`, which can reach only these
surfaces.

**DONE WHEN:** with the window unfocused, a spinner in a background tab settling to idle for 5 s
raises the tab dot and increments the badge; visiting the tab clears both; and the same event
reaches neither a completion subscription nor the push sink. Automated against the port's fake,
with the badge value asserted.

### A3 — "Скажи мне, когда вот это закончится"

`block.finished` and `session.ended` at `trust: attested`, and the one-shot subscriptions on a
block and on a tab, including the absent-not-disabled rule and the suppression override.

**DONE WHEN:** with shell integration, right-clicking a running block and choosing "уведомить,
когда закончится" produces exactly one notification when it exits and no second one; on a session
without shell integration the menu item is absent; and a `heuristic` event arriving for the same
session while the subscription is armed neither delivers through it nor disarms it.

### B — "Нотификация догоняет тебя в телефоне"

The target entity, its store and CRUD surface, the vault-backed secret, the four typed presets,
the HTTP sink with its per-context encoders and deadlines, and the visible delivery failure.

**DONE WHEN:** a user creates a `custom` target through the UI, its secret goes to the vault, the
app restarts, a command fails, and the local test HTTP server receives a POST carrying the event
and its attribution. Driving the UI is the point — a test that pokes the store directly proves
the sink, not the feature. Plus: a target whose composed URL embeds a secret never emits that URL
into a log or an error surface, asserted.

**Depends on:** A1 (the pipeline) and `nocx-sb3f` (§6.4).

### Alongside

- Close `nocx-4clc` — delivered: `frontend/src/ui/toast.tsx` exists, `showToast` is imported by
  22 files, `ToastHost` is mounted once, `.st-export-status` is gone.
- Re-parent `nocx-8yg.11` out of the `nocx-8yg` area epic into A1.

### Deliberately out

History and a notification centre; agent events (a child of `nocx-dw3`); deeper per-agent title
classification; OSC 1337 RequestAttention (§3.3); an inbound webhook endpoint; per-program grants
(ADR-0047 §4.5); a durable retry queue (ADR-0047 §2.2 — adding one is a deliberate amendment, not
an HTTP implementation detail).

## 10. Testing

- **The wire.** `notify.raise` gets its JSON Schema in `contracts/` in the same commit as the
  method, `additionalProperties: false` plus explicit `required`, carrying `title` and `body` and
  nothing else — that absence is what makes provenance structural — with the
  `…_OverTheWireConformsToContract` test off the real socket.
- **The differential property test** (ADR-0047 §4.3), ranging over every **schema-valid** input
  and comparing **route resolution**, which is ordered before sink validation. Restricting the
  generator to payloads a sink would accept would exclude exactly the oversized and
  invalid-encoding cases that could diverge.
- **Injection vectors** per §4.2.
- **Every external call has a failing test, and each is paired with "and on an ordinary machine
  it succeeds"** — the `contentkey` lesson, where every failure path was tested and the success
  path was never reachable.
- **Invariants as intervals** — §6.3 is written that way so the tests can be. Named cases: an
  adapter that blocks until its context is cancelled; a callback arriving _after_ cancellation
  finalised the instance; a duplicate completion; and saturation by many concurrent sessions,
  which must produce visible failed deliveries rather than an unbounded queue.
- **Secret containment**, with a sentinel token in a Telegram-shaped target: assert the sentinel
  appears in no log, no returned or wrapped error, no trace, no panic output and no fixture
  failure output — the `*url.Error` path especially (§4.1.1).
- **Acceptance criteria as assertions in the beads**, not prose, so the implementer is not the
  only author of the test.

## 11. To verify during implementation

1. Does Claude Code's "Send notification" actually emit OSC 9 against a live run (§3.5)? If yes,
   `pane.workFinished` demotes to a fallback.
2. Exact request shapes for Bark's `POST /push`, ntfy and Telegram, to fix each preset's schema
   and confirm every one of them can carry the message outside the URL (§4.1). If any cannot, it
   leaves scope rather than weakening the destination rule.

The packaged-build signing question was here in an earlier revision. It is now an acceptance
condition of A1 (§9), because a feature that ships across an unexercised platform seam is not
verified by anything that "to verify during implementation" describes.
