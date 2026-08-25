# API testing — your collection is a folder you own, and the request goes out from wherever you are

**Status:** design, awaiting the owner's approval
**Bead:** `nocx-kklhl` (this brainstorm); the epic filed from this document
**Comes from:** the owner's question — "what if we added Postman's job to the terminal,
imported from it, and could run a request through any connection?" — narrowed in
discussion to its actual motive: **Postman ties everything to its cloud.**

---

## 1. What a person can do that they cannot today

Open a collection exported from Postman, edit a request in a form, press Send — and get
the response in the terminal. The token lives in the vault, not in the file. The request
can go out from this machine or from inside any SSH connection already open. **No account
anywhere.**

That is the whole feature. Everything below exists to keep that sentence true when the
collection arrives in a pull request, the token is wrong, the remote host is unreachable,
or the response is 200 MB.

**The end-to-end check that watches it happen** (written when the epic is created, not
at the end):

> A test server is started locally. A Postman v2.1 collection file carrying
> `{{baseUrl}}/users` and a bearer token is imported. The test asserts the token is in the
> vault and **not** in any file on disk; opens the request; presses Send; finds a run with
> `201` and the decoded body; opens raw; and asserts the token appears there as a badge
> naming the secret, never as its bytes.

## 2. In scope, decided in discussion

Everything below is in the first release, not a later one:

- **Environments and `{{var}}` substitution.** Without them a Postman collection arrives
  broken — nearly every URL in one is `{{baseUrl}}/…`.
- **Auth through the vault:** bearer, basic, api-key. Three schemes, no more.
- **Choosing where the request goes out from** — carried by the environment (§6.5), not by
  a control on the request.
- **Cookies and session state between requests.** Log in with one request, the next carries
  the cookie. HTTPie treats this as the line between a toy and a usable client, and it is.
- **Import from a Postman v2.1 export and from a `curl` command line** (§10).
- **Raw diagnostics** — the full text of both sides, trivially reachable (§11).

## 3. What this is not

Deliberately out, and each for a reason rather than a schedule:

- **JS scripting, tests and a collection runner.** Postman's sandbox is a product of its
  own. Nothing here forecloses it; nothing here builds toward it either.
- **OAuth2 flows.** Bearer, basic and api-key cover the motive; OAuth2's redirect dance
  is a separate deliverable with its own surfaces.
- **GraphQL, gRPC, WebSocket.** Different protocols, different response shapes.
- **Code generation, mock servers, response diffing between runs.**
- **Unix sockets and `docker exec` / `kubectl` contexts.** The unix socket is one
  parameter away — `x/crypto/ssh` `Client.Dial` accepts `"unix"` and we hard-code
  `"tcp"` — and it stays away until somebody asks.
- **A general scan for secret literals typed in by hand.** §11.4 searches a response for
  the two or three values **this request used**, which is a bounded, known-plaintext check.
  It does not sweep a body against the whole vault, and it does not look for credentials
  nobody here issued.
- **Splits.** Not needed, and explicitly not designed around — see §9.4.

## 4. What this crosses, and what those documents already decided

Per AGENTS.md, a design that crosses a boundary names it before it proposes anything.

