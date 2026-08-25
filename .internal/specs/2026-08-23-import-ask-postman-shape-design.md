# The import ask asks one question, and the answer may be a URL

Date: 2026-08-23 · Bead: nocx-tpsqv (brainstorm) · Branch: `feat/api-testing`

Supersedes part of [`2026-08-22-openapi-import-design.md`](2026-08-22-openapi-import-design.md)
§7 — see §8 below. Continues
[`2026-08-23-import-collection-ask-design.md`](2026-08-23-import-collection-ask-design.md),
which took Postman's question COUNT and is here finished by taking its shape.

## What a person can do that they cannot today

Paste the URL of a Postman export — one that is only reachable through
`prod-bastion` — pick that connection, press Import, and get a collection whose
environment already routes through the same connection, so the first request
they send goes out rather than failing.

And, on the way, the ordinary case: paste the export's TEXT, or drop the file,
without typing an absolute path for either the source or the destination.

## Why this, and what it is answering

The previous spec said it plainly: what is worth taking from Postman "is not the
chrome but the **question count**". It then took the count and left the shape,
so the ask still opens as a form of two absolute paths with a drop region added
above them. The owner's reference, again, is the real dialog: one paste box
across the top, one drop region, no path fields at all.

Two path fields are two questions a person cannot answer without leaving the
app — one of them for a file they downloaded thirty seconds ago, the other for a
folder that does not exist yet. `nocx-6hg2w.14` already closed half of it by
putting `defaultRoot` on the wire; this closes the other half by removing the
fields rather than prefilling them better.

The URL is not decoration on top of that. It is the entrance that makes the
route matter: a document reachable only from inside a network is exactly the
document nobody can drop, because it never reaches their machine at all.

Boundary documents this crosses, and what they already decided:

- **AD-8 / "look for the existing answer before you write a second one".** The
  route already exists as a concept (`apicoll.Route`, §6.5: the route lives on
  the environment), as a picker (`environment-view.tsx:311`), and as a transport
  (`apisend` leasing the named profile's pooled SSH connection). None of the
  three is re-implemented here; all three are addressed.
- **AD-1.** This is control-plane work: one JSON-RPC method gains one parameter.
  No byte stream is wrapped, nothing is sniffed.
- **`contracts/README.md:76`** — params are deliberately NOT contracted; results
  are, because results are where the renderer's assumptions live. The result
  shape does not change here, so no schema changes and the existing conformance
  tests stand, with one addition named in §9.
- **`internal/apiimport`'s own rule** (`write.go:31`): the package does not reach
  the network. The fetch is therefore a seam beside it, never inside it.

## What was checked before this was written

- `validateAPIImportPostmanRaw` (`ws_api_handlers.go:1879`) already refuses
  both-or-neither BY NAME for `path` and `document`, with the reasoning written
  down: a precedence rule would let a caller's ignored parameter do nothing
  silently. Adding a third source extends that rule; it does not introduce it.
- `ImportInto` (`write.go:65`) takes an `io.Reader`. A fetched body is a reader.
  There is no new writer, no new atomicity story, no new undo.
- `parseImport` (`write.go:184`, the body in `postman.go:213`) tells the two
  entrances apart by first byte: `{` or `[` is a Postman export, and **anything
  else is handed to the curl parser**. That is load-bearing for §5.
- `httppolicy.Transport.check` (`policy.go:141`) returns for `https` with **no
  address check at all**. Its rule is about credentials in clear text, not about
  where the backend may go. Named here because the first draft of this design
  claimed the opposite, and a design that believes it is guarded is worse than
  one that knows it is not.
- `store.connections()` (`api-store.ts:322`) already answers the connection list
  the environment picker draws from; the pane already passes it
  (`api-pane.tsx:1636`).
- `proposedDestination` (`api-paths.ts:54`) and its stated rule — "AN OFFER, NOT
  A DERIVATION" — already cover the file case.
- Two ceilings already exist and are different on purpose:
  `maxAPIImportDocumentRunes` = 1 MiB bounds an export carried INLINE over the
  socket (`ws_api_handlers.go:1524`); `maxDocumentBytes` = 16 MiB bounds the
  document itself inside `apiimport` (`postman.go:19`). A fetched body crosses
  no socket, so it is bounded by the second, not the first.

## §1 — The surface

One dialog, three regions, top to bottom:

1. **The paste box.** `TextField multiline` across the top, placeholder "Paste a
   Postman export or a URL". It is the same kit control the curl ask uses, in
   the same slot grammar.
2. **The drop region.** The existing `DropZone`, unchanged, permanent, with its
   icon and its sentence — the thing the previous spec fought for and won
   (`nocx-9hb5g`). Its file picker stays.
3. **The destination summary.** One line: `Imports into: collections/acme`, with
   a pencil `IconButton`. Clicking it reveals the destination `TextField` with
   its existing Browse button in the trailing slot.

The two always-visible path fields (`api-import-postman-file`,
`api-import-postman-dest`) stop being always-visible. **The field ids do not
change** — the previous spec's rule holds: moving a field is not renaming it,
and every test addresses these fields by id.

The chosen source is shown as one line — the file's name, or the URL, with a
clearing `IconButton` — because a person who dropped the wrong file must be able
to see which file the ask is holding and take it back.

**Import stays.** Postman has no submit button because Postman has no
destination; we have one and it is editable, so there is something to confirm.
A drop and a pick fill the ask; they do not import.

## §2 — Four entrances, one source, and the rule that tells them apart

The sources are the native Wails drop, the browser drop and its file input, the
pasted document, and the pasted URL. **Exactly one is held at a time**, and a
new one visibly replaces the last. This is the wire's own rule (§4) reflected in
the surface rather than a second one: an ask that could hold two sources would
have to decide which wins, and the loser would go on being displayed.

What a paste IS, decided **once**, in one exported function beside
`proposedDestination`:

- Text whose trimmed form starts `http://` or `https://` → a **URL**.
- Anything else → a **document**, and it must start `{` or `[` after trimming;
  it is refused in the renderer otherwise, with a sentence, before any round
  trip. Blanks are already refused here for exactly that reason.

Two derivations of "is this a URL" is the `ssh`-without-a-space defect in
another costume (AGENTS.md): they would agree everywhere anybody looked.

**Why the renderer refuses non-JSON text rather than the backend.** Because
`parseImport` would not refuse it — it would hand it to the CURL parser
(`write.go:184`), and curl is deliberately out of this ask (§8). A person who
pasted a shell command would get a collection minted from it, or an error
mentioning curl in a dialog that never offered curl. The backend keeps its own
guard for the URL route (§5); this one is about not spending a round trip to
learn what the form already knew.

## §3 — The destination is an offer, and it may have nothing to offer

`api-paths.ts`'s rule continues unchanged: the field the person sees is the
truth, the backend refuses what it must, and this only fills the field in.

| Source             | Offer                                                                                                 |
| ------------------ | ----------------------------------------------------------------------------------------------------- |
| File, drop, picker | `<defaultRoot>/<basename before the first dot>` — already implemented                                 |
| Pasted document    | `<defaultRoot>/slugify(info.name)`, read with `JSON.parse` inside a `try`; any failure means no offer |
| URL                | `<defaultRoot>/<stem of the last path segment>`                                                       |

Reading `info.name` is a **syntactic offer, not a parse of the format**: it
never validates `info.schema`, never decides what kind of document this is, and
never refuses anything. The backend remains the only reader of hostile input.

When nothing can be offered — a share link like
`https://api.postman.com/collections/1234-abc` has no usable segment — the
summary line opens as the destination field, empty, with the root in front of
it, and Import stays disabled until it grows a last segment. That is the
existing `isBareRoot` predicate doing its existing job, not a second rule.

## §4 — The wire: a third source, and the route it travels

`api.import.postman` gains `url`, and `route` beside it:

```
params: { path? , document? , url? , route? , dest }
```

- Exactly one of `path`, `document`, `url`. Two or more, or none, is refused BY
  NAME — the existing sentence, widened from two to three.
- `route` is meaningful only with `url`, and `route` with anything else is
  refused by name for the same reason: a parameter that silently does nothing is
  worse than an error.
- `route` absent means direct. Its shape is `apicoll.Route`'s — `kind`,
  `profileId`, `insecureTls` — because that is what the environment already
  stores and what `apisend` already takes.
- `dest` is unchanged.

The result is unchanged: the same `unsupported` list, the same contract, the
same schema. Params are not contracted here (`contracts/README.md:76`), so the
new rules land as validator cases with tests, not as a schema.

## §5 — The fetch

A **new, narrow, injected seam** — the acquisition of a document by URL — built
over `apisend`, wired at the composition root, and named in the capability
interface beside `ImportPostman`/`ImportPostmanDocument`. Not HTTP code in the
transport handler, and not inside `internal/apiimport`, which states it does not
reach the network (`write.go:31`).

It performs one `GET`:

- No auth, no custom headers, no cookies. Nothing about this request is
  configurable, because everything configurable about it would be a second
  request builder beside the one we have.
- On the chosen route: `direct` dials from the machine running the backend;
  `connection` leases the named profile's pooled SSH connection — the same lease
  a tab uses, authorized the same way.
- Bounded: `maxDocumentBytes` (16 MiB) on the body, a timeout, and
  `httppolicy.DefaultMaxRedirects` on the chain, every hop re-checked by the
  policy that is already there.
- **The body must be a document.** The first non-space byte must be `{` or `[`;
  anything else is refused as "what is at that address is not a Postman export"
  rather than being passed to `parseImport`, where a login page would become a
  curl parse error. `Content-Type` is not consulted — the same first-byte rule
  the rest of the import already lives by.
- **Fetched completely before `ImportInto` is called.** A failed fetch writes
  nothing; there is no partial folder to reason about.

### What this does and does not do about SSRF

It is written down here rather than assumed, because the reasoning is the
deliverable:

**`httppolicy` does not close SSRF and does not claim to.** For `https` it
returns without resolving or checking a single address (`policy.go:141`). Its
rule is that a credential must not go out in clear text.

**This adds no capability the renderer does not already have.** A renderer can
write a request naming any URL (`api.request.write`) into an open collection and
send it (`api.request.send`), on any route it likes. The URL import is the same
power in one step instead of two, and with the route named by the person in
front of a picker rather than inherited from a file.

**Local and private addresses are permitted, deliberately.** Fetching
`http://localhost:8080/collection.json`, or a service reachable only behind a
bastion, is the point of the feature and not a leak in it. The decision is the
owner's, recorded here with its cost: on a backend that is not the person's own
machine — `make dev-web` over a forwarded port, and the remote backend
`nocx-83spa` exists for — a URL import reads that machine's network, and its
success, failure and timing are an oracle about it. We accept that because it is
the same reach the workbench already grants, and because a fetch that refused
private addresses would refuse the exact document this feature was built for.

**Two things follow and are not optional.** The `http://` rule still applies as
written (clear text only to loopback and private addresses), and every refusal
test gets its `https` twin (§9) — a suite that only tests the `http` refusal
would be green while the reachable-by-`https` half went unexamined.

## §6 — The collection inherits the route it arrived through

Today the importer always writes `Route{Kind: RouteDirect}` (`postman.go:244`).
When the document was fetched through a connection, the environment it mints
carries `kind=connection` with that same `profileId` instead.

The reason is a whole failure mode, not a nicety: a person who imports a
collection from behind `prod-bastion` and gets an environment routed `direct`
has a collection where every single request fails until they find the route
control in the environment editor and set by hand the thing they had just told
the import. This is the same conclusion the OpenAPI design reached
(`2026-08-22-openapi-import-design.md:19`) for the same reason.

`insecureTls` is NOT inherited. It is per-environment on purpose — "a person
turns it on for the dev environment and cannot thereby turn it on for
production" (`collection.go:126`) — and a fetch is not an environment.

## §7 — The kit

No new kit component. `TextField` (multiline, and with a trailing `IconButton`
for the destination's Browse), `DropZone`, `Select` for the connection —
the same control and the same options grammar as `environment-view.tsx:311` —
and `IconButton` for the clear and the pencil. The pencil goes into the icon
registry; a surface may not inline an SVG (`frontend/src/ui/README.md`).

The summary line **places** kit components and does not repaint them: no
`background`, `border`, `color`, `font-*`, `padding` or `box-shadow` on any of
them. If the editable-summary shape turns up a second time it becomes a kit
component then, and not before.

## §8 — What this supersedes, and what is deliberately out

**Supersedes `2026-08-22-openapi-import-design.md` §7.** That section planned a
SECOND dialog, `OpenAPIImportDialog`, carrying its own file/text/URL switch, its
own URL field and its own connection picker beside it. After this work all of
that already exists in the one ask, so OpenAPI arrives as a third document shape
inside `ImportInto` (`nocx-wacz2`) and needs no surface of its own. The openapi
spec is edited in the same commit as this one; a superseded design left standing
is how the second dialog gets built anyway.

Deliberately out, each with a bead rather than a silence:

- **curl.** It stays exactly where it is: a button in the request editor opening
  `CurlImportDialog`, minting one request into the form with no file behind it.
  It is a different outcome from a different question, and the owner's decision
  is that it does not join this ask.
- **OpenAPI documents** — needs the converter (`nocx-wacz2`, phase-2).
- **Authenticating the fetch** — `nocx-ttrlr` holds it.
- **Several files in one drop** — already refused with a sentence, unchanged.

## §9 — Testing

- **Rule 2 — the epic proves its happy path.** One end-to-end check on
  `cmd/devharness`: a URL is pasted, a connection is chosen, Import is pressed,
  the collection appears in the tree, and its environment carries that same
  `profileId`. Watched, not inferred.
- **Rule 3 — every external call fails in a test, and the invariant is an
  interval.** The fetch: 404, timeout, connection refused, a body over the
  ceiling, a redirect chain over the limit, a response that is not a document, a
  connection that cannot be leased. The write: `dest` exists, an FS failure on
  the Nth file. **Each address refusal gets its `https` twin**, plus a redirect
  from public to private, and IPv4-mapped IPv6. And the paired positive for each:
  on an ordinary machine, the fetch succeeds.
- **Rule 4 — the implementer does not write the tests alone.** Acceptance
  criteria are assertions in the beads.
- **Rule 5 — the wire is a party to the contract.** The result shape is
  unchanged, so no new schema; but `…_OverTheWireConformsToContract` for
  `api.import.postman` must also run the **URL** route, or it certifies two
  entrances out of three.
- **The seam must be reachable.** `deadcode -tags gtk3 -whylive` on the fetch
  seam's concrete method, contrasted against an unwired one — the check AGENTS.md
  spells out, because on an interface-first codebase a filter run proves nothing.
- **Frontend, at the seam a person reaches** (rule 1): the paste box exists and
  is enabled from the state a person starts in; pasting a URL reveals the
  connection picker; pasting text that is not JSON is refused without a round
  trip; a second source replaces the first; Import is disabled on a bare root.

## §10 — Open questions

None. The three that were open — whether the destination survives as a field,
whether the URL fetch is routed, and what happens to curl — are answered in §1,
§5 and §8, each by the owner, on 2026-08-23.
