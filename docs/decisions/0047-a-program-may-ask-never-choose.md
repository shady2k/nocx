# ADR-0047 — A program may ask; it never chooses

- **Status:** Accepted
- **Date:** 2026-08-13 (rescoped and accepted 2026-08-14 — see §1.1)
- **Related:** AD-1 (this extends its ledger-facts enumeration), AD-6 (untouched, §4.2),
  AD-8 (one owner per behaviour), ADR-0024 (untouched, §4.1), ADR-0017 (a connection
  references a secret), ADR-0011 §4 (delete-cascade ordering), beads `nocx-uz7f`,
  `nocx-ywhp` (consent for program-initiated actions — the neighbour to this decision,
  §4.5), `nocx-sb3f`.
- **Design that consumes this:** `.internal/specs/2026-08-13-notification-system-design.md`
- **Consulted:** an adversarial review (codex, four rounds, 2026-08-13/14) and the owner.
  §5 records what was taken and what was left.
- **Formerly ADR-0029:** renumbered 0047 on 2026-08-28 (`nocx-yjvg5`) — the number was shared with [ADR-0029 — A proposed keystroke is bound to what makes it meaningful](0029-a-keystroke-is-bound-to-what-makes-it-meaningful.md), which is older and keeps it.

## 1. Context

**A program printing an escape sequence can make nocx act outside the terminal.** This is
already true and already shipped: a program printing `OSC 52` causes nocx to write the
system clipboard. `OSC 9` / `OSC 777` — a request to raise a notification, which is how a
TUI reaches you from a machine you got to over ssh — is the second instance, and there will
be a third.

Today the category has no written rules. OSC 52's consent lives in `nocx-ywhp`, which is
unbuilt, and nothing says what such an effect may and may not reach. Each new one therefore
arrives as its own argument, which is how a second model for one decision gets built.

**This ADR decides the category once**: what a program-initiated effect may cause, and what
it may never choose. The notification system is the case that forced it and is specified
separately; nothing here is specific to notifications.

### 1.1 What this ADR claimed on 2026-08-13, and why it was wrong

The first version claimed the notification design **violated** AD-1 and needed a carve-out
from ADR-0024. Both were overstated, and the owner caught it.

AD-1's `cwd/OSC/prompt markers do not cross the control plane` bullet is justified by AD-6
and was already superseded in substance by the 2026-08-02 ledger-facts amendment
(`nocx-m64b`), which permits exactly this shape: the renderer owns the VT state, derives a
typed fact from it, and sends it as a schema-checked record. That amendment's own three
remaining prohibitions are all satisfied here — no raw bytes wrapped in JSON, no raw OSC
sequence crossing, and no fact carrying the output it was derived from (an OSC 9 payload is
never rendered as output at all). What is genuinely missing is narrower: the amendment
enumerates _facts about a completed command_, and says of itself "this is not a general
licence", so a new record kind is added to the enumeration deliberately or not at all.

ADR-0024 needs nothing. Its prohibitions are about **authority** — opening or completing an
execution attempt, forging an exit status, taking input ownership, writing history. A
notification asserts none of them. The argument that "a banner is an effect caused by
output, even if it forges no state" proves too much: OSC 52 is an effect caused by output,
it is in the product, and nobody has read ADR-0024 as forbidding it.

So the permission is one line in an enumeration. **The value of this document is the
constraints**, which existed nowhere before it.

## 2. Decision

**A program may ask nocx to present something. It may never choose where that goes.**

### 2.1 AD-1 — extend the ledger-facts enumeration

> The enumeration of typed facts that may cross the control plane (amended 2026-08-02) is
> extended to **presentation requests** (2026-08-14, ADR-0047): a parsed, expressly
> registered terminal sequence by which a program asks nocx to present a message. The
> amendment's test is unchanged and still governs — no raw bytes wrapped in JSON, no raw
> OSC sequence crossing, no fact carrying the output it was derived from — and the rules in
> §2.2 bind every such record.

### 2.2 The rules every program-initiated effect obeys

**Provenance is structural, not validated.** The record carries exactly the fields the
renderer is authorised to originate, plus addressing. For a presentation request that is
`sessionId`, `title` and `body`. **`sessionId` is addressing, not attribution**: one
WebSocket multiplexes many server-assigned sessions (AD-1), so the record must say which
terminal instance parsed the sequence, and the backend rejects an id not live on that
connection. Every attributed field — the kind, the trust class, the severity, the host, the
tab, the timestamp — is **absent from the wire** and stamped by the backend from the method
invoked and its own session registry. A schema proves a record's shape, never who assigned
a field, so validating authorship was never available.

**Ingress authority is closed**, which is what makes "stamped from the method invoked" mean
anything: no renderer-callable method may produce an `attested` event (§3). A
renderer-originated request is always `programRequest`; a heuristic adapter always
`heuristic`; attested facts originate only at the backend boundary that authenticates them.