| Binding           | What it already decided                                                                                                                   | What this design does with it                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-1**          | One WebSocket: raw **binary** data plane, JSON-RPC control plane. Raw PTY bytes are never wrapped in JSON; ledger _facts_ may cross       | **The precedent exists and is followed rather than argued around.** `files.read` already carries the content of an arbitrary file over the control plane — bounded, streamed, `truncated`/`binary`/`lossy` as explicit states, and **never base64**. An HTTP response body is the same kind of payload and gets the same treatment (§12.3). The earlier draft of this row argued instead that "an HTTP response is not PTY data", which is true and insufficient: it would license unbounded bulk data by exclusion. No AD is amended; the existing answer is copied |
| **AD-2**          | The Go backend is the one core                                                                                                            | The HTTP client, the parsers and the importers are backend-side. The renderer never dials                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| **AD-6**          | Single-owner state; the backend never sniffs the byte stream                                                                              | Why `curl`-over-exec was rejected (§7.2): it would make the backend parse a foreign program's output                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| **AD-7**          | `session` owns its channel and **references** a pooled `ssh` connection                                                                   | The executor takes a lease on the same pool. A request over `prod-bastion` opens a `direct-tcpip` channel on the connection already established — no second handshake, no second auth prompt                                                                                                                                                                                                                                                                                                                                                                         |
| **AD-8**          | Interface-first + DI. Variance is expressed by the interface, never by a fork inside an implementation                                    | One executor, a `Dialer` supplied by the caller (§7.1). No mode string chooses between local and remote                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| **AD-10**         | Backpressure / flow control                                                                                                               | §12.3: the response body is capped and the cap is visible in the product                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| **ADR-0011**      | Secrets are backend-owned; what travels is an opaque reference                                                                            | Imported secrets go to the vault; files carry references (§6.3, §8)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| **ADR-0021**      | The reference is what is stored, sent and resolved; **only the rendering is a chip**                                                      | §11's raw view: the backend sends spans, never values                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| **ADR-0019**      | One authoritative ledger; restore is three promises named separately                                                                      | **This design does not touch it.** The run list (§9.3) is not that ledger and makes no restore promise                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| **`nocx-52b`**    | An imported Tabby config was an ingress into a renderer-takeover chain; part of the fix was doing file selection and parsing backend-side | Both importers parse backend-side and treat their input as hostile (§10)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| **`nocx-jb20.1`** | The renderer could forge a reference to another credential's secret and have the backend spend it against a host the caller controls      | The same attack arrives here **by file**. §8 is the answer, and it is a requirement, not a nicety                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |

## 5. The shape, in one paragraph

A **collection is a folder** the user places. Inside it, one JSON file per request; secrets
are not in it. A **request** is one model with two projections — a form now, an HTTPie-style
line later — and the file is the truth, not either projection. **Sending** is one HTTP
client _implementation_ whose dialer is supplied: `net.Dialer` locally, a lease on the SSH
pool otherwise. Instances are immutable and keyed by route and cookie scope — one shared
mutable client could not hold a per-environment jar and a per-call dialer at the same time
without leaking one environment's cookies or route into another's request.
The **workbench** is a single pane holding the tree, the form and the list of runs. An
**environment** answers both "where" and "how to get there", so the two cannot drift apart.

## 6. The collection

### 6.1 A collection is a folder

The user chooses where. A new collection with no answer given goes to a default folder
under the app directory, so "just make one" works without a decision. The app remembers
**the list of opened folders**, never their contents.

This is Bruno's answer and it is the right one: it is what makes the collection shareable
through git with no account, reviewable in a pull request, and diffable. HTTPie Desktop is
the counter-example the motive rejects — local-first storage, but an account is the point
of the product, with "local-only environment" offered as the opt-out.

### 6.2 One file per request

Not one file per collection. Two people editing two requests conflict in two files rather
than in one, and the entire "shared through git" claim rests on that.

**Format: JSON.** Not because JSON is nicer to read than YAML — it is not — but because
`contracts/` already holds JSON Schemas from which the renderer's types are generated and
against which Go is validated. Testing rule 5 then works on the collection format for
free, instead of buying a hand-written parser and a second road to types.

Nested folders are real directories. `environments/` sits beside the requests.

### 6.3 Secrets are not in the files

An imported Postman environment variable of `"type": "secret"` becomes a **declared secret
variable**: the file records its name and that it is secret, and nothing else. The value
goes to the vault, and the identifier that points at it goes into the app's binding
document — never into the collection folder. §8 is why that distinction is the whole
security argument rather than a detail.

This is the part neither competitor has. Bruno's answer is a plaintext `.env` plus the
discipline of remembering a `.gitignore` line; HTTPie CLI's sessions are plain JSON under
`~/.config/httpie/sessions/<host>/<name>.json`, so whatever auth is in them is in the
clear. Here the folder is safe to commit **by construction rather than by discipline** —
and that is the argument that survives contact with a security review.

### 6.4 One model, two projections

The form ships first; the HTTPie-style line ships second. But the model is designed for
both from day one, and the invariant is written as a test **before** the second projection
exists:

