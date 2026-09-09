# Notification centre — design

- **Date:** 2026-08-22
- **Owner:** shady2k
- **Bead:** `nocx-v0omj` (brainstorming session)
- **Amends:** decision #3 and decision #8 of
  [the notification system design](2026-08-13-notification-system-design.md) (2026-08-13)
- **Reviewed by:** an adversarial pass (codex) against the code, 2026-08-22. Fourteen
  findings; three overturned decisions taken earlier in this session. Dispositions are
  recorded inline, at the decision each one changed.

## 0. What this amends, and why that is not a footnote

The 2026-08-13 design answered "History / notification centre" with **None. A notification
is transient. The dock badge is what replaces it**, and §9 listed a notification centre as
deliberately out of scope. That decision is now reversed by the owner. Two consequences
follow immediately and are settled here rather than left to be discovered:

- **Decision #8 is superseded too.** It made the dock badge count _tabs with unseen
  activity_, reusing `hasActivity`, and justified itself with "no new state, no new
  lifecycle". A centre with unread rows **is** a second lifecycle. §6 names the one source
  of truth for the bell count, the dock badge, tab activity and mark-read, so the two
  cannot disagree.
- **"A notification is transient" survives in ADR-0047's sense** — nothing is persisted to
  disk, nothing outlives the process — and stops being true in the product sense. §7 states
  the retention interval with both ends.

## 1. What a user can do that they could not before

**Open the bell in the activity bar and see what happened while they were not looking —
including the events no banner showed them — and mark them read.**

The end-to-end check that watches it (AGENTS.md rule 2), written now rather than at the
end: with the window unfocused, a session on a remote host ends; the bell shows an unread
row naming that session and its cause; clicking the row focuses the tab; the unread count
returns to zero and the row remains in the feed.

## 2. Boundaries this crosses, and what they already decided

| Boundary                                                     | Already decided                                                                                                              | This design                                                                                                                                   |
| ------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-1**                                                     | One WebSocket: binary data plane + JSON-RPC control plane                                                                    | The feed is control plane. No PTY byte ever enters it                                                                                         |
| **AD-6**                                                     | The backend never sniffs the byte stream                                                                                     | Untouched. OSC parsing stays in the renderer (`renderers/xterm.ts`), which then calls `notify.raise` as it does today                         |
| **AD-8**                                                     | One owner per behaviour, interface-first, wired at one composition root                                                      | The **router** stays the only holder of "where". The **centre** is the only holder of "what was raised". Neither answers the other's question |
| **ADR-0047**                                                 | Routing resolves once before any sink; a sink never selects a target; default-deny; trust classes are hard capability bounds | Unchanged and load-bearing. §5 states what "unfiltered ingest" does **not** mean                                                              |
| **ADR-0048**                                                 | UI state is a document on the Go side; localStorage may not carry facts                                                      | The feed is in memory and dies with the process, so it is not a document. `mark all read` is therefore also in memory — see §7                |
| **helper design D17** (`2026-08-13-remote-helper-design.md`) | A helper service may only be the remote half of an interface that already exists locally                                     | §4's `Ingress` is that local interface. Defining it now is the precondition for a helper ever raising events, not a speculative hook          |
| **`nocx-if6` phase A**                                       | Session identity becomes `(backendId, sessionId)`                                                                            | The occurrence record carries backend identity from the first commit. §4                                                                      |

## 3. The shape, in one paragraph

Everything raised enters an **ingress** stage that stamps what nocx owns and hands the
occurrence to two independent consumers: the **feed**, which remembers, and the **policy +
router**, which deliver. The feed never decides delivery; policy never decides membership.
That is the whole inversion — today policy sits in front of the router and a suppressed
event is destroyed (`internal/app/app.go:1045`, `internal/notify/policy.go:28`), so the
events most worth seeing are exactly the ones nothing remembers.

```
notify.raise ─┐
session end  ─┼─► Ingress (stamps At, Level, Attribution, mints OccurrenceID)
block done   ─┘        │
                       ├─► Feed (in-memory, bounded, revisioned)  ──► notify.feed.read / feed.changed
                       └─► Policy ──► Router ──► Sinks (banner today; toast, sound, push later)
                                       │
                                       └─► delivery outcome ──► Feed.recordOutcome(OccurrenceID)
```

