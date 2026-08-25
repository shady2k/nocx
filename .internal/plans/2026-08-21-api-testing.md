# API testing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** Open a collection exported from Postman, edit a request in a form, press Send, and get the response — with the token in the vault rather than in the file, and the request going out either from this machine or from inside an SSH connection already open. No account anywhere.

**Architecture:** A collection is a folder of JSON files the user places; `internal/apicoll` owns the model and the folder, and addresses files through a backend-held handle so the renderer never names a path twice. `internal/apisend` owns the HTTP client implementation whose dialer is supplied — `net.Dialer` locally, an adapter over an SSH pool lease otherwise. **A collection file names a variable, never a secret**, so a folder from a pull request has no way to spell "the production SSH password". The surface is one singleton **pane**.

**Tech Stack:** Go 1.x (`github.com/shady2k/nocx`), `golang.org/x/crypto/ssh`, JSON-RPC 2.0 over the existing WebSocket control plane, Solid + TypeScript frontend, JSON Schema in `contracts/` with generated renderer types, vitest + `go test -race` + Playwright.

**Spec:** `.internal/specs/2026-08-21-api-testing-design.md` — every §N below points there.

**This plan is the second draft.** The first was reviewed against the code and several of its claims were wrong. What changed, so nobody re-proposes the discarded versions:

- The secret **scope** on `SecretRecord`, its document version bump and its migration are **gone**. A file cannot name a secret at all, so there is nothing to scope (§8). This removed an entire task from the vault.
- The **body cap moved from the last task to the first send**, and it is `files.read`'s streamed 2 MiB rather than a number of ours (§12.3). Shipping an unbounded send and capping it eight tasks later would have released exactly the failure the design promises to prevent.
- **`tunnelConn.Dial` is `Dial(addr string)`** — not `DialContext`, and it takes no context. An adapter is needed and cancellation does not reach a blocked remote dial (§7.1).
- **`Reconcile` keeps an orphan whose catalogue record landed**, the opposite of the first draft's claim (§12.2).
- Several tasks were **not vertical** and could not have been committed past the deadcode ratchet. Boundaries were redrawn around user-reachable operations.

## Global Constraints

- **AGENTS.md is binding.** Read it before the first edit.
- **A task that adds a Go package lands with the wiring that makes it reachable** (`nocx-z7s6`). Every task is a vertical slice for this reason: package + composition root + transport method + the surface that calls it. `deadcode` cannot see a dead method behind a live interface, so each task also records `-whylive` for **its own** entry point — not only the epic's.
- **Every JSON-RPC result gets a JSON Schema in `contracts/`** with `additionalProperties: false` and an explicit `required`, plus the DTO test and `…_OverTheWireConformsToContract`.
- **Persisted files are a different compatibility boundary from RPC results.** The collection manifest, request and environment formats get their own schemas **and** a `storage.Module` version, refusing a version newer than ours before decoding anything.
- **Secrets never cross to the renderer**, and **identifiers for them never enter a collection file** (§8).
- **A test may not depend on timing.** Wait on an observable state change. `runtime.NumGoroutine` is not an observation — it is polluted by unrelated runtime goroutines and is timing-dependent.
- **The kit owns appearance.** Read `frontend/src/ui/README.md` first.
- **Commit subject ends with the bead id**; the body is prose about what was wrong and why this way.
- **A worker runs the unit tests for the files it changed and stops there.** The full gate belongs to whoever integrates.

---

## File Structure

**New Go packages**