```
parse(render(r)) == r      for every request r
```

Touch the form, the line redraws. Touch the line, the form redraws. They cannot diverge
because there is nothing between them to diverge.

Two surfaces may never own the same input (AGENTS.md), and this does not: the **file** is
the owner, both surfaces are projections of it, and neither is the truth. The failure that
bought that rule — two derivations of "am I in an ssh context" that agreed everywhere
except the one moment that mattered — is a _second derivation_, which is exactly what a
single model with a proven round trip prevents.

**Where the line cannot reach**, it names rather than loses: a multi-line body, a binary,
an awkward auth lives in a file and the line carries `data=@body.json`. That is HTTPie's
own answer to its own limit, and it keeps the invariant total.

### 6.5 The environment answers "where" and "how"

A Postman environment already answers _where_ — it holds `baseUrl`. Ours also answers
_how to reach it_:

```
dev   →  http://localhost:3000        direct
prod  →  https://api.internal         through prod-bastion
```

An environment file therefore holds plain values, the **names** of its secret variables,
and the route. It holds no secret values and no identifiers for them (§8).

**An unresolved variable blocks the send and names itself.** Not the literal `{{braces}}`
sent to the server, not an empty string quietly substituted. Both of those turn a missing
value into a request that goes out and fails somewhere further away, and the empty-string
version is worse than the failure it hides: an `Authorization: Bearer ` header is a
plausible-looking request that teaches the wrong lesson about why it was rejected.

Three consequences, and the third is the reason:

1. There is no connection dropdown on the request. One concept, not two.
2. Switching environment moves the address and the route together, in one motion.
3. **A production request cannot accidentally go out around the bastion**, because the
   address and the route are one record. This is the shape AGENTS.md's rule about two
   derivations of one fact is written against: they agree everywhere until they do not.

## 7. The executor

### 7.1 One client, a supplied dialer

One HTTP client implementation. Locally its transport dials with `net.Dialer`; through a
connection it dials with a lease on the SSH pool.

**The seam needs an adapter, and it has one limit worth naming.** `tunnelConn.Dial`
(`internal/ssh/ssh_tunnel.go:116`) is `Dial(addr string) (net.Conn, error)` — it returns a
`net.Conn`, which is what matters, but it is **not** the signature
`http.Transport.DialContext` takes and it accepts **no context**. So a small adapter
supplies the network parameter and drops the context, and the consequence is honest:
**cancelling a request does not interrupt a blocked remote channel open.** The mitigation
is a bounded dial deadline on the adapter plus closing the connection when it arrives late,
so a cancelled run can never acquire one. A context-aware tunnel dial seam would remove the
limit; adding one is not in this design's scope, and pretending the limit is not there
would be worse than the limit.

**And "any connection already open" means the profile's pooled connection, not a
particular live one.** `TunnelConn` goes through `acquirePooled`: it shares when the
resolved pool key matches and otherwise establishes one. So a send may authenticate anew
rather than ride a connection you can see in a tab. That is the correct behaviour — a route
names a destination, not a window — but the sentence in §1 must not be read as a promise
about a specific open session.

Local and remote are therefore **not two strategies**; they are one executor with a
different dialer. That is AD-8's form: variance expressed by the interface, no flag inside
the implementation, and a third dialer can be added without editing a `switch`.

The remote side resolves the name (a `direct-tcpip` channel is opened to a host:port the
remote resolves), TLS is ours end-to-end, and the response arrives as a value — status,
headers, timings, bytes.

### 7.2 Why not `curl` on the far side

Three reasons, each from something already in this repo:

- **Footprint.** `2026-08-10-remote-footprint-consent-design.md` makes what nocx leaves on
  a remote host a governed matter. `curl -H "Authorization: Bearer …"` puts the token in
  `argv`, visible to every `ps` on that host, and in the shell's history.
- **Parsing a foreign byte stream** is what AD-6 exists to prevent. Over a `net.Conn` the
  response is structured; out of `curl` it would have to be excavated.
- **Dependency on the host.** A distroless container has no `curl`.

### 7.3 The HTTP policy is not written twice

