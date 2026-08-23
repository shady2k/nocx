# OpenAPI import — the service publishes its contract, so the collection already exists

**Status:** design, approved by the owner in the session that produced it
**Bead:** `nocx-3lyeo` (this brainstorm); the epic filed from this document
**Comes from:** the owner's question — "what if a collection could be made from an OpenAPI
spec, from a file, from text, or by URL?" — narrowed in discussion to its actual motive,
which is the third of those and not the first two.

---

## 1. What a person can do that they cannot today

Point nocx at the OpenAPI document of a service that only they can reach, and get a
collection they can send from immediately.

**DONE WHEN** a person gives the panel `https://api.internal/openapi.json` — an address
reachable only through `prod-bastion` — picks that connection in the import dialog, and
receives a collection whose folders are the spec's tags, whose requests are its
operations, and whose one environment already carries the `servers` URL and that same
connection. They open a request and press Send, having edited nothing but a value.

**The end-to-end check that watches it happen**, written now rather than at the end:

> `cmd/devharness` serves a fixture OpenAPI 3.1 document over loopback. The test imports
> it by URL, asserts the folder tree matches the spec's tags, opens one request, presses
> Send, and reads the response.

### 1.1 Why this is worth building, stated as the thing it beats

Postman import covers "I already have a collection". `curl` import covers "I have one
request". The case neither covers is the common one for an internal service: **there is
no collection and there never was**, because the service publishes `/openapi.json` and
nobody maintains anything by hand beside it.

And the reason it belongs in nocx specifically rather than in any API client: **Postman
cannot reach `http://10.0.3.17:8080/openapi.json`.** We already can, and we already send
there. "Point at the swagger address of a service only you can reach, and get forty
requests that go out through the same connection" is not an import feature; it is the
product's own thesis applied to the one document that already describes the work.

## 2. What this is not

Each for a reason rather than a schedule.

- **Request bodies generated from schemas.** Decided in discussion, and the reasoning is
  worth keeping because it will be re-proposed: a generated body never makes a request
  send correctly — the values are not in the document, so `""` and `0` earn a 400 either
  way. Its only value is carrying the _shape_, so the spec need not stay open in a second
  window. That value is real but small, and its cost is the largest single piece of the
  converter: recursive schema walking with `allOf`/`oneOf`, `nullable`, nested objects and
  arrays, and cycle detection. Without it, `$ref` resolution stays shallow — one or two
  hops to see what an operation is — and the converter is a fraction of the size.
  A body that **is** in the document (`example`/`examples`) is carried verbatim, because
  that costs a marshal and no recursion at all.
- **Swagger 2.0.** A second document shape: `basePath`, `definitions`, body parameters,
  `consumes`/`produces`. Refused by name, with its own bead, never in silence.
- **External `$ref`** — a `$ref` naming another URL. Not fetched. See §5.
- **OAuth2 and OpenID Connect flows.** Named in `Unsupported`, auth set to none.
- **Re-import or sync of a changed spec.** A collection does not remember where it came
  from, and giving it that memory is a separate deliverable with its own surfaces.
- **Authenticating the spec fetch itself.** See §4.2.

## 3. What this crosses, and what those documents already decided

Stated before what to build, per AGENTS.md.

| Document                                        | What it already decided                                                                      | What we do                                                                                                                                         |
| ----------------------------------------------- | -------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| api-testing design §10                          | An import is hostile input; parsing happens backend-side                                     | Held: the converter lives in `internal/apiimport`, never in the renderer                                                                           |
| api-testing design §10, `TestPackageNeverExecs` | **An import never fires a request**; the package cannot reach `net/http` or `os/exec` at all | Held **literally**. The fetch is not in the package and not in an import — see §4                                                                  |
| api-testing design §8                           | A collection file names a variable, never a secret                                           | Satisfied for free: a spec carries no credential values, so `BindWriter` receives nothing on this path                                             |
| api-testing design §7.3                         | The `http://` address rule is written once, in `internal/httppolicy`                         | Reused through `apisend`, not re-derived                                                                                                           |
| api-testing design §13.1, `names.go`            | A collection folder is a hostile path; a name is never validated, it is **minted**           | Reused unchanged, including `maxItems`, `maxFolderDepth`, `maxNameRunes` and the Windows reserved names                                            |
| api-testing design §6.5                         | The route lives on the environment beside the address, so the two cannot drift               | Constrains §4.3: we create one environment, never a guessed route for a server we did not reach                                                    |
| api-testing design §12.2                        | An import is one atomic arrival: staging, sync, one rename                                   | Reused unchanged                                                                                                                                   |
| AD-1                                            | The control plane is JSON-RPC over the one socket                                            | One new method, its schema in `contracts/` in the same commit                                                                                      |
| AD-8                                            | One owner per behaviour, every module behind an interface                                    | The new code is a converter and a fetch caller. Path minting, atomic arrival, route resolution and the HTTP policy are all called, none re-written |