| Path                   | Responsibility                                                                                                                        |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/apicoll/`    | The collection model, the on-disk JSON format and its version protocol, the handle, path safety. Knows nothing about HTTP.            |
| `internal/apibind/`    | The binding document: (collection, environment, variable) → vault id. The only thing that holds a secret identifier for this feature. |
| `internal/apisend/`    | The HTTP client implementation, the `Dialer` seam, bounded streamed response capture, raw spans. Knows nothing about files.           |
| `internal/apiimport/`  | Postman v2.1 and `curl` → the model. Hostile input; one converter, two entrances.                                                     |
| `internal/httppolicy/` | The policy engine extracted from `internal/assistant/httpguard.go`, with resolve-and-dial as a route-specific capability.             |

**Modified Go:** `internal/assistant/httpguard.go` (becomes a caller), `internal/capability/` (new operations, template `snippet.go`), `internal/transport/ws_api_handlers.go` (new; template `ws_snippet_handlers.go`), `internal/transport/ws_config_handlers.go` (`regResponder` entries), `internal/app/app.go` (composition root).

**New frontend:** `frontend/src/api/api-content.ts` (**extends `SolidPaneContent`** — the object the registry actually takes), `api-client.ts`, `api-store.ts`, `api-pane.tsx`, `request-form.tsx`, `run-list.tsx`, `raw-view.tsx`.

**Modified frontend:** `frontend/src/surface-registry.ts` (`SURFACE_ID_API`), `frontend/src/main.tsx:348`.

---

## Task Ordering

```
T1 ──▶ T2 ──▶ T3 ──▶ T4 ──┬──▶ T5 ──┬──▶ T7 ──▶ T8 ──▶ T9 ──▶ T10
                          └──▶ T6 ──┘
```

T5 and T6 are independent of each other. Everything else is a chain.

---

### Task 1: A collection folder opens as a handle, and its requests are listed

**Files:**

- Create: `internal/apicoll/{collection,folder,handle,path,version}.go` and their tests
- Create: `contracts/api.collections.open.schema.json`, `contracts/api.collections.list.schema.json`
- Create: `contracts/files/collection-manifest.schema.json`, `contracts/files/request.schema.json` — **persisted** formats, not RPC results
- Create: `internal/capability/api.go`, `internal/transport/ws_api_handlers.go`
- Create: `frontend/src/api/{api-content.ts,api-client.ts,api-store.ts,api-pane.tsx}`
- Modify: `internal/transport/ws_config_handlers.go`, `internal/app/app.go`, `frontend/src/surface-registry.ts`, `frontend/src/main.tsx:348`

**Interfaces produced:**

```go
package apicoll

type Request struct {
    ID      string   `json:"id"`
    Name    string   `json:"name"`
    Method  string   `json:"method"`
    URL     string   `json:"url"`
    Headers []Header `json:"headers"`
    Query   []Param  `json:"query"`
    Body    Body     `json:"body"`
    Auth    Auth     `json:"auth"`
}
type Header struct { Name, Value string; Enabled bool }
type Param  struct { Name, Value string; Enabled bool }
type Body   struct { Kind, Text, FileRef string } // "none"|"raw"|"form"|"file"
type Auth   struct { Kind, Var, User string }     // Kind: "none"|"bearer"|"basic"|"apikey"
                                                  // Var: a VARIABLE NAME, never a secret id (§8)

// HandleID is minted by the backend on open. The renderer names this and a
// relative path; it never names a root again (§13.1).
type HandleID string

type Service interface {
    Open(root string) (HandleID, Collection, error)
    List(h HandleID) (Collection, error)
    ReadRequest(h HandleID, relPath string) (Request, error)
    WriteRequest(h HandleID, relPath string, r Request) error
}

// Module is the persisted format's own version, separate from any RPC contract.
var Module = storage.Module{Name: "apicoll", Current: 1}
```

**Acceptance Criteria:**

- A folder with a manifest and two request files opens; both appear with method and name.
- A folder with no manifest is refused by name — not a panic, not an empty collection.
- One malformed request file is refused **by name** and the others still list. One bad file does not hide a collection.
- A manifest whose version is **newer than ours is refused before any decoding**, with a sentence naming the version. This is the persisted-format protocol of `internal/storage/document.go`, which RPC contracts do not provide.
- `Read(Write(r)) == r` for every request, including empty headers, a nil body and a `{{var}}` in each field. This is the half of §6.4's invariant testable before the line projection, and writing it now is what stops the model being shaped by the form.
- **Path safety, one test each:** `..` in a relative path is refused; an absolute path is refused; a request file that is a symlink out of the root is refused **without following it**; a write through a symlink is refused (`internal/storage/document.go:159` already refuses exactly this); a root replaced between open and read is reported, not papered over. Refused, never clamped — a silently rewritten path reports success for something it did not do.
- The renderer can name `root` **only** on `open`. A test asserts every other `api.*` method rejects a params object carrying a path.
- The opened-folder list survives a restart; contents are re-read, never cached.
- **The default location for a new collection is decided in this task and written into the bead** (§15 q1).
- `api.collections.list` validates against its contract **off the real socket**.
- `deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/apicoll.service.Open' ./...` prints a path from `main`; the contrast against a deliberately unwired symbol in the same package goes in the bead.

