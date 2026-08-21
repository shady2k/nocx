# API testing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** Open a collection exported from Postman, edit a request in a form, press Send, and get the response — with the token in the vault rather than in the file, and the request going out either from this machine or from inside an SSH connection already open. No account anywhere.

**Architecture:** A collection is a folder of JSON files the user places; `internal/apicoll` owns the model and the folder. `internal/apisend` owns one `http.Client` whose dialer is supplied — `net.Dialer` locally, a lease on the existing SSH pool otherwise — so local and remote are one code path, not two strategies. Secrets live in the vault under their own scope and the resolver refuses references outside it. The surface is one singleton **pane** holding the tree, the form and the list of runs.

**Tech Stack:** Go 1.x (`github.com/shady2k/nocx`), `golang.org/x/crypto/ssh`, JSON-RPC 2.0 over the existing WebSocket control plane, Solid + TypeScript frontend, JSON Schema in `contracts/` with generated renderer types, vitest + `go test -race` + Playwright.

**Spec:** `.internal/specs/2026-08-21-api-testing-design.md` — every section reference below (§N) points there.

## Global Constraints

- **AGENTS.md is binding.** Read it before the first edit; the testing rules and the git-authority rules are not optional.
- **A task that adds a Go package lands with the wiring that makes it reachable** (`nocx-z7s6`). Every task below is a vertical slice for exactly this reason: package + composition-root wiring + transport method + the surface that calls it. A task that leaves a package callable only from its own tests cannot pass the pre-commit deadcode ratchet, and the ratchet is the hook, not the brief.
- **Every JSON-RPC result gets a JSON Schema in `contracts/`** with `additionalProperties: false` and an explicit `required`, plus both conformance tests. `npm run contracts:check` runs in pre-commit from `frontend/`.
- **Secrets never cross to the renderer.** ADR-0011. What crosses is an opaque reference or, for the raw view, a span annotated with a name.
- **A test may not depend on timing.** Wait on an observable state change, never on a duration.
- **The kit owns appearance.** Read `frontend/src/ui/README.md` and list `frontend/src/ui/` before building any control. A surface may place a kit component and may never repaint it.
- **Commit subject ends with the bead id.** Body is prose explaining what was wrong, what changed, and why this way rather than the obvious alternative.
- **A worker runs the unit tests for the files it changed and stops there.** `make ci-full`, the containerized jobs and the e2e suite belong to whoever integrates.

---

## File Structure

**New Go packages**

| Path                   | Responsibility                                                                                                                                          |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/apicoll/`    | The collection model, the on-disk JSON format, the folder reader/writer, the opened-folder list. Knows nothing about HTTP or transport.                 |
| `internal/apisend/`    | One `http.Client`, the `Dialer` seam, request assembly from the model, response capture including timings and the raw spans. Knows nothing about files. |
| `internal/apiimport/`  | Postman v2.1 and `curl` → the `apicoll` model. Hostile input; one converter, two entrances.                                                             |
| `internal/httppolicy/` | The guard extracted from `internal/assistant/httpguard.go`, with the policy as a parameter.                                                             |

**Modified Go**

| Path                                       | Change                                                                                         |
| ------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| `internal/vault/meta.go:18`                | `SecretMeta` gains a scope; the closed kind vocabulary gains the API kinds                     |
| `internal/assistant/httpguard.go`          | Becomes a thin caller of `internal/httppolicy`                                                 |
| `internal/capability/`                     | New `APIOperation` interfaces, following `internal/capability/snippet.go`                      |
| `internal/transport/ws_api_handlers.go`    | New. Template: `internal/transport/ws_snippet_handlers.go`                                     |
| `internal/transport/ws_config_handlers.go` | `regResponder` entries for every `api.*` method                                                |
| `internal/app/app.go`                      | Composition root: construct the stores, the sender and the importers; `transport.WithAPI(...)` |

**New frontend**

| Path                                | Responsibility                                     |
| ----------------------------------- | -------------------------------------------------- |
| `frontend/src/api/api-client.ts`    | The JSON-RPC client for `api.*`, framework-neutral |
| `frontend/src/api/api-store.ts`     | The one list every part of the surface reads       |
| `frontend/src/api/api-pane.tsx`     | The pane: tree, form, run list                     |
| `frontend/src/api/request-form.tsx` | The form projection of one request                 |
| `frontend/src/api/run-list.tsx`     | Runs, pretty/raw toggle                            |
| `frontend/src/api/raw-view.tsx`     | Raw text with the secret spans rendered as badges  |

**Modified frontend**

| Path                               | Change                                                            |
| ---------------------------------- | ----------------------------------------------------------------- |
| `frontend/src/surface-registry.ts` | `SURFACE_ID_API` constant                                         |
| `frontend/src/main.tsx:348`        | `registry.register(SURFACE_ID_API, …)` and the activity-bar entry |

---

## Task Ordering

```
T1 ──▶ T2 ──▶ T3
        │
        ├────▶ T4 ──▶ T5 ──┬──▶ T6 ──▶ T8 ──▶ T9 ──▶ T10 ──▶ T11
        │                  └──▶ T7 ──┘