**Noninterference, stated differentially because that is what a test can check.** For any
two schema-valid payloads differing only in their presentation fields, route resolution
MUST produce the same sinks, the same target identifiers, the same credentials, the same
destination and the same method. Resolution completes **before** any sink-level validation;
a sink that then rejects a payload records an attempted delivery that failed and never
removes itself from the resolved set. Redaction runs only after resolution, and size and
encoding validation apply to the redacted, encoded payload: a redaction-induced size change
may change delivery success and can never re-resolve a destination.

**Destination.** Program-supplied fields MUST NOT participate in URL construction in any
position — scheme, userinfo, host, port, path, query or fragment. The rule is absolute and
admits no per-provider exception; a provider whose only endpoint places message content in
the URL is out of scope until it offers one that does not. Redirects are refused. The URL
derives only from user configuration, trusted metadata, and secrets below.

**Secret-bearing URLs.** A user-configured secret MAY occupy one fixed URL component that a
provider requires, declared by a typed preset — never the scheme, userinfo, host, port,
query or fragment, because a host secret leaks through DNS and TLS SNI before any code of
ours runs. The composed URL is then secret-bearing: never persisted composed, never logged,
never named in an error (a failure names its target), and never followed across a redirect,
which is the second and independent reason redirects are refused. **The error carries it
even when we do not** — Go's `http.Client.Do` returns `*url.Error`, whose `Error()` prints
the URL, and Go redacts a userinfo password but not a path segment, so one `%w` is enough
to log a bot token. Such errors, redirect errors, transport errors and recovered panics are
classified inside the adapter and replaced with a target-named error before anything logs,
wraps, traces or presents them; the request carries no ambient proxy, tracing hook or
wrapping transport that could observe the composed URL.

**Retention, with bounded ownership and a closing event.** nocx MUST NOT write the
presentation fields or a composed secret-bearing URL to a database, configuration, a durable
queue or a structured log. Each sink invocation is synchronous, takes a finite-deadline
context, must stop retaining request data and return when cancelled, and may not publish a
callback after returning. **Expiry cancels the invocation; the closing event is the
invocation's return** — a timeout is a logical result, not proof a goroutine stopped
writing. Finalization is one-shot, so a late result is ignored. The router holds global
limits on in-flight invocations, on queued instances and on retained bytes; admission
beyond a limit is a visible failed delivery, never an unbounded queue. No retry survives
process exit: delivery is at-most-once.

### 2.3 The value's category, and who may encode it

Program-supplied fields are **untrusted presentation data** — never control data, never an
opaque blob to concatenate.

**Routing is resolved once, in the router, before any sink is invoked.** A sink receives an
immutable resolved destination and may never select a target, credential, method, retry,
alternate destination or redirect target. Stated here rather than left to AD-8 because
destination work otherwise settles naturally inside sink code.

A sink MAY validate size and Unicode, redact under the single redaction policy, and encode
the fields for one fixed, sink-declared position, using a context-specific encoder — JSON
string encoding, HTTP field-value validation, raw-body writing, percent-encoding. It MUST
NOT concatenate a field into protocol syntax, nor parse it as a URL, template, header set,
method, credential, routing key or configuration. **CR, LF and NUL are rejected in every
position, in every sink**, and an invalid payload fails visibly rather than falling back to
concatenation.

## 3. Trust classes

Every event carries a `trust` class, stamped by its source adapter and absent from the wire:

| `trust`          | Origin                                                     | May reach                                          |
| ---------------- | ---------------------------------------------------------- | -------------------------------------------------- |
| `attested`       | a backend boundary that authenticated the fact             | every sink; and completion subscriptions           |
| `programRequest` | a parsed, registered sequence the program printed          | every sink; never a completion subscription        |
| `heuristic`      | an inference from stream content (e.g. a title transition) | local attention only. Never a network destination. |

**Only an `attested` event may match, activate, deliver through, or consume a completion
subscription.** Saying only that a guess cannot _close_ one leaves it able to match, borrow
the subscription's sinks and its explicit-gesture suppression override, deliver, and leave
it armed so the real completion delivers again. The matching attested event consumes the
subscription once every selected invocation has returned, whatever the outcome — consuming
only on success would leave it armed to fire on something unrelated, and a failure is
already visible.

The routing table is **default-deny**: a `(kind, trust)` pair reaches a sink only where a
row says so, and one table governs both the ordinary route and any ad-hoc subscription
route. That is the enforcement boundary, rather than an accept-declaration protocol on
every sink.

**An inference is not a request.** Classifying a terminal title into "working" / "idle" and
then asserting that work finished is nocx making a claim from hostile content — which
_would_ offend ADR-0024, unlike an explicit request. Hence the third class, and hence its
inability to reach anything outside the machine.

## 4. Rationale

**4.1 Why ADR-0024 is untouched.** Its prohibitions are about authority: opening or
completing an execution attempt, forging an exit status, taking input ownership, writing
history. Presenting an attributed message asserts none of them, and OSC 52 is the standing
precedent — an effect caused by output, in the product, never read as an ADR-0024
violation. §3's third class is where the line actually sits: an inference that becomes
nocx's own claim is the thing ADR-0024 forbids, and it is forbidden here too.