- [ ] **Step 1: Write the failing path-escape test first** — it is the one that would otherwise be written last and cut

```go
// internal/apicoll/path_test.go
func TestReadRequest_RefusesEscapingTheRoot(t *testing.T) {
    root := t.TempDir()
    outside := filepath.Join(t.TempDir(), "id_ed25519")
    os.WriteFile(outside, []byte("PRIVATE KEY"), 0o600)
    os.MkdirAll(filepath.Join(root, "users"), 0o755)
    os.Symlink(outside, filepath.Join(root, "users", "steal.json"))
    write(t, root, "nocx-collection.json", `{"schemaVersion":1,"name":"acme"}`)

    svc := newService()
    h, _, err := svc.Open(root)
    if err != nil { t.Fatalf("Open: %v", err) }

    for _, rel := range []string{"../../id_ed25519", outside, "users/steal.json"} {
        if _, err := svc.ReadRequest(h, rel); !errors.Is(err, ErrPathOutsideCollection) {
            t.Errorf("ReadRequest(%q) err = %v, want ErrPathOutsideCollection — a collection "+
                "from a pull request must not read files outside itself", rel, err)
        }
    }
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/apicoll/ -run TestReadRequest_Refuses -v` → FAIL, `undefined: newService`.

- [ ] **Step 3: Write the model, the version protocol, the handle and the folder reader**

The handle table lives in the service. `Open` canonicalises the root; every later call re-validates rather than trusting open time. Decode failures are collected and returned, never aborting the listing.

- [ ] **Step 4: Run and confirm green**, `go test ./internal/apicoll/ -race -v`

- [ ] **Step 5: Capability and transport**

`internal/capability/api.go` follows `internal/capability/snippet.go`'s shape. **But not its gate:** snippets hold the config gate because the snippet library is a document in the profile directory that backup/restore also writes (`ws_snippet_handlers.go:9`). **A collection is an arbitrary folder the user chose** (§6.1) — backup/restore does not touch it, so the snippet analogy does not transfer. Collections get their **own conflict domain**. Do not serialise them behind config.

- [ ] **Step 6: Both contract schemas, and separately the persisted-file schemas**

RPC results under `contracts/`; the manifest and request-file formats under `contracts/files/`. They are different boundaries: generated renderer DTOs give no migrations, no newer-version refusal and no strict validation of a file on disk.

- [ ] **Step 7: Wire the composition root** — `internal/app/app.go` near line 552 and `transport.WithAPI(...)` near line 823.

- [ ] **Step 8: The pane — as a `PaneContent`, not a bare component**

`frontend/src/api/api-content.ts` **extends `SolidPaneContent`** (`frontend/src/solid-pane-content.ts:18`), the way `SettingsContent` does (`settings-content.ts:29`). The registry factory returns a `PaneContent`, whose lifecycle is `mount`, `viewportChanged`, `focus`, `dispose`, `setVisible`, `setTarget` — a Solid component registered directly does not type-check.

Branded `SURFACE_API` and `SINGLETON_API` beside the Settings constants. Test the lifecycle: abort during mount, visibility before measurement, focus, and singleton deduplication.

The activity-bar entry **opens or focuses the pane and does not expand the side panel** — the bottom-zone pattern `sidebar.tsx` describes, which the Settings gear uses (§9.2).

- [ ] **Step 9: Run the gates for what changed, and commit**

```bash
go test ./internal/apicoll/ ./internal/transport/ -race
cd frontend && npm run test -- api && npm run contracts:check && npm run typecheck
```

---

### Task 2: Send from this machine — bounded and streamed from the first commit

**Files:**

- Create: `internal/httppolicy/{policy,dial}.go` + tests
- Create: `internal/apisend/{sender,client,dialer,capture}.go` + tests
- Create: `contracts/api.request.send.schema.json`
- Create: `frontend/src/api/{request-form.tsx,run-list.tsx}`
- Modify: `internal/assistant/httpguard.go`, `internal/transport/ws_api_handlers.go`, `internal/app/app.go`, `frontend/src/api/api-pane.tsx`