`internal/assistant/httpguard.go` already solves this problem: `http://` is permitted only
to loopback and private addresses, enforced **on every connection and every redirect hop**
rather than in a form, and `Authorization` is dropped on any origin change. Its file
comment names the four reasons it cannot be a form check, and all four apply here verbatim.

So the guard is **extracted into a shared package with the policy as a parameter**, not
copied and not re-derived. Writing a second HTTP client with its own rules is the
"second answer to one question" AGENTS.md forbids, and the two would agree until they did
not.

## 8. A collection file cannot name a secret at all

`nocx-jb20.1` closed a hole where the renderer could name **another** credential's secret
and have the backend spend it against a host the caller controls. Nothing in this design
lets the renderer do that. The question is whether a **file** can, because a collection
folder is meant to arrive in a pull request.

**It cannot, and the reason is the format rather than a check.** A request file carries a
variable name — `{{token}}` — and never a secret identifier. The binding from a variable to
a stored value is **owned by the app**, held in its own document beside the vault, and keyed
by (collection, environment, variable). A hostile file can write `{{token}}` and gets
whatever the reader bound in their own environment; it has no way to spell "the password
behind the production SSH profile", because there is no syntax in which a file names a
secret.

An earlier draft of this section had files carrying opaque vault references and proposed a
scope on the secret plus a resolver that refused cross-scope reads. That draft was wrong
twice over, and both are worth recording so the idea is not re-proposed:

- **There is no single resolution funnel to put the check in.** Secrets are fetched by id
  from `internal/capability/secret.go:288,377`, `internal/credential/resolver.go:107,109`,
  `internal/ssh/ssh_auth.go:288,297` and `internal/contentkey/contentkey.go:146`, and
  `connection.Resolver` merely copies references into `ssh.ConnectConfig` for SSH to read
  later. A check in one of those is a check five other paths do not make.
- **It answered the wrong question.** Refusing a reference the file should never have been
  able to write is a guard bolted onto a format that permits the attack. Removing the
  spelling removes the attack, and needs no vault change at all: no field on
  `SecretRecord`, no document version bump, no migration of existing records, no scoped
  capability.

### 8.1 Where the value lives

In the vault, as every other secret in this product does — a second secret store would be a
second lifecycle, a second backup and a second unlock, which is what AD-8 exists to
prevent. What changes is only **who holds the identifier**: the app's binding document, not
the collection file, and never the renderer (ADR-0011 is untouched).

So sharing a collection shares variable _names_, and each person supplies their own values.
That is what a shared collection should do anyway — the alternative is teams mailing each
other production tokens, which is the practice this feature should make unnecessary rather
than convenient.

### 8.2 The two-store write shrinks but does not vanish