## 4. The shape

### 4.1 The fetch is a send, and that is what keeps the invariant literal

The one thing in this feature that needed deciding: `apiimport` is forbidden to touch the
network, by a test that asserts the package cannot reach `net/http` at all. A URL entrance
appears to contradict it.

It does not, because **the fetch is not part of the import.** It is an ordinary send,
performed before the import begins, whose response body is handed to the importer as an
`io.Reader` — exactly the reader a file or a paste would have produced.

And the sender is `internal/apisend`, not a new HTTP client. `apisend` already answers the
whole question: reach a host through a route (`RouteID` = the chosen connection), under
`httppolicy`'s address rule, re-checking every redirect hop and dropping the credential
across an origin change, with a ceiling on the response size. A bespoke fetch in
`capability` would be a second answer to "how do we reach a host through a connection" —
the failure AGENTS.md names, where two derivations agree everywhere you look and disagree
somewhere you did not.

```
capability.ImportOpenAPI(ctx, src, dest)
  ├── src.Kind == url  → apicoll.Request{GET, src.URL, Auth: none}
  │                      → apisend, RouteID = src.ConnectionID
  │                      → the response body as io.Reader
  ├── src.Kind == file → the file, opened through the injected FS
  └── src.Kind == text → strings.NewReader(src.Text)
                            ↓
                  apiimport.ImportInto(ctx, fs, bind, dest, r)
```

`ImportInto` already tells its document apart by its first byte; it gains a third
recognised shape. Postman and OpenAPI are both JSON, and they are told apart by the
top-level key — `openapi` against `item`/`info.schema` — never by a guess.

The connection offered in the dialog is a **global** connection (`internal/connection`),
not an environment. This is what dissolves the chicken-and-egg: an environment is a file
inside a collection that does not exist yet, whereas connections exist already and an
environment merely references one. The connection the spec was fetched through is then
written into the collection's environment, because it is almost certainly the connection
the requests will be sent through.

### 4.2 The fetch carries no credential

A spec behind `Authorization` returns 401, and that is **said plainly**, naming the status
and telling the person to download the document and import it as a file. AGENTS.md: a soft
degrade must be visible in the product, not only in a log. An empty collection with a
success message on it would be the worst available outcome.

### 4.3 YAML

`gopkg.in/yaml.v3` is already a direct dependency, so accepting `openapi.yaml` costs one
branch — the document does not begin with `{`, so it is parsed as YAML — and no new
supply-chain surface. It is worth having: for the file and paste entrances, YAML is the
commoner spelling.

## 5. The conversion

One operation becomes one `apicoll.Request`, written as a `.json` file like every other.

- **Folder** — the operation's first `tag`, as one segment minted by `slug()`. Untagged
  operations land in the root. Tags are how the spec's own author grouped the API and how
  Swagger UI already shows it.
- **Name** — `summary`, else `operationId`, else `METHOD /path`. The display name goes
  into the JSON untouched; the **path** is minted from it, so `../../etc` is a folder
  called `etc` rather than a traversal that has to be caught.
- **URL** — `{{baseUrl}}` plus the path, with `/users/{id}` rewritten to `/users/{{id}}`.
  One grammar for "a hole in the address", the same rewrite the Postman importer already
  performs on `:id`.
- **Path parameters** become the request's **own** `Variables`, empty and enabled. The
  reason is already written into the model: `id` belongs to the request, because two
  requests legitimately want different ones and an environment carrying both would be a
  place to keep other people's values.
- **Query parameters** become `Query` rows — enabled when `required`, present but disabled
  otherwise. A disabled row is a row the user keeps.