**Interfaces produced:**

```go
package apisend

type Dialer interface {
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Key is what a client instance is keyed by. One shared mutable client
// cannot hold a per-environment jar and a per-call dialer at once without
// leaking one environment's cookies or route into another's request, so
// instances are immutable and cached by this.
type Key struct { RouteID, CookieScope string }

type Sender interface {
    Send(ctx context.Context, r apicoll.Request, k Key) (Response, error)
}

type Response struct {
    Status     int
    Headers    []apicoll.Header
    Text       string // decoded, always valid UTF-8; EMPTY when Binary
    Binary     bool   // never base64 — the run says "binary body, N bytes"
    Lossy      bool   // invalid sequences replaced
    Truncated  bool   // the 2 MiB ceiling was hit
    Size       int64
    Timings    Timings
    TLSVersion string
    RemoteAddr string
}
type Timings struct { DNS, Connect, TLS, TTFB, Total time.Duration }
```

**Acceptance Criteria:**

- A request against a local test server returns status, headers, decoded body and a non-zero `Total`.
- A second Send **adds a run** rather than replacing the first.
- **The body is bounded in this task, not a later one.** The reader stops at the ceiling **plus one byte and never buffers the whole body** — the property `files.read` states and the reason a 40 GB response is safe. A cap applied after reading is not a cap. Test with a server that streams far past the ceiling and assert peak allocation, not just the returned length.
- `Truncated`, `Binary` and `Lossy` are distinct states with distinct sentences in the UI. A binary body sends **empty text**, never base64.
- **The 2 MiB default is inherited from `files.read`, not chosen**, and a parameter may only lower it.
- `internal/assistant`'s existing guard tests pass **unedited**. If one needs editing, stop and say so — that is the signal the extraction changed the assistant's policy.
- **The extraction separates policy from transport.** `httpguard.go:140` resolves locally and `:208`/`:226` dial with a concrete `net.Dialer`; that is correct for the assistant and cannot be reused verbatim for a route that must resolve remotely. Extract a policy engine **plus a route-specific resolve-and-dial capability**; the assistant's constructor stays locked to its existing concrete behaviour.
- `http://` to a public address refused, to loopback allowed, checked on the connection and on every redirect hop.
- A failure test for each of DNS, connection refused, TLS handshake, mid-body close — each paired with "and on an ordinary machine it succeeds".
- The send holds no global gate across network I/O. Secrets and request data are snapshotted under a short-lived gate which is **released before dialling**; holding config or vault behind arbitrary remote latency would block unrelated settings, imports and backup.

- [ ] **Step 1: Write the failing streaming-bound test**

```go
// internal/apisend/capture_test.go
func TestCapture_StopsAtTheCeilingWithoutBufferingTheBody(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        chunk := bytes.Repeat([]byte("x"), 1<<20)
        for i := 0; i < 64; i++ { _, _ = w.Write(chunk) } // 64 MiB
    }))
    defer srv.Close()

    var before, after runtime.MemStats
    runtime.GC(); runtime.ReadMemStats(&before)
    got, err := newSender().Send(context.Background(),
        apicoll.Request{Method: "GET", URL: srv.URL}, Key{})
    runtime.ReadMemStats(&after)

    if err != nil { t.Fatalf("Send: %v", err) }
    if !got.Truncated { t.Error("Truncated = false, want true") }
    if int64(len(got.Text)) > ceiling { t.Errorf("len = %d, want <= %d", len(got.Text), ceiling) }
    if d := after.TotalAlloc - before.TotalAlloc; d > 8<<20 {
        t.Errorf("allocated %d bytes — the body was buffered; a cap applied AFTER "+
            "reading is not a cap (§12.3)", d)
    }
}
```

- [ ] **Step 2: Run, fail, then extract `internal/httppolicy` FIRST**

Carry the four reasons in `httpguard.go`'s header comment across verbatim — a reader who does not know them will "simplify" the guard into a form validator.

- [ ] **Step 3: Write the sender over the extracted policy**, `httptrace` for timings, the ceiling in the reader