```

T3 and T4 are independent of each other. T6 and T7 are independent of each other. Everything else is a chain.

---

### Task 1: A collection folder opens and its requests are listed

**Files:**

- Create: `internal/apicoll/collection.go`, `internal/apicoll/folder.go`, `internal/apicoll/opened.go`
- Create: `internal/apicoll/collection_test.go`, `internal/apicoll/folder_test.go`
- Create: `internal/capability/api.go`
- Create: `internal/transport/ws_api_handlers.go`
- Create: `contracts/api.collections.list.schema.json`, `contracts/api.collections.open.schema.json`
- Create: `frontend/src/api/api-client.ts`, `frontend/src/api/api-store.ts`, `frontend/src/api/api-pane.tsx`
- Modify: `internal/transport/ws_config_handlers.go` (regResponder entries)
- Modify: `internal/app/app.go` (composition root)
- Modify: `frontend/src/surface-registry.ts`, `frontend/src/main.tsx:348`
- Test: `internal/transport/ws_contract_test.go`, `frontend/src/api/api-store.test.ts`

**Interfaces:**

Produces — every later task consumes these names verbatim:

```go
package apicoll

// Request is the model. Both projections (form now, line later) are views of it.
type Request struct {
    ID      string            `json:"id"`
    Name    string            `json:"name"`
    Method  string            `json:"method"`
    URL     string            `json:"url"`
    Headers []Header          `json:"headers"`
    Query   []Param           `json:"query"`
    Body    Body              `json:"body"`
    Auth    Auth              `json:"auth"`
}

type Header struct { Name, Value string; Enabled bool }
type Param  struct { Name, Value string; Enabled bool }
type Body   struct { Kind string; Text string; FileRef string } // Kind: "none"|"raw"|"form"|"file"
type Auth   struct { Kind string; SecretRef string; User string } // Kind: "none"|"bearer"|"basic"|"apikey"

// Collection is a folder. Requests are addressed by their path within it.
type Collection struct {
    Root     string
    Name     string
    Requests []RequestRef
}
type RequestRef struct { RelPath, Name, Method string }

type Folder interface {
    Open(root string) (Collection, error)
    ReadRequest(root, relPath string) (Request, error)
    WriteRequest(root, relPath string, r Request) error
}

type OpenedList interface {   // the app remembers folders, never their contents
    List() ([]string, error)
    Add(root string) error
    Remove(root string) error
}
```

JSON-RPC surface added here: `api.collections.list` (no params) and `api.collections.open` (`{path}`).

**Acceptance Criteria:**

- A folder containing a manifest and two request files opens, and both requests appear in the tree with their method and name.
- A folder with no manifest is refused with a named error, not a panic and not an empty collection.
- A request file whose JSON does not match the schema is refused **by name**, and the other requests in the folder still list — one bad file does not hide the collection.
- The opened-folder list survives a restart; the contents are re-read from disk, never cached.
- `api.collections.list` validates against its contract schema **off the real socket**, not only as a marshalled struct.
- **The round trip holds on the file, from day one:** `Read(Write(r)) == r` for every request, including empty headers, a nil body and a `{{var}}` in every field. This is the half of §6.4's invariant that is testable before the line projection exists, and writing it now is what stops the model being quietly shaped by the form.
- **The default location for a new collection is decided in this task and written into the bead** (spec §15, open question 1). Whichever way it goes — a fixed directory under the app dir, or asked once and remembered — leaving it open in the code is not an option, because "where did my collection go" is answered by the first line of code that guesses.
- `deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/apicoll.folder.Open' ./...` prints a path from `main`, and the contrast against a deliberately unwired symbol in the same package is recorded in the bead.