## 4. Ingress: the seam, and what it stamps

**Finding folded in (review, severity 2):** an earlier draft recorded at the entry to
`Policy.Submit`. That is wrong — `Router.Raise` stamps `ev.At = time.Now()`
(`internal/notify/notify.go:318`, "the router is the first nocx-owned stage of the
pipeline"), so a hook before it would file every occurrence with a zero timestamp, against
the `Event` contract and ADR-0047. Stamping moves **into ingress**, once, and `Router.Raise`
stops doing it. Nothing restamps afterwards.

```go
// Ingress is the one entry point of the notification pipeline: it stamps the
// fields nocx owns, mints the occurrence identity, and fans out to the feed
// and the delivery path. It is the local interface a remote helper's `notify`
// service may later be the remote half of (helper design D17).
type Ingress interface {
    // Admit stamps and records one occurrence, then submits it for delivery.
    // The returned id is stable and is how an asynchronous delivery outcome
    // finds its occurrence again.
    Admit(ctx context.Context, ev Event) (OccurrenceID, error)
}
```

`OccurrenceID` is minted here and is the identity the rest of the design hangs on. The
review's strongest structural point was that the earlier draft had **no** occurrence
identity — it tried to make one collapse key serve as identity, as debounce stream and as
feed row at once, and those have three different lifetimes.

**Attribution gains backend identity now** (`nocx-if6` phase A), and the existing dishonesty
is not propagated: `notify.Attribution.Tab` is stamped from the WebSocket connection id
(`internal/transport/ws_notify.go:119`) and `nocx-wyp3p` is that defect. The occurrence
record carries `(BackendID, SessionID)` and **does not carry a tab**; resolving a session to
a tab is the renderer's knowledge and stays there (§8).

## 5. Unfiltered ingest — and the two things it does not mean

Every raised event enters the feed. Policy decides which **channels** fire; it never
decides membership. A suppressed event, a coalesced one and a delivered one are all in the
feed, and the row says which channels fired.

**It does not mean the trust bound is negotiable.** ADR-0047 §3 makes trust classes hard
capability bounds: `TrustHeuristic` may never reach a sink whose `LeavesMachine()` is true,
and `NewRouter` refuses to construct such a table at all (`ErrTrustCapability`,
`internal/notify/notify.go:289`). That check stays exactly where it is and never becomes
user-configurable, in this epic or in the settings epic (`nocx-3mniv`). A routing rule that
would send an inference off the machine is refused at construction, loudly.

**It does not mean unbounded retention.** See §7.

## 6. One source of truth for "unseen"

Superseding decision #8. Four surfaces could each invent their own count; they get one.

| Surface                                 | Reads                                                                                  |
| --------------------------------------- | -------------------------------------------------------------------------------------- |
| Bell badge in the activity bar          | `feed.unreadCount`                                                                     |
| Dock badge (when `nocx-3a40` builds it) | `feed.unreadCount` — **not** tabs with `hasActivity`                                   |
| Per-row unread mark                     | the occurrence's own `readAt == nil`                                                   |
| Tab activity dot                        | `hasActivity`, unchanged — it answers "this tab produced output", a different question |

**Visiting a tab does not mark its notifications read.** They are different facts: output
arrived, versus you saw what we told you. Conflating them is what decision #8 did, and it
is why a centre could not be built on top of it.

## 7. Bounding, with both ends named

**Finding folded in (review, severity 1) — the earlier claim "memory is bounded
structurally by tabs × kinds × levels" was false.** `session.NewID()` mints fresh random
128 bits per `Open` (`internal/session/session.go:313`), so a tab that reconnects produces a
new session id and therefore a new key. The bound is "every session ever opened in this
process", which is not a bound. Collapsing remains the right answer to a flood; it is **not**
a substitute for a global limit, and the global limit is part of the primary model, not a
backstop.

Three limits, all explicit, all reported:

- **`MaxOccurrences`** — total rows held. Reaching it evicts the oldest **read** occurrence
  first; if none is read, the oldest unread. Never a refusal: refusing new occurrences
  freezes the feed at the moment a flood began, which is the failure the flood argument
  rejects.
- **`MaxRetainedBytes`** — the same currency `Limits.MaxRetained` already uses
  (`internal/notify/notify.go:201`). `notify.raise` admits 4096 runes of title and 4096 of
  body (`internal/transport/ws_notify.go:82`), so rows are not a usable unit of account on
  their own.
- **`MaxRun`** — the flood collapse, below.

**Eviction is visible.** A single reserved row, outside the occurrence budget, carries "N
occurrences dropped" with the oldest and newest instants it covers. A soft degrade must be
visible in the product, not only in a log.

### Flood collapse

A flood in a terminal is one session repeating one kind, not N distinct facts. Consecutive
occurrences sharing `(BackendID, SessionID, Kind, Level)` **within a bounded window** compact
into one row carrying a count and the newest title.

**Finding folded in (review, severity 3):** an earlier draft made the row live until "mark
all read", and reused `DebounceKey` as its identity. Both are wrong. Identity and interval
are different: a debounce stream lives eight seconds (`notifyDebounceWindow`, `internal/app/app.go:403`), a
feed row would have lived for hours, so "deploy staging succeeded" and "deploy production
succeeded" from one session would have merged because the user had not cleared the inbox.
The collapse window is therefore **its own bounded interval**, and the occurrences inside a
collapsed row keep their individual `OccurrenceID`s, so §9's epic 2 can expand them.

The window is a named constant beside `notifyDebounceWindow`, and its **value is not the
open question the interval is**: it starts at 30 seconds. The constraint that matters is an
ordering one, and it is what the review's counterexample turns on — the collapse window
must be **strictly shorter than the time a row can stay unread**, or the two intervals
collapse into one again and "deploy staging" merges with "deploy production". A run that
spans the window boundary opens a second row; that is correct, not a rounding error.

`DebounceKey` stays what it is and keeps governing only its own eight seconds.

### Level in the key

**Finding folded in (review, severity 6):** calling this a defect fix was wrong. The only
production constructor of `notify.Event` is `notify.raise`, and it hard-codes
`Level: notify.LevelInfo` (`internal/transport/ws_notify.go:151`), so a danger arriving
behind a success cannot occur today. `Level` is in the collapse key because a failure must
not be compacted into a run of successes **once a source produces both** — it is forward
structure for §9's `session.ended`, stated as such, not a bug being fixed.

It also changes delivery once such a source exists: alternating severities would open one
debounce window per severity and could emit one closing summary each. `DebounceKey` is
therefore deliberately **not** given `Level` — only the feed's collapse key has it. The two
keys answer different questions and are allowed to differ, because §4 gave occurrences their
own identity and neither key is being used as one.

## 8. The wire, and the reload race

**Finding folded in (review, severity 13):** the earlier draft had no wire contract at all.

The existing answer is reused rather than reinvented: `internal/settings` keeps a monotonic
in-memory revision bumped only on a successful mutation and carries it in the snapshot
(`settings.go:830`, `:1514`), and the renderer reconciles against it (`SettingsObserver`).

- `notify.feed.read` → `{revision, unreadCount, occurrences[], dropped?}` — the snapshot.
- `notify.feed.changed` → `{revision}` — a notification, carrying the revision only.
- `notify.feed.markRead` → `{revision}`.

The renderer fetches a snapshot, then applies `changed` only when the revision is exactly
its own + 1; any gap means a missed notification and it refetches. That closes the
reload race without an ordered event log: the snapshot is authoritative and the tail is a
hint. It also makes the class of `nocx-sb3f` harmless here — a `changed` dropped by the
refreshable queue costs one refetch, not a lost row.

Per AGENTS.md rule 5, each of the three gets a schema in `contracts/` with
`additionalProperties: false` and explicit `required`, a generated renderer type, a
`_DTOConformsToContract` test, and a `_OverTheWireConformsToContract` test off the real
socket.

**Clicking a row focuses a tab, and that resolution is the renderer's.** The occurrence
carries `(BackendID, SessionID)`; the renderer already owns the session→tab mapping. This
sidesteps `nocx-jiwq.1` (backend cannot ask the renderer to focus a tab) rather than
depending on it: the click starts in the renderer, so nothing has to travel backwards.
`nocx-jiwq.1` remains open and is still required for a **banner** click, which does start
outside.

## 9. Epics

**Finding folded in (review, severity 9):** epic 1 as first drafted promised "see what you
missed" while the only event the product can produce is `program.notify`, raised by a
program the user themselves started and is usually watching. The epic therefore ships its
own first source, or its promise is not true.

- **Epic 1 — the bell shows what you missed.** Ingress with stamping and occurrence
  identity; the bounded feed with eviction, byte budget and the visible dropped row; flood
  collapse; the three wire methods with their schemas; the activity-bar bell with unread
  count and mark-read; click-to-tab resolved in the renderer. **Plus its first honest
  source: `session.ended`** — the fact already exists in the session registry, it is
  attested, and it is genuinely something that happens while you are not looking. Today's
  `Policy` stays where it is, behind ingress, unchanged.
  **DONE WHEN:** the §1 check passes.
- **Epic 2 — the feed groups.** Expandable collapsed rows (the occurrence ids are already
  there), grouping on other axes, filtering by kind or host.
- **Epic 3 — you choose the channels.** `Policy` moves from "in front of the router" to a
  routing stage; `Disposition` changes meaning from "dropped" to "which channels fired";
  `nocx-3mniv` lands here. Most expensive by far: `internal/notify/policy_test.go` is 1144
  lines encoding the present meaning, and they are rewritten, not renamed.

**The review's objection to this split is recorded and partly accepted:** epic 3 changes
what epic 1 integrated against. The mitigation is §4 — `Ingress` and `OccurrenceID` are the
contract that survives both, and epic 1 stores occurrences rather than dispositions. Epic 1
records _that_ delivery happened and its outcome; it does not encode _how_ policy decided.

## 10. Testing

Beyond the §1 acceptance check:

- **Every external call has a failing-path test, and each has its paired success on an
  ordinary machine** (AGENTS.md rule 3): the banner sink refusing while the feed still
  records; ingress under a full feed; `markRead` racing a concurrent `Admit`.
- **Intervals, both ends.** "An occurrence exists from `Admit` returning until eviction or
  process exit"; "a collapsed run exists from its first occurrence until the collapse window
  closes"; "an unread mark exists from `Admit` until `markRead` or eviction".
- **The flood, measured, not asserted.** A test raising N occurrences from one session and
  asserting both that the feed stays inside `MaxOccurrences`/`MaxRetainedBytes` and that a
  pre-existing unread `danger` row from another session is **still there** afterwards. That
  is the property the whole flood argument exists to buy.
- **Reachability, per AGENTS.md:** `deadcode -tags gtk3 -whylive` on the feed's write path,
  not `-filter`. The scar being avoided is `ContentDB.Add`, a dead write path behind a live
  read path in the same package.

## 11. Deliberately out

Persistence of the feed across restart; the 151 existing `showToast` call sites, which are
form feedback and not notifications (`frontend/src/dispatcher.ts:447`, the saturation toast,
notably cannot travel over the transport whose failure it reports); a background mode with
the window closed (`main.go:73` terminates the process after the last window closes — its
own feature); the helper itself; per-kind and per-channel settings (`nocx-3mniv`, epic 3).

## 12. Open, and deliberately not decided here

- **Point-in-time versus stateful events.** Alertmanager-style grouping assumes
  firing/resolved with a duration; `program.notify` is an instant. nocx has a stateful class
  too — session disconnected/reconnected, port appeared/disappeared, agent run
  started/finished. Epic 1 needs no answer because it groups only consecutive runs inside a
  bounded window. Epic 2 cannot start without one.
- **Whether the dock badge, when `nocx-3a40` builds it, counts unread occurrences or unread
  _sources_.** §6 fixes the source of truth; the presentation is that bead's.