- [ ] **Step 4: Run all three packages**, `go test ./internal/apisend/ ./internal/httppolicy/ ./internal/assistant/ -race`

- [ ] **Step 5: `api.request.send`, its schema, both conformance tests, the form and the run list**

- [ ] **Step 6: Commit**

---

### Task 3: Environments, variables, and the route — landing together

The SSH dialer and the thing that selects it are one deliverable. Split apart, the dialer has no caller and the task cannot commit.

**Files:**

- Create: `internal/apicoll/{environment,substitute}.go`, `internal/apisend/ssh_dialer.go` + tests
- Create: `contracts/api.environments.list.schema.json`, `contracts/files/environment.schema.json`
- Modify: `internal/apisend/sender.go`, `internal/transport/ws_api_handlers.go`, `internal/app/app.go`, `frontend/src/api/{api-pane.tsx,api-client.ts}`

**Interfaces produced:**

```go
type Environment struct {
    Name        string            `json:"name"`
    Values      map[string]string `json:"values"`
    SecretVars  []string          `json:"secretVars"` // NAMES ONLY — no values, no ids (§8)
    Route       Route             `json:"route"`
}
// Route answers "how to get there" in the SAME record as baseUrl answers
// "where" (§6.5). Two records would drift; one cannot.
type Route struct {
    Kind      string `json:"kind"`      // "direct" | "connection"
    ProfileID string `json:"profileId"` // empty for "direct"
}

func apisend.NewSSHDialer(lease ssh.TunnelConn, dialTimeout time.Duration) Dialer
```

**Acceptance Criteria:**

- `{{baseUrl}}/users` with `baseUrl=http://localhost:3000` resolves; substitution works in URL, headers, query and body, with a test for each — one that works in three places out of four is the shape that ships.
- An unresolved `{{var}}` **blocks Send and names the variable**. Not the literal braces, not an empty string.
- Switching environment moves the address **and** the route in one motion: the same request under `prod` dials the SSH dialer and under `dev` dials locally.
- The request model carries **no route field**. A test asserts it — the model, not only the UI, must make a per-request route inexpressible.
- The SSH dialer takes a lease and **opens no second SSH connection when the pool key matches**: assert the pool's connection count across a send.
- **The adapter's limits are tested, not assumed.** `tunnelConn.Dial(addr string)` takes no context, so: a cancelled request cannot be interrupted mid-dial, but the dial has a **bounded deadline**, and a connection that arrives after cancellation is **closed and never produces a run**. Both directions get a test.
- A spent lease **refuses** rather than dialling locally. A silent fallback would send a production request around its bastion, which §6.5 exists to make impossible.
- Losing the connection mid-request is a named error, distinguishable from an HTTP error and from a user cancel.
- The bead records the honest reading of "any connection already open": `TunnelConn` goes through `acquirePooled` and shares only when the resolved pool key matches, so a send may authenticate anew. A route names a destination, not a window.

- [ ] **Step 1: Write the failing no-local-fallback test**

```go
func TestSSHDialer_ASpentLeaseRefusesRatherThanDiallingLocally(t *testing.T) {
    d := apisend.NewSSHDialer(&fakeTunnelConn{dialErr: ssh.ErrTunnelConnClosed}, time.Second)
    _, err := d.DialContext(context.Background(), "tcp", "api.internal:443")
    if !errors.Is(err, ssh.ErrTunnelConnClosed) {
        t.Fatalf("err = %v, want ErrTunnelConnClosed — falling back to the local dialer "+
            "would send a production request around its bastion", err)
    }
}
```

- [ ] **Step 2: Run, fail, implement substitution, the route, the dialer and the wiring, pass, commit**

---

### Task 4: Secret variables — the binding document and auth

**Files:**

- Create: `internal/apibind/{binding,store}.go` + tests
- Create: `internal/apisend/auth.go` + tests
- Create: `contracts/api.bindings.set.schema.json`
- Modify: `internal/apisend/sender.go`, `internal/transport/ws_api_handlers.go`, `internal/app/app.go`, `frontend/src/api/request-form.tsx`

**Interfaces produced:**