- **Header parameters** become `Headers`.
- **Body** — `example` or the first of `examples`, carried verbatim when present; empty
  otherwise. `Content-Type` from the media type.
- **Auth** — the operation's `security`, else the document's. `http`+`bearer` →
  `Auth{Kind: bearer, Var: "{{<scheme key>}}"}`; `http`+`basic` → basic; `apiKey` →
  apikey, carrying its `name` and `in`. The variable **name** is added to the
  environment's `SecretVars`; no value exists to bind. `oauth2` and `openIdConnect` →
  `AuthNone` plus a line in `Unsupported`.
- **Environment** — exactly one, built from the server whose origin matches the URL the
  document was fetched from (for a file or a paste, `servers[0]`). It carries `baseUrl`
  and the route the person chose. Server variables such as `https://{region}.api.com` are
  filled from their `default`.
- **The other `servers`** are itemised in `Unsupported` — "server `https://staging…` was
  not imported". They deliberately do not become environments: their route can only be
  guessed, and §6.5 exists precisely so that an address and its route cannot drift. A
  "production" environment silently routed through the bastion that happened to be
  selected is the exact shape that bites.

## 6. Failure, and every bound as a number

| Situation                                                                                | What happens                                                                                                                                                                                                                   |
| ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `dest` already exists                                                                    | Refused, never replaced — `ImportInto`'s existing rule                                                                                                                                                                         |
| The fetch fails, times out, exceeds its ceiling, or returns something that is not a spec | **Nothing is written at all.** The fetch completes before `ImportInto` is called                                                                                                                                               |
| The document exceeds its byte ceiling                                                    | Refused by name                                                                                                                                                                                                                |
| Operations exceed `maxItems`                                                             | Refused by name; the constant already exists in `names.go`                                                                                                                                                                     |
| Depth, name length, Windows reserved names                                               | Already minted by `slug()`/`names.go`                                                                                                                                                                                          |
| `swagger: "2.0"`                                                                         | Refused by name — "this is Swagger 2.0; nocx imports OpenAPI 3.0 and 3.1" — with its own bead                                                                                                                                  |
| An external `$ref`                                                                       | Itemised in `Unsupported`, per operation. The request still lands, minus what the ref would have added. **Not fetched:** the content of a downloaded document must not decide where we go next, least of all through a bastion |
| A cyclic internal `$ref`                                                                 | Bounded by a stated depth                                                                                                                                                                                                      |
| A write fails on the forty-first file                                                    | Staging plus one rename, already built; `FS` is injected so this is a test rather than a hope                                                                                                                                  |

## 7. The surface

> **Superseded, 2026-08-23, by
> [`2026-08-23-import-ask-postman-shape-design.md`](2026-08-23-import-ask-postman-shape-design.md).**
> There is no second dialog. The one import ask already takes a file, a pasted
> document or a URL, already carries the connection picker beside the URL, and
> already proposes its destination — so an OpenAPI document arrives as a third
> document shape inside `ImportInto` (`nocx-wacz2`) and needs no surface of its
> own. What follows is the plan that was replaced, kept because the reasoning
> below about kit components and the `Unsupported` list still holds for the one
> ask; only the second dialog is gone.

`PostmanImportDialog` is the precedent and its grammar is repeated. `OpenAPIImportDialog`
carries a source switch (file / text / URL), a URL field with the connection picker beside
it, and the destination folder field with Browse. Kit components only — a surface may
place them and may not repaint them. The `Unsupported` list is shown after the import, as
it already is for Postman.

## 8. Testing

- **Rule 2 — the epic proves its happy path.** The end-to-end check in §1, on
  `cmd/devharness`.
- **Rule 3 — every external call fails in a test.** 404, timeout, over-ceiling, not-JSON,
  a 401 on the fetch, and an FS failure on the Nth file.
- **Rule 4 — the implementer is not the only author of the tests.** Acceptance criteria
  are written as assertions in the beads, not as prose.
- **Rule 5 — the wire is a party to the contract.** `contracts/api.import.openapi.json`,
  plus `…_DTOConformsToContract` and `…_OverTheWireConformsToContract`.
- `deadcode -whylive` on the converter: it must be reachable from `main`, not only from
  its own tests.

## 9. Open questions

None outstanding. Swagger 2.0 and authenticating the spec fetch are both deliberately out
(§2) and each gets a bead rather than a silence.