- [ ] **Step 1: Write the failing folder test**

```go
// internal/apicoll/folder_test.go
func TestOpen_ListsRequestsAndKeepsGoingPastOneBadFile(t *testing.T) {
    root := t.TempDir()
    write(t, root, "nocx-collection.json", `{"schemaVersion":1,"name":"acme"}`)
    write(t, root, "users/create.json", `{"id":"a","name":"create","method":"POST","url":"{{baseUrl}}/users"}`)
    write(t, root, "users/broken.json", `{ not json`)

    c, err := newFolder().Open(root)
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    if c.Name != "acme" {
        t.Errorf("Name = %q, want acme", c.Name)
    }
    if len(c.Requests) != 1 {
        t.Fatalf("Requests = %d, want 1 — a broken file must not hide the good ones", len(c.Requests))
    }
    if c.Requests[0].Method != "POST" || c.Requests[0].Name != "create" {
        t.Errorf("Requests[0] = %+v", c.Requests[0])
    }
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/apicoll/ -run TestOpen_ListsRequests -v`
Expected: FAIL — `undefined: newFolder`.

- [ ] **Step 3: Write the model and the folder reader**

`collection.go` holds the structs above. `folder.go` walks the root, decodes each `*.json` that is not the manifest, collects decode failures into a returned slice rather than aborting, and returns the manifest name.

- [ ] **Step 4: Run it and confirm it passes**

Run: `go test ./internal/apicoll/ -race -v`

- [ ] **Step 5: Add the capability operation and the transport handlers**

`internal/capability/api.go` follows `internal/capability/snippet.go` exactly: an `APIOperation` whose `Run` hands the service to the callback guard-bound. `internal/transport/ws_api_handlers.go` follows `internal/transport/ws_snippet_handlers.go` — a constructed handler type holding the operation and the `Responder`, never the `*WSServer`.

Collections live under the profile directory the way the snippet library does, so this belongs to the **config conflict domain**: hold the config gate, and not the vault gate (no secrets are resolved in this task).

- [ ] **Step 6: Write the contract schemas and both conformance tests**

`additionalProperties: false` plus an explicit `required` on every object. Then in `internal/transport/ws_contract_test.go`, both shapes — the DTO test and, the one that matters, `…_OverTheWireConformsToContract`, which validates the real result off the real socket.

- [ ] **Step 7: Wire the composition root**

`internal/app/app.go` near line 552, where `snippetStore`/`snippetSvc` are built, and `transport.WithAPI(...)` near line 823 where `WithSnippets` is passed.

- [ ] **Step 8: Register the pane and the activity-bar entry**

`frontend/src/surface-registry.ts` gains `SURFACE_ID_API`. `frontend/src/main.tsx:348` registers it beside `SURFACE_ID_SETTINGS`, singleton-keyed. The activity-bar entry **opens or focuses the pane and does not expand the side panel** — the bottom-zone pattern `sidebar.tsx` describes and the Settings gear uses (§9.2).

- [ ] **Step 9: Run the gates for what changed and commit**

```bash
go test ./internal/apicoll/ ./internal/transport/ -race
cd frontend && npm run test -- api && npm run contracts:check && npm run typecheck
git add -A && git commit   # subject ends with the bead id
```

---

### Task 2: Send a request from this machine and see the response

**Files:**

- Create: `internal/httppolicy/policy.go`, `internal/httppolicy/policy_test.go`
- Create: `internal/apisend/sender.go`, `internal/apisend/dialer.go`, `internal/apisend/sender_test.go`
- Create: `contracts/api.request.send.schema.json`
- Create: `frontend/src/api/request-form.tsx`, `frontend/src/api/run-list.tsx`
- Modify: `internal/assistant/httpguard.go` (becomes a caller of `httppolicy`)
- Modify: `internal/transport/ws_api_handlers.go`, `internal/app/app.go`, `frontend/src/api/api-pane.tsx`

**Interfaces:**

Consumes: `apicoll.Request` from Task 1.

Produces:

```go
package apisend

// Dialer is the seam. Local and remote are ONE sender with a different
// dialer, never two strategies and never a flag inside one (AD-8).
type Dialer interface {
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

type Sender interface {
    Send(ctx context.Context, r apicoll.Request, d Dialer) (Response, error)
}

type Response struct {
    Status     int
    Headers    []apicoll.Header
    Body       []byte
    Truncated  bool          // §12.3 — a capped body is a STATE, never a silent short read
    Timings    Timings
    TLSVersion string
    RemoteAddr string
}
type Timings struct { DNS, Connect, TLS, TTFB, Total time.Duration }
```

**Acceptance Criteria:**

- A request against a local test server returns status, headers, decoded body and non-zero `Total` timing.
- The response appears in the run list with status, elapsed time and size, and a second Send **adds a second run rather than replacing the first**.
- `internal/assistant`'s existing guard tests still pass **unchanged** after the extraction — the extraction may not alter the assistant's policy.
- `http://` to a public address is refused; `http://` to loopback is allowed; the check happens on the connection, not on the form. (Inherited from the extracted guard, asserted here as a fresh test that the wiring is live.)
- A test exists for each of: DNS failure, connection refused, TLS handshake failure, a server that closes mid-body — and each has its paired "and on an ordinary machine it succeeds".

- [ ] **Step 1: Write the failing send test**

```go
// internal/apisend/sender_test.go
func TestSend_ReturnsStatusHeadersAndBody(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(201)
        _, _ = w.Write([]byte(`{"id":"usr_1"}`))
    }))
    defer srv.Close()

    got, err := newSender().Send(context.Background(),
        apicoll.Request{Method: "POST", URL: srv.URL + "/users"},
        &net.Dialer{})
    if err != nil {
        t.Fatalf("Send: %v", err)
    }
    if got.Status != 201 {
        t.Errorf("Status = %d, want 201", got.Status)
    }
    if string(got.Body) != `{"id":"usr_1"}` {
        t.Errorf("Body = %q", got.Body)
    }
    if got.Timings.Total == 0 {
        t.Error("Total timing is zero — the diagnostics of §11 depend on it")
    }
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/apisend/ -run TestSend_Returns -v`
Expected: FAIL — `undefined: newSender`.

- [ ] **Step 3: Extract the guard into `internal/httppolicy` FIRST**

Move `guardedTransport` and its resolver out of `internal/assistant/httpguard.go` with the policy as a **parameter**, not a mode string. The four reasons in that file's header comment for why this cannot be a form check are carried across verbatim — a future reader who does not know them will "simplify" it into a form validator.

`internal/assistant` becomes a caller. Its existing tests must pass untouched; if any needs editing, stop and say so rather than editing it.

- [ ] **Step 4: Write the sender over the extracted policy**

`httptrace` supplies the timings. The `Dialer` is threaded into `http.Transport.DialContext`.

- [ ] **Step 5: Run and confirm both packages pass**

```bash
go test ./internal/apisend/ ./internal/httppolicy/ ./internal/assistant/ -race
```

- [ ] **Step 6: Add `api.request.send`, its schema, both conformance tests, and the run list**

This method resolves secrets from Task 5 later, so its operation holds **both** the config gate and the vault gate from the start — adding a gate to a live method later is a change of concurrency behaviour, not a refactor.

- [ ] **Step 7: Commit**

---

### Task 3: Send from inside an SSH connection

**Files:**

- Create: `internal/apisend/ssh_dialer.go`, `internal/apisend/ssh_dialer_test.go`
- Modify: `internal/app/app.go` (hand the pool connector to the sender)

**Interfaces:**

Consumes: `apisend.Dialer` (Task 2); `tunnel.Connector` (`internal/tunnel/tunnel.go:110`) and `ssh.TunnelConn` (`internal/ssh/ssh_tunnel.go:12`).

Produces: `apisend.NewSSHDialer(conn ssh.TunnelConn) Dialer`.

**Acceptance Criteria:**

- A request whose environment names a connection is dialled through `tunnelConn.Dial`, and the test asserts the **remote** side resolved the name — the address the sender was given is not resolved locally.
- The dialer takes a **lease on the existing pool** and opens no second SSH connection: the test asserts the pool's connection count is unchanged across a send. (AD-7: `session` references, never owns.)
- Losing the connection mid-request surfaces as a named error on the run, distinguishable from an HTTP error and from a user cancel.
- A closed lease refuses to dial rather than dialling locally — a silent fallback to the local dialer would send a production request around its bastion, which §6.5 exists to make impossible.

- [ ] **Step 1: Write the failing test that the local dialer is never used**

```go
func TestSSHDialer_NeverFallsBackToLocal(t *testing.T) {
    lease := &fakeTunnelConn{dialErr: ssh.ErrTunnelConnClosed}
    _, err := apisend.NewSSHDialer(lease).DialContext(context.Background(), "tcp", "api.internal:443")
    if !errors.Is(err, ssh.ErrTunnelConnClosed) {
        t.Fatalf("err = %v, want ErrTunnelConnClosed — a spent lease must refuse, never dial locally", err)
    }
}
```

- [ ] **Step 2: Run it, confirm it fails, implement, confirm it passes, commit**

---

### Task 4: Environments and `{{var}}` — and the route lives there too

**Files:**

- Create: `internal/apicoll/environment.go`, `internal/apicoll/substitute.go`, and their tests
- Create: `contracts/api.environments.list.schema.json`
- Modify: `frontend/src/api/api-pane.tsx` (the environment picker and the route line)

**Interfaces:**

Produces:

```go
type Environment struct {
    Name      string            `json:"name"`
    Values    map[string]string `json:"values"`
    SecretRefs map[string]string `json:"secretRefs"` // name → opaque vault reference
    Route     Route             `json:"route"`
}
// Route answers "how to get there" in the SAME record as baseUrl answers
// "where" (§6.5). Two records would drift; one cannot.
type Route struct {
    Kind      string `json:"kind"`      // "direct" | "connection"
    ProfileID string `json:"profileId"` // empty for "direct"
}
```

**Acceptance Criteria:**

- `{{baseUrl}}/users` with `baseUrl=http://localhost:3000` resolves to `http://localhost:3000/users`.
- An unresolved `{{var}}` **blocks Send and names the variable**. It does not send the literal braces and it does not send an empty string.
- Substitution happens in URL, headers, query and body alike — a test for each, because a substitution that works in three places out of four is the shape that ships.
- Switching environment changes the base URL **and** the route in one motion; a test asserts a request under `prod` dials the connection dialer and the same request under `dev` dials locally.
- There is no connection control on the request. A test asserts the request model carries no route field — the model, not just the UI, must make it inexpressible.

- [ ] **Step 1: Write the failing test that an unresolved variable blocks the send**

```go
func TestSubstitute_UnresolvedVariableIsNamedAndBlocks(t *testing.T) {
    _, err := Substitute("{{baseUrl}}/users", Environment{Values: map[string]string{}})
    var unresolved *ErrUnresolved
    if !errors.As(err, &unresolved) {
        t.Fatalf("err = %v, want ErrUnresolved", err)
    }
    if unresolved.Name != "baseUrl" {
        t.Errorf("Name = %q, want baseUrl — the user must be told WHICH variable", unresolved.Name)
    }
}
```

- [ ] **Step 2: Run, fail, implement, pass, commit**

---

### Task 5: Secrets get their own scope, and auth uses them

**Files:**

- Modify: `internal/vault/meta.go:18` — `SecretMeta` gains `Scope Scope`, where `type Scope string` with `ScopeConnection Scope = "connection"` (the default every existing secret takes) and `ScopeAPI Scope = "api"`. The closed kind vocabulary of `meta.go:27` gains `KindBearerToken`, `KindAPIKey`. The vocabulary is closed on purpose — its comment says the format carries the set from day one so a new kind does not degrade into "unknown" — so extending it is the sanctioned move and inventing a kind string at a call site is not.
- Create: `internal/apisend/auth.go`, `internal/apisend/auth_test.go`
- Create: `internal/apicoll/resolve.go`, `internal/apicoll/resolve_test.go`
- Modify: `frontend/src/api/request-form.tsx` (auth as chips, per ADR-0021 — never a text field)

**Interfaces:**

Produces: `apicoll.ResolveSecret(ref string, scope vault.Scope) (credential.Secret, error)`, which **refuses any reference outside the API scope**.

**Acceptance Criteria — this task is the gate on the format decision (§8):**

- A collection file referencing a secret belonging to an SSH profile is **refused at resolve time**, with a named error. The test constructs a real profile secret, references it from a collection file, and asserts the send never happens.
- The refusal is in the **resolver**, not in the importer and not in the UI: a hand-edited file, a file arriving in a pull request, and a file written by our own importer all hit the same check.
- Bearer, basic and api-key each produce the correct header, each from a vault reference, and the value never appears in any JSON-RPC params or result.
- A test asserts the renderer cannot name a secret id on any `api.*` method — the same refusal `credentials.create` already makes (`ws.go:1371`).
- **If this cannot be made to hold, stop and raise it.** §8 of the spec says the file-based format is re-opened rather than shipped with a warning, and that decision is the owner's.

- [ ] **Step 1: Write the failing cross-scope refusal test**

```go
func TestResolve_RefusesASecretOutsideTheAPIScope(t *testing.T) {
    v := newTestVault(t)
    sshRef, _ := v.CreateNamed(ctx, credential.Secret("prod-root-password"),
        vault.SecretMeta{Name: "prod", Kind: vault.KindPassword, Scope: vault.ScopeConnection})

    _, err := apicoll.ResolveSecret(string(sshRef), vault.ScopeAPI)
    if !errors.Is(err, apicoll.ErrSecretOutOfScope) {
        t.Fatalf("err = %v, want ErrSecretOutOfScope — a collection file from a pull "+
            "request must not reach a connection's password (spec §8, nocx-jb20.1)", err)
    }
}
```

- [ ] **Step 2: Run, fail, implement the scope, pass**

- [ ] **Step 3: Check the existing vault tests still pass unedited**

Run: `go test ./internal/vault/ ./internal/credential/ ./internal/connection/ -race`
A `SecretMeta` change touches the profile path. If an existing test needs editing to pass, that is a signal the scope defaulted wrongly — fix the default, not the test.

- [ ] **Step 4: Implement the three auth schemes, run, commit**

---

### Task 6: Import a Postman v2.1 collection

**Files:**

- Create: `internal/apiimport/postman.go`, `internal/apiimport/postman_test.go`
- Create: `internal/apiimport/testdata/` (a real export, secrets replaced)
- Create: `contracts/api.import.schema.json`

**Interfaces:**

Produces two entry points, and the split matters: the pure one is testable without a disk, the other owns the atomicity of §12.2.

```go
// Pure: bytes in, model out. No disk, no vault.
func FromPostman(r io.Reader) (apicoll.Collection, []apicoll.Request, []apicoll.Environment, []Unsupported, error)

// Effectful: assembles in a temp dir, writes secrets to the vault under
// ScopeAPI FIRST, then arrives by one rename (§12.2).
func ImportInto(root string, v SecretWriter, r io.Reader) ([]Unsupported, error)

type Unsupported struct { What, Why string } // itemised to the user, never logged away
type SecretWriter interface {
    CreateNamed(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error)
}
```

**Acceptance Criteria:**

- A real Postman v2.1 export imports: folders become directories, requests become files, `{{baseUrl}}` survives as `{{baseUrl}}`.
- An environment variable of `"type": "secret"` lands **in the vault under the API scope**, and a scan of every written file finds the value in none of them.
- Anything not carried over is **itemised to the user**, not logged. A test asserts the list is non-empty for an export using a feature we do not model, and that it reaches the RPC result.
- **Partial failure:** an import that fails on the last file leaves **no** partial collection on disk. The test injects a write failure at file N and asserts the target directory does not exist.
- **Ordering:** the secret is written before the file that references it, and an interrupted import leaves an orphan vault record that `Reconcile` collects — never a file referencing nothing. The test asserts the invariant with **both ends**: the record exists from before the first referencing write until the last referencing file is removed.
- Parsing happens backend-side. A test asserts the renderer sends a **path**, never file contents (`nocx-52b`).
- **An import never fires a request.**

- [ ] **Step 1: Write the failing test that no file contains the secret**

```go
func TestFromPostman_SecretGoesToTheVaultAndNoFileCarriesIt(t *testing.T) {
    root := t.TempDir()
    v := newTestVault(t)
    if err := ImportInto(root, v, strings.NewReader(postmanExportWithSecret)); err != nil {
        t.Fatalf("ImportInto: %v", err)
    }
    filepath.WalkDir(root, func(p string, d fs.DirEntry, _ error) error {
        if d.IsDir() { return nil }
        b, _ := os.ReadFile(p)
        if bytes.Contains(b, []byte("s3cr3t-token-value")) {
            t.Errorf("%s carries the secret in the clear — the whole point of §6.3 is that "+
                "this folder is safe to commit BY CONSTRUCTION", p)
        }
        return nil
    })
}
```

- [ ] **Step 2: Run, fail, implement (temp dir + one rename), pass, commit**

---

### Task 7: Import a `curl` command line

**Files:**

- Create: `internal/apiimport/curl.go`, `internal/apiimport/curl_test.go`

**Interfaces:** Produces `apiimport.FromCurl(line string) (apicoll.Request, []Unsupported, error)`.

**Acceptance Criteria:**

- `-X`, `-H`, `-d`/`--data-raw`/`--data-binary`/`--data-urlencode`, `-F`, `--json`, `-u`, `-b`, `-G`, `-L`, `-k`, `--compressed` each produce the right field; one test per flag.
- Quoting and line continuations are handled by **our own parser**. A test asserts a line containing `$(rm -rf /)` and one containing backticks are parsed as literal text and that **no shell is invoked** — assert on the absence of any exec, not merely on the absence of damage.
- `--proxy`, `--cert` and `-o` are **refused out loud and itemised**. A test asserts the refusal reaches the RPC result, because a flag that changes the meaning of the request may not be silently dropped.
- A line carrying `-H 'Authorization: Bearer …'` is detected as a secret candidate and offered to the vault; the imported file carries a reference, not the token.

- [ ] **Step 1: Write the failing no-shell test**

```go
func TestFromCurl_NeverInvokesAShell(t *testing.T) {
    r, _, err := FromCurl(`curl -X POST 'https://x/y' -H 'X-A: $(touch /tmp/pwned)'`)
    if err != nil { t.Fatalf("FromCurl: %v", err) }
    if got := headerValue(r, "X-A"); got != `$(touch /tmp/pwned)` {
        t.Errorf("X-A = %q — the substitution must survive as LITERAL TEXT", got)
    }
}
```

- [ ] **Step 2: Run, fail, implement, pass, commit**

---

### Task 8: Raw, and the three states of a secret in it

**Files:**

- Create: `internal/apisend/spans.go`, `internal/apisend/spans_test.go`
- Create: `contracts/api.request.raw.schema.json`
- Create: `frontend/src/api/raw-view.tsx`, `frontend/src/api/raw-view.test.tsx`

**Interfaces:**

Produces — the value never crosses; only spans do (ADR-0021, §11.2):

```go
type Span struct {
    From, To int    `json:"from"`
    Kind     string `json:"kind"`     // "text" | "secret" | "secret-damaged"
    Name     string `json:"name"`     // the secret's NAME, never its value
    Damage   string `json:"damage"`   // e.g. "truncated, 24 of 214 bytes"; empty unless damaged
}
type Raw struct { Text string `json:"text"`; Spans []Span `json:"spans"` }

// Placement is what the SENDER knows because it did the substituting: where
// a secret was put, and what it should still be. This is why §11.2 is a
// VERIFICATION and not a scan — there is nothing to search for.
type Placement struct {
    From, To int
    Name     string
    Want     string // the expected bytes; never crosses the wire
}

func MarkSpans(text string, placed []Placement) Raw
```

**Acceptance Criteria:**

- Raw shows the request line, headers, body, the connection diagnostics (resolved address, route, TLS version, per-phase timings) and the response — for both sides.
- **Exact byte match → a badge naming the secret.**
- **Our span, bytes differ → a damage badge naming the shape of the damage, and the bytes are NOT rendered.** The test truncates a token to its first 24 bytes and asserts those 24 bytes appear **nowhere** in the payload — a truncated token is a prefix of a live one.
- **Not our span → plain text.**
- A test asserts no `Span` ever carries a secret value, over the wire, for all three states.
- The response is marked by the same mechanism: a server echoing the token back is shown as a badge, which is the finding people otherwise miss.
- No reveal state persists: a test asserts a revealed value is not written to any store and does not survive a remount.

- [ ] **Step 1: Write the failing truncation test**

```go
func TestSpans_ADamagedSecretNeverLeaksItsSurvivingBytes(t *testing.T) {
    secret := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature"
    sent := "Bearer " + secret[:24] // truncated in transit

    raw := MarkSpans(sent, []Placement{{From: 7, To: len(sent), Name: "API_TOKEN", Want: secret}})

    if raw.Spans[1].Kind != "secret-damaged" {
        t.Fatalf("Kind = %q, want secret-damaged", raw.Spans[1].Kind)
    }
    if strings.Contains(raw.Text, secret[:24]) {
        t.Error("the surviving 24 bytes are in the payload — they are a PREFIX OF A LIVE TOKEN")
    }
    if !strings.Contains(raw.Spans[1].Damage, "24 of 54") {
        t.Errorf("Damage = %q, want the shape of the damage", raw.Spans[1].Damage)
    }
}
```

- [ ] **Step 2: Run, fail, implement, pass, commit**

---

### Task 9: Cookies and session between requests

**Files:**

- Create: `internal/apisend/jar.go`, `internal/apisend/jar_test.go`
- Modify: `frontend/src/api/api-pane.tsx` (a visible, clearable session indicator)

**Acceptance Criteria:**

- A login request setting a cookie is followed by a request that carries it, with no configuration.
- The jar is scoped per environment. A test asserts a `dev` cookie is never sent under `prod` — cross-environment leakage is the failure mode this scoping exists to prevent.
- The jar is **visible and clearable in the product**, not only in memory: a soft state the UI does not admit to is how a stale session becomes an hour of debugging.
- A cookie marked `Secure` is not sent over plain http; a test for each direction.
- Whether the jar survives a restart is decided **in this task and written into the bead**, either way — spec §15 open question 2 leaves it open, and leaving it open in the code is not an option.

- [ ] **Step 1: Write the failing cross-environment test, run, fail, implement, pass, commit**

---

### Task 10: The body cap, and the failure paths in one pass

**Files:**

- Modify: `internal/apisend/sender.go`
- Create: `internal/apisend/failure_test.go`

**Acceptance Criteria:**

- The body is capped. **A capped body is a state the run displays**, and three sentences stay distinct: truncated, empty, and gone.
- The cap is measured, not asserted: the bead records what was measured and at what size the control plane degrades. (§15 open question 3.)
- A response larger than the cap does not put an unbounded value on the control plane — the test asserts the frame size, not merely that nothing crashed.
- Failure tests, each paired with its success case: DNS, TCP refused, TLS failure, mid-body close, pool lease refused, vault sealed, collection folder unreadable, collection folder read-only on write, malformed import.
- Cancelling a request in flight leaves no goroutine and no half-written run. Assert with `-race` and a goroutine count, not by eye.

- [ ] **Step 1: Write the failing cap test, run, fail, implement, pass, commit**

---

### Task 11: The end-to-end check that watches a person do it

**Files:**

- Create: `e2e/api-testing.spec.ts`
- Create: `e2e/fixtures/postman-collection.json`

**Acceptance Criteria — this is the epic's DONE WHEN, and it is written as one scenario:**

- A local test server is started by the spec.
- A Postman v2.1 export carrying `{{baseUrl}}/users` and a bearer token is imported through the UI.
- The token is in the vault, and a walk of every file under the collection root finds it in none.
- The request opens, Send is pressed, and a run appears with `201` and the decoded body.
- Raw is opened, and the token appears **as a badge naming the secret**, never as its bytes.
- The spec waits on observable state — a run row, a DOM state — and never on a duration.
- It runs in the container (`e2e/run-in-container.sh`) and is confirmed in CI. A layout-sensitive failure that is red only in the container is investigated before it is "fixed": the container is Linux WebKit at a container-default viewport, and CI is the source of truth.

- [ ] **Step 1: Write the spec, watch it fail at each stage, make each stage pass, commit**

---

## Where this plan deliberately says no

- **Splits** — not built and not designed around (§9.4). The workbench is a pane, so it inherits them.
- **The HTTPie-style line** — the second projection. The model is built for it and `parse(render(r)) == r` is written in Task 1's model tests, but no line parser ships here.
- **Everything in spec §3.**