Writing a value still touches the vault and then the binding document. The failure is now
benign in one direction and repairable in the other: a vault value with no binding is dead
weight nobody can reach, and a binding naming a value that is not there is a variable that
reports itself unresolved and blocks the send (§6.5's rule, not a new one). Write the value
first, the binding second. §12.2 states the invariant with both ends.

## 9. The surface

### 9.1 It is a pane

`2026-08-16-tabs-panes-and-blocks-design.md`: "A pane is the thing that survives … and a
tab is the strip entry that shows one or more panes together." The pane is the durable
identity; the tab is a cheap wrapper minted and destroyed by dragging. `SurfaceRegistry`
already builds `PaneContent`, so surfaces are already panes.

**The API workbench is one pane** — a singleton, like Settings and Connections.

### 9.2 One workbench, not a pane per request

A request opens **inside** the workbench, not as its own tab. A pane per request would
clutter the strip and make switching between fifty requests worse than a list is. The
analogy to the Files panel opening a file in a tab does not hold: files are opened two at a
time and closed; requests are switched between constantly.

The activity-bar icon **opens or focuses the workbench pane** and does not expand the side
panel — the pattern `sidebar.tsx` already describes for its bottom zone, and the one the
Settings gear uses today. The tree lives in the workbench and is not duplicated into a
sidebar view; two trees would be two owners of one selection.

```
┌──┬───────────────────────────────────────────────────────────────┐
│▣ │ ▸terminal   ▸ssh prod   ▸API ×                                │
│▤ ├─────────────────────┬─────────────────────────────────────────┤
│▥ │ acme-api          ▾ │ POST   {{baseUrl}}/users                │
│▦ │ ▾ users             │ ┌ Headers ─ Body ─ Auth ─ Params ─────┐ │
│▧ │   ● create     POST │ │ Authorization   ⟦API_TOKEN⟧         │ │
│  │   ○ list       GET  │ └─────────────────────────────────────┘ │
│  │ ▾ auth              │                               [ Send ⏎ ]│
│  │   ○ login      POST ├─────────────────────────────────────────┤
│  │                     │ ● POST /users         201  184ms  1.2KB │
│  │ ─────────────────── │ {  "id": "usr_8f21"  }            [raw] │
│  │ environment: prod ▾ │ ─────────────────────────────────────── │
│  │ → through prod-bast │ ● POST /users         422   96ms   310B │
└──┴─────────────────────┴─────────────────────────────────────────┘
```

### 9.3 The run list is not the terminal's blocks

Decided in discussion: the list of runs is the workbench's own, not `createCommandBlock`
and not the terminal's ledger. Two consequences, and the second is a gain:

- It makes **no restore promise**. ADR-0019 is untouched; nothing here re-decides restore.
- The body is not constrained to SGR rows, so JSON renders as a **collapsible tree**,
  headers as a **table**, and raw as its own view. Had the run been a terminal block, all
  three would have been coloured text.

Components come from `frontend/src/ui/` per its README inventory; anything missing is added
there as a variant. A surface may place a kit component and may never repaint it.

### 9.4 Splits are not designed for

Several panes in one tab is the model's planned end state, not something this feature
needs. Because the workbench is a **pane**, it inherits splits when they arrive — drag it
beside a terminal pane and the "fire a request, watch the log" layout exists with no change
here.

The point is negative: **nothing is built to work around their absence.** The rejected
alternative — the workbench in the sidebar panel — was precisely such a workaround, and it
would have become permanent, because moving out of a sidebar into a pane later is a rewrite.

## 10. Import

Two entrances, one converter: a Postman v2.1 collection (plus its environment) and a
`curl` command line both produce the same request model.

**Both are parsed backend-side and both are hostile input.** `nocx-52b` is the direct
precedent: an imported Tabby config was step 1 of a renderer-takeover chain, and one item
of its fix was to do file selection and parsing in the backend.

**`curl` is parsed, never executed.** Our own quoting and continuation handling; no
`sh -c`, ever.

**A `curl` line usually carries a live token** in `-H 'Authorization: Bearer …'`. On import
that is detected and offered to the vault; `secret-candidate.ts` already does this kind of
recognition.

**Supported flags are a bounded set** — `-X`, `-H`, `-d`/`--data-raw`/`--data-binary`/
`--data-urlencode`, `-F`, `--json`, `-u`, `-b`, `-G`, `-L`, `-k`, `--compressed` — and
anything outside it is **refused out loud, itemised to the user**. Flags that change the
meaning of the request (`--proxy`, `--cert`, `-o`) may not be silently dropped: AGENTS.md,
"a soft degrade must be visible in the product, not only in a log".

**An import never fires a request.**

## 11. Diagnostics: raw, and how a secret appears in it

Making the full text of both sides trivially visible is a first-class requirement, not a
debugging affordance.

```
 ● POST /users                    201   184ms                 [pretty]
 ── request ─────────────────────────────────────────────────────────
 POST /users HTTP/1.1
 Host: api.internal
 Authorization: Bearer ⟦API_TOKEN⟧
 Content-Type: application/json

 {"email":"a@b.c","name":"A"}
 ── connection ──────────────────────────────────────────────────────
 through prod-bastion → 10.0.3.17:443    TLS 1.3
 dns 4ms · connect 21ms · tls 38ms · ttfb 118ms · total 184ms
 ── response ────────────────────────────────────────────────────────
 HTTP/1.1 201 Created
 …
```

### 11.1 A badge means "exactly this secret"

The owner's design, and it is better than redaction: the badge is not a curtain, it is
**evidence**. Three states, never two:

| State                                              | Rendered as                                                                                                                                                                                        |
| -------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The bytes still equal the secret                   | Badge `API_TOKEN` — this is exactly the secret you named                                                                                                                                           |
| They differ, but this is a span **we** substituted | Damage badge: `API_TOKEN · truncated, 24 of 214 bytes`. **The bytes are not shown** — the shape of the damage says more than its first characters, and a truncated token is a prefix of a live one |
| Not our span                                       | Ordinary text. It is not a secret                                                                                                                                                                  |

The middle row is the case that makes the whole thing safe: without it, "show the text when
it does not match" would print the beginning of a live credential in the clear.

### 11.2 The backend sends spans, never values

Only the backend can compare bytes against the vault: ADR-0011 keeps values away from the
renderer and that is not negotiable. So the raw text crosses **already segmented** —
literal runs, plus spans annotated with the name of a secret. The value never crosses.

This is ADR-0021's shape exactly ("the reference is what gets stored, sent and resolved;
only the RENDERING is a chip") and the same mechanism `unresolved-redactions.ts` already
uses: spans with positions.

**On the request side it is cheap because there is nothing to search.** We placed the
secret and know where. So this is verification — _are the bytes we put there still the
secret's_ — not a scan.

### 11.3 The response needs a different mechanism, and saying otherwise was wrong

An earlier draft said the response is marked "by the same mechanism". It cannot be. The
mechanism is a list of **placements** — offsets where the sender itself substituted a
value — and a placement in the request says nothing about whether a server echoed those
bytes back, or where. Marking a response requires **searching** it.

So the response gets a **bounded known-plaintext search**: the body and headers are
searched for the two or three secret values this request actually used, and nothing else.
That is a different thing from the general scan §3 excludes — the set is tiny, known, and
issued by us — and it is worth its cost for the reason below.

Left unspecified, this hides a real leak: without the search, a server that echoes your
token into an error message ships it to the renderer as **ordinary text**, in a view whose
entire purpose is to show everything.

Three things the search must state rather than discover in production: it runs on the
decoded body only (a compressed or chunked frame is searched after decoding, never before);
it makes no attempt at transformed spellings (a base64-wrapped or URL-escaped token is
**not** found, and the design says so rather than implying coverage it does not have); and
overlapping matches collapse to the longest.

### 11.4 What the response search finds

APIs echo credentials back in error text. Marking the response this way
surfaces it immediately, and that is a finding people otherwise miss for years.

## 12. Failure

### 12.1 Every external call fails in a test

DNS, TCP connect, TLS handshake, response read, acquiring the pool lease, unlocking the
vault, reading and writing files, parsing an import. Mechanical, cheap, and the
highest-yield check available (AGENTS.md testing rule 3). For each one, the paired test:
"and on an ordinary machine it succeeds".

### 12.2 A partial import

Forty files written, the forty-first fails. **The import assembles in a temporary directory
and arrives by one rename.**

**The temporary directory is created inside the destination's parent** so the rename is
within one filesystem — across filesystems it is a copy and not atomic — and an existing
destination is refused rather than replaced. Files and the containing directory are synced
before the rename, and the parent after it; without that the crash window is "the rename
landed and the contents did not".

Ordering for a secret variable: **the vault value is written first, the binding second.**

An earlier draft justified this ordering by claiming an orphaned vault record is collected
by `Reconcile`. That is the opposite of what `Reconcile` does. `internal/vault/journal.go:119`
clears the journal entry and **keeps** the secret whenever a catalogue record exists, and
`CreateNamed` writes value and record in the same save (`vault.go:1122`) — so a crashed
import leaves exactly the record reconciliation treats as complete. The correct reading is
that the vault deletes an orphan only when no record landed **and** no metadata target was
attached.

So the ordering stands but for the opposite reason: a vault value with no binding is
unreachable dead weight, harmless and collectable later, while a binding pointing at a
value that is not there is a variable that reports itself unresolved and blocks the send.
The benign failure is the one we choose to risk.

The invariant, with both ends named, as testing rule 3 demands — and note that the closing
event is real, which it was not in the earlier draft:

> A vault value for a secret variable exists from before its binding is written until the
> collection that declares that variable is deleted. Deleting a collection deletes the
> bindings it owns and the values those bindings alone reference.

### 12.3 A large response

**The existing answer is `files.read`, and it is copied rather than re-derived.** That
method already carries the content of an arbitrary file over the control plane, and its
contract says how:

> bounded, streamed content of one file. The provider reads **at most the effective limit
> plus one byte and never the whole file**, so the memory guard holds for a 40 GB file …
> the parameter can only lower the **2 MiB** ceiling

Four properties come with it, and all four apply here unchanged:

- **Streamed, never buffered whole.** The reader stops at the limit plus one byte, so the
  guard holds regardless of what the server sends. A cap applied after reading the body is
  not a cap.
- **2 MiB by default**, and a parameter that can only lower it. This closes the spec's
  third open question by inheritance rather than by opinion: a different number here would
  be a second answer to a question the product already answers.
- **`truncated`, `binary` and `lossy` are explicit fields**, so the three sentences stay
  distinct — the same distinction ADR-0019 §7's eviction rule makes.
- **Binary is never base64.** `files.read` sends empty text and says "binary file, N bytes".
  A base64 body would be exactly the bulk-payload-in-JSON that AD-1's prohibition is about,
  arriving through a side door.

The run offers the full body as a **file** rather than as a larger result: a response too
big to show is a thing to save and open, not a bigger frame.

## 13. Security

- **Imported names, URLs and headers are hostile** and reach the DOM as text. The CSP and
  the `textContent` discipline from `nocx-52b`'s fix already cover the mechanism.
- **A file cannot name a secret** (§8). The load-bearing one, and it is a property of the
  format rather than a check somebody has to remember to run.
- **Redirects** drop `Authorization` on any origin change — inherited from the guard in
  §7.3, not re-implemented.
- **An import never sends a request.**

### 13.1 A collection folder is a hostile path, not only hostile content

Backend-side parsing makes the _contents_ safe to read. It does nothing about **which file
gets read**, and an earlier draft of this design missed that entirely while calling the
folder hostile in the same paragraph.

The renderer must therefore never be able to address a file by path twice. **Opening a
collection mints a backend-held handle**; every later call names the handle and a path
relative to it, and `root` is never accepted again. This is the shape `internal/filesystem`
already uses, and `files.read`'s contract shows why identity has to be provider-canonical
rather than lexical — two symlinks to one file are one file.

Within the handle, four rules, each with a test:

- A relative path containing `..`, an absolute path, or a path escaping the root after
  symlink resolution is refused. Refused, not clamped: a silently rewritten path is a
  surface that reports success for something it did not do.
- Request and environment files are opened **without following symlinks**. A collection
  from a pull request may not read `~/.ssh/id_ed25519` by pointing a request file at it.
- Writing never follows a symlink either. `internal/storage/document.go:159` already
  refuses exactly this — the existing answer, not a new one.
- The root may be replaced between open and read. The handle is re-validated per operation
  rather than trusted from open time; the failure is reported, not papered over.

## 14. Testing

- A JSON Schema in `contracts/` for every JSON-RPC result this surface adds, and — the one
  that matters — `…_OverTheWireConformsToContract`, the real result off the real socket. Its
  absence is what let `vault.status` omit `defaultProvider` for months while both suites
  stayed green.
- `parse(render(r)) == r`, written before the second projection exists.
- The end-to-end check of §1.
- The failure-path tests of §12.1, each paired with its success case.
- For the epic: `deadcode -whylive` on the executor's entry point, contrasted against an
  unwired symbol in the same package. Not `-filter`, which cannot report a dead method
  behind a live interface — and every module here is behind one.

## 15. Open questions

1. **The default folder for a new collection** — a fixed directory under the app dir, or
   asked once and remembered as a preference (Bruno does the latter, defaulting to
   `~/Documents/bruno`).
2. **Cookie and session persistence between requests** is in scope, but where the jar
   lives — per environment, per collection, or per workbench — is not decided here.
3. ~~The body cap needs a number.~~ **Closed by §12.3** — it is `files.read`'s 2 MiB
   ceiling and its streamed reader, inherited rather than chosen. A different number would
   have been a second answer to a question this product already answers.