```go
package apibind
// The ONLY place a secret identifier for this feature is held. Never a file
// under the collection root, and never the renderer (§8).
type Key struct { Collection, Environment, Variable string }
type Store interface {
    Lookup(k Key) (credential.SecretID, bool, error)
    Bind(ctx context.Context, k Key, value credential.Secret) error
    Unbind(ctx context.Context, k Key) error
    UnbindCollection(ctx context.Context, collection string) error // §12.2's closing event
}
```

**Acceptance Criteria:**

- **A collection file cannot name a secret.** A test writes a request file whose auth `Var` is a raw vault id belonging to an SSH profile, sends it, and asserts the value is not sent: the id is looked up as a _variable name_, finds no binding, and the send is blocked as unresolved. The point is that the file's content is irrelevant, not that a check caught it.
- No `api.*` params or result ever carries a `credential.SecretID`. Assert over every method.
- Bearer, basic and api-key each produce the right header from a binding; the value appears in no JSON-RPC frame.
- **Ordering:** the vault value is written before the binding. A crash between them leaves an unreachable value (harmless) and never a binding pointing at nothing.
- **The closing event exists and is tested:** deleting a collection removes its bindings and the values only those bindings referenced. This is what §12.2's invariant needs and what the first draft did not have.
- The first draft's `Reconcile` claim is **not** re-used. `internal/vault/journal.go:119` clears the entry and keeps the secret when a catalogue record exists, and `CreateNamed` writes value and record together (`vault.go:1122`) — so a crashed create is treated as complete. Any cleanup here is ours, not reconciliation's.

- [ ] **Step 1: Write the failing test that a file naming a raw vault id gets nothing**

```go
func TestSend_AFileNamingARawVaultIDResolvesNothing(t *testing.T) {
    v := newTestVault(t)
    sshID, _ := v.CreateNamed(ctx, credential.Secret("prod-root-password"),
        vault.SecretMeta{Name: "prod", Kind: vault.KindPassword})

    r := apicoll.Request{Method: "GET", URL: "http://x/", Auth: apicoll.Auth{
        Kind: "bearer", Var: string(sshID), // a hostile file's best attempt
    }}
    _, err := send(t, r, envWithNoSuchVariable)
    var unresolved *apicoll.ErrUnresolved
    if !errors.As(err, &unresolved) {
        t.Fatalf("err = %v, want ErrUnresolved — a vault id in a file is just an unknown "+
            "VARIABLE NAME; there is no syntax for naming a secret (§8)", err)
    }
}
```

- [ ] **Step 2: Run, fail, implement, pass, commit**

---

### Task 5: Import a Postman v2.1 collection

**Files:**

- Create: `internal/apiimport/postman.go`, `internal/apiimport/fs.go` + tests, `internal/apiimport/testdata/`
- Create: `contracts/api.import.postman.schema.json`
- Modify: `internal/transport/ws_api_handlers.go`, `internal/app/app.go`, `frontend/src/api/{api-pane.tsx,api-client.ts}`

**Interfaces produced:**

```go
func FromPostman(r io.Reader) (apicoll.Collection, []apicoll.Request, []apicoll.Environment, []Unsupported, error)
func ImportInto(ctx context.Context, fs FS, b apibind.Store, dest string, r io.Reader) ([]Unsupported, error)

type Unsupported struct { What, Why string } // itemised to the user, never logged away
type FS interface {                          // injected so "fail at file N" is testable
    MkdirTemp(dir, pattern string) (string, error)
    WriteFile(name string, b []byte, perm os.FileMode) error
    Sync(name string) error
    Rename(old, new string) error
    RemoveAll(path string) error
}
```

**Acceptance Criteria:**

- A real Postman v2.1 export imports: folders become directories, requests become files, `{{baseUrl}}` survives.
- A `"type": "secret"` variable becomes a **declared secret variable** — its name in the environment file, its value in the vault, its identifier in the binding document. A walk of every written file finds the value in none of them **and finds no vault identifier either**.
- Anything not carried over is **itemised into the RPC result**, not logged. Test with an export using a feature we do not model.
- **Atomicity, stated rather than implied:** the temporary directory is created **inside the destination's parent** so the rename stays on one filesystem; an existing destination is **refused**, not replaced; files and the staging directory are synced before the rename and the parent after it. The injected `FS` makes each of these a test: fail at file N, fail at sync, fail at rename, fail after rename.
- After a failure at any of those points, the destination does not exist and no binding references a missing value.
- Parsing is backend-side: the renderer sends a **path**, never file contents (`nocx-52b`).
- **An import never fires a request.**
- It lands with its RPC method, handler, client call and the UI entrance that invokes it — `FromPostman` with no caller cannot commit.