**4.2 Why AD-6 is untouched.** AD-6 forbids the backend to interpret the **bytes a session
produces**, and it does not: the sequence is parsed in the renderer and the backend never
sees it. What the backend does with the resulting typed value — validate, redact, coalesce,
bound, encode — is interpretation of a value, which AD-6 never spoke to. An earlier
revision defended this as "it receives a string and does not parse it", which was an
overclaim of the same family this document exists to remove.

**4.3 Why the differential test, and not "the backend never interprets the value."** That
was the first version and it failed twice: unenforceable, since no test proves the absence
of semantic interpretation across future code; and false inside its own design, since
coalescing and redaction must inspect the value. An equivalence over pairs is a property
test that can fail. Two details are load-bearing: it ranges over every **schema-valid**
input rather than every input a sink would accept — otherwise the generator excludes the
oversized and invalid-encoding cases that could diverge — and it compares **route
resolution**, ordered before sink validation, so a size rejection is a failed delivery
rather than a changed sink set.

**4.4 Why the destination rule is absolute.** "The destination is user-configured" sounds
sufficient and is not, because _where_ is undefined — authority? initial URL? redirect
target? Naming every component makes it checkable. An intermediate revision then granted
one provider a payload-derived path segment and contradicted itself in the same document:
percent-encoding prevents injection, it does not make the destination independent of the
payload. The resolution was to change the endpoint rather than weaken the rule, and it cost
nothing — every provider considered can carry its message outside the URL. An absolute rule
is worth more than a rule with one exception, because only the first can be tested by
construction rather than by remembering the exception.

**4.5 Why not a program-scoped grant (`nocx-ywhp`).** That epic decides **consent** for an
action a program takes on the user's behalf — writing their clipboard. This ADR decides
**capability**: what the effect may reach once permitted. They are neighbours, not
duplicates, and the split is deliberate: consent is per program and per action, capability
is per class and holds regardless of who was granted what. A per-program dialog layered
over a routing rule the user already configured would be a second model for one decision.
When `nocx-ywhp` is built, it grants against the classes named here rather than inventing
its own.

**4.6 The residual risk, accepted deliberately.** Once a user routes program-sourced events
to a network destination, any host they reach can put text on their phone. It is bounded by
three things and no more: the destination is theirs, the rate limit is per session and
kind, and every message carries backend-stamped attribution naming the tab, host and
session. Without attribution, `Ваш банк: подтвердите вход` from a hostile MOTD is
indistinguishable from the user's own alert — which is why attribution is stamped by the
backend and is not on the wire.

## 5. What the reviews supplied, and what was left

**Round two** supplied the differential noninterference test, the enumerated destination
rule, "untrusted presentation data" in place of "opaque string", per-context encoders, and
the rule fixing who owns the attributed fields. **Round three** caught that one provider's
path segment contradicted the destination rule in the same document, that "only an attested
event closes a subscription" left the privileged route reachable by a guess, and that
"until every sink completed or failed" was a predicate with no guaranteed closing event.
**Round four** returned the first non-rejection, confirmed that overruling round two's
weaker destination rule was correct, and found that removing the attributed fields from the
wire had also removed **addressing** — which AD-1's multiplexing makes indispensable — plus
the `*url.Error` leak, that a deadline is not proof of a stopped goroutine, and that a
per-key debounce bounds one key rather than aggregate memory.

**The owner** supplied the two things no review round found: that a provider secret can
itself occupy a URL position, which is a third category with its own consequences; and that
this ADR's original framing was wrong — AD-1's ledger amendment already permits this shape,
ADR-0024 is about authority rather than effects, and OSC 52 is the standing precedent
(§1.1). That correction is why the document is a third of its previous length.

Left, on purpose:

- **A per-sink accept-declaration protocol.** Replaced by one default-deny routing table
  authoritative for both routes (§3), which is the enforcement boundary the recommendation
  was asking for. Default-deny makes drift fail closed.
- **An AD-6 amendment.** §4.2. The reasoning changed twice; the conclusion did not.
- **A twenty-item partial-failure enumeration.** The demand is legitimate and is AGENTS.md
  rule 3; the list was not, since half described flows the design does not have. The design
  enumerates its own intervals.

## 6. Consequences

- **AD-1's enumeration gained one line** (`docs/architecture.md:110`); nothing else in the spine changed.
- **A presentation request's schema in `contracts/`** carries `sessionId`, `title` and
  `body` and nothing else, with `additionalProperties: false` — that absence is what makes
  provenance structural rather than validated.
- **A property-based differential test gates the router**, ranging over schema-valid inputs
  and comparing resolution before sink validation.
- **A network sink cannot be built from a generic template engine.** Each preset declares
  its payload position, its encoder, and where its secret goes.
- **Secret containment is testable**: a sentinel secret must be absent from logs, returned
  and wrapped errors, traces, panic output and fixture failure output.
- **`nocx-ywhp` grants against these classes** when it is built, rather than defining its
  own.
- **The next program-initiated effect reuses this document** instead of arguing itself into
  existence. That is the whole reason it is an ADR and not a section of a spec.
