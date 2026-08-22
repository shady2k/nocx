# The rest of "a program in a pane can reach you" — decisions and four tasks

**Epic:** `nocx-jiwq`, whose eight built children shipped the notification core, the router,
`notify.raise`, the macOS banner and the policy. Four are open, and they are what stands
between the product and epic 3 (`nocx-3mniv`), whose acceptance criterion names a toast and
an OSC 9 that do not yet exist.

**Not in scope, and it cannot be:** `nocx-n0z7` (our own bundle identifier) is already
correct in both plists at commit `3c26d512`. Its last criterion is "read `CFBundleIdentifier`
back from the BUILT bundle rather than trusting the template", and that needs macOS. This
work is on Linux, so the bead stays open and the epic stays open with it. Do not close
either on this machine.

## The decisions

### D1 — The backend never learns which tab holds a session. It asks for a session.

`nocx-jiwq.1` is written against `wailsadapter`'s `Host`, which wants `Lookup (sessionID →
tab)` and `Focus (tabID → error)`. The first half should not exist. Epic 1 settled the
ownership question in the other direction and shipped it: the **renderer** owns session →
tab (`PaneManager.findBySession`), the backend cannot do it at all, and `Attribution.Tab` is
a WebSocket connection id rather than a tab (`nocx-wyp3p` is that defect).

So the capability the backend gains is **"focus the pane holding session S"**, carrying a
session id and nothing else. The renderer resolves it with the lookup it already owns. That
deletes `Lookup` from the port, leaves exactly one implementation of session → tab in the
product, and makes the banner click land through the same path the feed row already uses.

### D2 — A toast is a sink like any other, reached through a port

The router is the only holder of "where" (ADR-0029 §2.3), and a sink may never select its
target. A toast is therefore not a special case in the renderer: it is a `Sink` whose
`Deliver` hands the event to a **port** the transport implements, exactly as `HostSink`
hands its event to `AttentionHost`. `LeavesMachine()` is false.

The port is bound late through a holder, for the same reason `HostHolder` exists: the table
is fixed at `NewRouter`, and what arrives later is the implementation, never the
destination. Until it binds, delivery reports unavailable — visibly failed, never silently
dropped. `internal/notify` stays Wails-free and transport-free; the port is an interface it
declares and something else satisfies.

### D3 — A delivery that fails after acceptance becomes a row in the feed

`nocx-r6pxp` had no good answer in August because there was nowhere to put the fact.
There is now: epic 1 built the thing whose entire purpose is remembering what happened while
you were not looking, and a notification that was accepted and then failed to arrive is
precisely that.

Two constraints, and the second is the one that bites:

1. The failure row names what failed, which channel, and why, and it carries the ORIGINAL
   event's attribution so it lands beside the notification it is about.
2. **A failure row may never itself produce a failure row.** Otherwise one broken sink turns
   into an unbounded feed of complaints about being broken. The failure is admitted to the
   feed **directly**, never raised through the pipeline that produced it — it is a fact the
   centre records, not an event the router routes. Say so in the code, and test it by
   failing the sink that would carry it.

### D4 — The bound on `notify.raise`'s text lives in the schema

`nocx-jiwq.3`: `maxNotifyTextRunes = 4096` exists in the Go validator and nowhere in
`contracts/notify.raise.schema.json`, so the wire contract and the validator disagree about
whether a bound exists at all. AGENTS.md rule 5 makes the schema a party to the contract, so
the schema grows the bound, the renderer's generated type carries it, and the Go validator
enforces the same number by reading it from one place rather than restating it.

Runes against characters: the schema's `maxLength` counts UTF-16 code units in JSON Schema's
own definition, and Go counts runes. Pick one, say which, and make the Go side's message
name the same unit the schema does — a bound that means two different things on two sides of
the wire is the defect rule 5 exists to prevent.

---

### Task A — the renderer can be asked to focus a session (`nocx-jiwq.1`)

**Acceptance Criteria:**

- A backend→renderer control push carries a session id and asks for its pane to be focused.
  It is a JSON-RPC notification on the existing control plane (AD-1); no new socket.
- The renderer resolves the session with `PaneManager.findBySession` — the ONE lookup, not a
  second one — and focuses the pane, or does nothing when the pane is gone.
- `wailsadapter`'s host no longer takes a `Lookup`; the port carries a session id.
- A banner clicked while the app is in the background focuses the right pane, watched by a
  test at the seam a user reaches.
- With no renderer attached the push is dropped without error and without blocking the
  delivery path — a click cannot be honoured by a renderer that is not there, and pretending
  otherwise would stall a sink.

### Task B — OSC 9 and OSC 777, the bell, and the toast sink (`nocx-c6ef`)

**Acceptance Criteria:**

- `ESC]9;text` raises `notify.raise`; **`ESC]9;4;state;pct` raises nothing** — it is the
  ConEmu progress protocol, and a naive handler on 9 turns a progress tick into a
  notification. Test the trap directly, with a sequence of progress ticks producing zero
  raises.
- `ESC]777;notify;title;body` raises with both fields; a malformed 777 raises nothing.
- Do NOT register a second handler for OSC 1337 — `xterm.ts` already owns that ident and
  dispatches from inside; one handler owns one ident.
- The bell source comes from the existing `onBell`.
- A `ToastSink` in `internal/notify` delivers through a port (D2), bound through a holder,
  and the renderer presents it with the kit's existing `showToast`. Do not build a second
  toast.
- The sink's failure path: with nothing bound, delivery reports unavailable and the router
  records a failed delivery.

### Task C — a failure after acceptance reaches the feed (`nocx-r6pxp`)

**Acceptance Criteria:**

- A delivery that fails after `notify.raise` has answered produces one occurrence in the
  feed naming the channel and the reason, attributed to the original event's session.
- A failure of the delivery that would carry THAT row produces nothing — no second row, no
  recursion. Test it by failing the sink that would carry it.
- The occurrence is admitted to the feed directly, not raised through the router.
- The existing behaviour is unchanged for a failure that happens BEFORE acceptance: it still
  reaches the caller as a JSON-RPC error.

### Task D — one bound, named once (`nocx-jiwq.3`)

**Acceptance Criteria:**

- `contracts/notify.raise.schema.json` declares the bound on `title` and `body`; the
  generated renderer type carries it; the Go validator enforces the same number without
  restating it as a second literal.
- The unit is named and the same on both sides, and the rejection message says which unit it
  counted.
- A test sends a payload one unit over the bound and gets the documented refusal, and one
  exactly at the bound and gets through.

## Order

```
D (bound, contracts)      independent, small
A (focus a session) ──► B (OSC 9/777, bell, toast sink)
C (failure to the feed)   independent of A and B; needs epic 1's feed, which is landed
```

B needs A only for the click; the toast half is independent.