- [ ] **Step 1: Write the failing test that no file carries the secret or its id**

```go
func TestImportInto_NoFileCarriesTheValueOrItsIdentifier(t *testing.T) {
    dest := filepath.Join(t.TempDir(), "acme")
    binds := newBindStore(t)
    if _, err := ImportInto(ctx, realFS{}, binds, dest, strings.NewReader(exportWithSecret)); err != nil {
        t.Fatalf("ImportInto: %v", err)
    }
    id, _, _ := binds.Lookup(apibind.Key{Collection: dest, Environment: "prod", Variable: "token"})
    filepath.WalkDir(dest, func(p string, d fs.DirEntry, _ error) error {
        if d.IsDir() { return nil }
        b, _ := os.ReadFile(p)
        if bytes.Contains(b, []byte("s3cr3t-token-value")) {
            t.Errorf("%s carries the value in the clear", p)
        }
        if id != "" && bytes.Contains(b, []byte(id)) {
            t.Errorf("%s carries the vault identifier — a file must not be able to name a "+
                "secret at all, which is the whole of §8", p)
        }
        return nil
    })
}
```

- [ ] **Step 2: Run, fail, implement, pass, commit**

---

### Task 6: Import a `curl` command line

**Files:** Create `internal/apiimport/curl.go` + tests; `contracts/api.import.curl.schema.json`; modify the handler, the client and the pane's paste entrance.

**Acceptance Criteria:**

- One test per supported flag: `-X`, `-H`, `-d`/`--data-raw`/`--data-binary`/`--data-urlencode`, `-F`, `--json`, `-u`, `-b`, `-G`, `-L`, `-k`, `--compressed`.
- Quoting and continuations are handled by **our parser**. A line containing `$(…)` and one containing backticks parse as literal text, and the test asserts **no shell was invoked** — assert on the absence of an exec, not on the absence of damage.
- `--proxy`, `--cert`, `-o` are **refused out loud and itemised into the RPC result**. A flag that changes the meaning of a request may not be silently dropped.
- A line carrying `-H 'Authorization: Bearer …'` is offered as a secret variable; the written file carries the variable name only.
- Lands with its RPC method, handler, client call and UI entrance.

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

### Task 7: Raw — placements on the request, a bounded search on the response

**Files:** Create `internal/apisend/spans.go` + tests, `contracts/api.request.raw.schema.json`, `frontend/src/api/raw-view.tsx` + test; modify `internal/apisend/sender.go` so the live send produces the raw.

**Interfaces produced:**

```go
type Span struct {
    From, To int    `json:"from"`
    Kind     string `json:"kind"`   // "text" | "secret" | "secret-damaged"
    Name     string `json:"name"`   // the NAME, never the value
    Damage   string `json:"damage"` // "truncated, 24 of 214 bytes"; empty unless damaged
}
type Raw struct { Text string `json:"text"`; Spans []Span `json:"spans"` }

// Placement is what the sender knows because it did the substituting. It is
// why the REQUEST side is verification and not a search.
type Placement struct { From, To int; Name string; Want string } // Want never crosses

func MarkRequest(text string, placed []Placement) Raw
// The response has no placements: a placement in the request says nothing
// about whether a server echoed the bytes back, or where. §11.3.
func SearchResponse(decoded string, used []NamedSecret) Raw
```

**Acceptance Criteria:**

- Raw shows request line, headers, body, the connection diagnostics (resolved address, route, TLS version, per-phase timings) and the response.
- Exact byte match → a badge naming the secret. Our span with different bytes → a **damage badge naming the shape**, and the surviving bytes appear **nowhere** in the payload — a truncated token is a prefix of a live one. Not our span → plain text.
- No `Span` ever carries a secret value, in any of the three states, over the wire.
- **The response search is bounded and its limits are stated, not discovered:** it runs on the **decoded** body only (after decompression and de-chunking, never before); it does **not** find transformed spellings — a base64-wrapped or URL-escaped token is missed, and a test pins that so the coverage is never overstated; overlapping matches collapse to the longest.
- A server echoing the token into an error message shows it as a badge. Without this the raw view — whose whole purpose is to show everything — ships a live credential to the renderer as ordinary text.
- No reveal state persists: a revealed value is written to no store and does not survive a remount.

- [ ] **Step 1: Write the failing truncation-leak test**

```go
func TestMarkRequest_ADamagedSecretNeverLeaksItsSurvivingBytes(t *testing.T) {
    secret := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature"
    sent := "Bearer " + secret[:24]
    raw := MarkRequest(sent, []Placement{{From: 7, To: len(sent), Name: "API_TOKEN", Want: secret}})

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

- [ ] **Step 2: Run, fail, implement both sides, pass, commit**

---

### Task 8: Cookies, scoped per environment, in the live sender

**Files:** Create `internal/apisend/jar.go` + tests; modify `internal/apisend/{sender,client}.go` and `frontend/src/api/api-pane.tsx`.

**Acceptance Criteria:**

- A login request that sets a cookie is followed by a request carrying it, with no configuration.
- **The jar is part of the client `Key`.** A test sends concurrently under `dev` and `prod` and asserts neither the cookie nor the dialer crossed. This is the concrete reason instances are immutable and cached rather than one shared client mutated per send.
- A `Secure` cookie is not sent over plain http; a test each way.
- The jar is **visible and clearable in the product**. A soft state the UI does not admit to is how a stale session becomes an hour of debugging.
- **Whether the jar survives a restart is decided in this task and written into the bead** (§15 q2).

- [ ] **Step 1: Write the failing concurrent cross-environment isolation test, run, fail, implement, pass, commit**

---

### Task 9: The failure paths, and an honest cancellation boundary

**Files:** Create `internal/apisend/failure_test.go`; modify `internal/apisend/sender.go`.

**Acceptance Criteria:**

- Failure tests, each paired with its success case: DNS, TCP refused, TLS failure, mid-body close, pool lease refused, vault sealed, collection folder unreadable, folder read-only on write, malformed import, handle invalidated by a root replaced underneath.
- **Cancellation is asserted on an observable, not on a goroutine count.** The sender exposes a lifecycle completion signal and the test waits on that. `runtime.NumGoroutine` is polluted by unrelated runtime goroutines and is timing-dependent, which the repository's own rule forbids.
- The cancellation **boundary is stated**: a blocked remote dial cannot be interrupted, because `tunnelConn.Dial` takes no context. What is guaranteed instead — a bounded dial deadline, and a late connection closed without producing a run — is tested in both directions. A context-aware tunnel seam would remove the limit and is a separate bead, filed by this task.
- Cancelling in flight leaves no half-written run.

- [ ] **Step 1: Write the failing late-connection test, run, fail, implement, pass, commit**

---

### Task 10: The end-to-end check that watches a person do it

**Files:** Create `e2e/api-testing.spec.ts`, `e2e/fixtures/postman-collection.json`.

**Acceptance Criteria — the epic's DONE WHEN, as one scenario:**

- The spec starts a local test server.
- A Postman v2.1 export with `{{baseUrl}}/users` and a bearer token is imported through the UI.
- The value is in the vault; a walk of every file under the collection root finds neither the value **nor any vault identifier**.
- The request opens, Send is pressed, a run appears with `201` and the decoded body.
- Raw is opened; the token appears **as a badge naming the secret**, never as its bytes.
- It waits on observable state — a run row, a DOM state — never on a duration.
- It runs in the container (`e2e/run-in-container.sh`) and is confirmed in CI. A failure red only in the container is investigated before it is "fixed": that image is Linux WebKit at a container-default viewport, and CI is the source of truth.

- [ ] **Step 1: Write the spec, watch each stage fail, make each pass, commit**

---

## Where this plan deliberately says no

- **Splits** — not built and not designed around (§9.4).
- **The HTTPie-style line** — the second projection. The model is built for it and the file round trip is tested in Task 1, but no line parser ships here.
- **A context-aware tunnel dial seam** — filed as its own bead by Task 9, not smuggled into this epic.
- **Everything in spec §3.**
