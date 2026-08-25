# The import ask takes Postman's shape — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A person pastes a Postman export's URL — including one reachable only through a connection — picks that connection, presses Import, and gets a collection whose environment already routes the same way; and the ordinary file case stops asking for two absolute paths.

**Architecture:** One JSON-RPC method (`api.import.postman`) grows a third mutually exclusive source, `url`, with an optional `route` beside it. A new `internal/apifetch` seam acquires the document over the route table `apisend` already owns, guarded by the `httppolicy` transport already in use, and hands bytes to the existing `apiimport.ImportInto`. The renderer's ask loses both path fields: one paste box, the existing `DropZone`, and one editable destination summary.

**Tech Stack:** Go (`net/http`, `httptest`), Solid.js + TypeScript, vitest + `@solidjs/testing-library`, Playwright for e2e, `cmd/devharness` for a headless backend.

**Spec:** [`.internal/specs/2026-08-23-import-ask-postman-shape-design.md`](../specs/2026-08-23-import-ask-postman-shape-design.md)

## Global Constraints

- **TDD, always: the failing test comes first.** Red → green → refactor. The pre-commit hook is the gate on every commit and runs formatters, linters, `go test -race` for staged Go, and vitest for staged frontend.
- **Every commit names its bead** in the subject — `<type>(<scope>): <subject> (<bead-id>)` — with a prose body saying what was wrong, what changed, and why this way rather than the obvious alternative.
- **Greenfield: no back-compat shims.** When a signature must change, change it and update every caller. Do not add a second function beside the first.
- **A new Go package lands with the wiring that makes it reachable**, in the same commit, or the deadcode ratchet in the pre-commit hook rejects it. This is why Task 2 is one vertical rather than three tasks.
- **Kit rules.** A surface may _place_ a kit component (`flex`, `margin`, `width`, `order`, `align-self`, `position`) and may never _repaint_ it (`background`, `border`, `color`, `font-*`, `padding`, `box-shadow`). No hand-rolled `<div>` controls, no inline SVG. `PencilIcon` and `CloseIcon` already exist in `frontend/src/ui/icons/`.
- **A test may not depend on timing.** Wait on an observable state change, never on a duration.
- **The worker runs the unit tests for what it changed and stops there.** `make ci-full`, the containerized jobs and the e2e suite belong to whoever integrates.
- **Field ids are not renamed.** `api-import-postman-file` and `api-import-postman-dest` keep their ids wherever those fields survive; moving a field is not renaming it.
- **Refusals are sentences, and each one has a test.** For every "returns an error when…" there is a paired "and on an ordinary machine it succeeds".

---

## File Structure

| File                                    | Responsibility                                                                                                  |
| --------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `internal/apiimport/write.go`           | `ImportInto` gains the route the document arrived through (Task 1)                                              |
| `internal/apiimport/postman.go`         | The minted environment carries that route instead of a hardcoded `direct`; `MaxDocumentBytes` exported (Task 1) |
| `internal/apifetch/fetch.go`            | **New.** Acquire an import document by URL, over a route, guarded and bounded (Task 2)                          |
| `internal/apifetch/fetch_test.go`       | **New.** `httptest`-driven refusals and the paired successes (Task 2)                                           |
| `internal/capability/api.go`            | `APIImportService` gains `ImportPostmanURL`; the service gets the fetcher (Tasks 2, 3)                          |
| `internal/transport/ws_api_handlers.go` | Params gain `url` + `route`; validator becomes exactly-one-of-three; handler dispatches (Tasks 2, 3)            |
| `internal/app/app.go`                   | The fetcher is constructed from the route table and handed to the import service (Task 2)                       |
| `frontend/src/api/api-paths.ts`         | `classifyPastedSource`, `proposedDestinationFromDocument`, `proposedDestinationFromURL` (Task 4)                |
| `frontend/src/api/api-client.ts`        | `ImportSource` gains the URL member (Task 5)                                                                    |
| `frontend/src/api/import-dialogs.tsx`   | The ask's new shape (Tasks 6, 7)                                                                                |
| `frontend/src/api/api-pane.tsx`         | State for the pasted source, the route, and the summary line's edit mode (Tasks 6, 7)                           |
| `e2e/api-import-url.spec.ts`            | **New.** The epic's happy path, watched (Task 8)                                                                |

## Task DAG

```
T1 ──► T2 ──► T3 ──┐
                   ├──► T8
T4 ──► T5 ──► T6 ──► T7 ──┘
```

---

### Task 1: The imported environment carries the route the document arrived through

**Files:**

- Modify: `internal/apiimport/write.go:65` (`ImportInto` signature), and the `parseImport` caller inside it
- Modify: `internal/apiimport/postman.go:19` (`maxDocumentBytes` → `MaxDocumentBytes`), `:244` and `:273` and `:343` (the three places an `Environment` is minted)
- Modify: `internal/capability/api.go:865,899` (both existing callers pass a direct route)
- Test: `internal/apiimport/write_test.go`, `internal/apiimport/postman_test.go`

**Interfaces:**

- Consumes: `apicoll.Route` (`internal/apicoll/collection.go:119`) — `{Kind, ProfileID, InsecureTLS}` with `apicoll.RouteDirect` / `apicoll.RouteConnection`.
- Produces:
  - `func ImportInto(ctx context.Context, fsys FS, b BindWriter, dest string, r io.Reader, route apicoll.Route) ([]Unsupported, error)`
  - `const MaxDocumentBytes = 16 << 20`

**Acceptance Criteria:**

- `ImportInto` takes the route as its last parameter; there is no second entry point and no default-carrying wrapper.
- An import given `apicoll.Route{Kind: "connection", ProfileID: "prod-bastion"}` writes an environment file whose `route.kind` is `connection` and whose `route.profileId` is `prod-bastion`.
- An import given the zero `apicoll.Route` writes `route.kind: "direct"` exactly as before — every existing test stays green without being edited for behaviour.
- `InsecureTLS` is never carried in from the parameter: an import given a route with `InsecureTLS: true` writes an environment with `insecureTls` absent/false.
- `MaxDocumentBytes` is exported and `maxDocumentBytes` no longer exists anywhere in the package.

- [ ] **Step 1: Write the failing test**

Add to `internal/apiimport/postman_test.go`:

```go
func TestImportInto_EnvironmentCarriesTheRouteTheDocumentArrivedThrough(t *testing.T) {
	fsys := newMemFS(t)
	doc := strings.NewReader(`{"info":{"name":"acme","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[]}`)

	_, err := apiimport.ImportInto(context.Background(), fsys, nopBinder{}, "acme", doc,
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion", InsecureTLS: true})
	if err != nil {
		t.Fatalf("ImportInto: %v", err)
	}

	var env apicoll.Environment
	readJSON(t, fsys, "acme/environments/default.json", &env)
	if env.Route.Kind != apicoll.RouteConnection {
		t.Errorf("route kind = %q, want %q", env.Route.Kind, apicoll.RouteConnection)
	}
	if env.Route.ProfileID != "prod-bastion" {
		t.Errorf("profile = %q, want prod-bastion", env.Route.ProfileID)
	}
	// InsecureTLS is per-ENVIRONMENT on purpose (collection.go:126): a
	// fetch is not an environment, so it may not turn verification off for
	// every request the collection will ever send.
	if env.Route.InsecureTLS {
		t.Error("insecureTls was carried in from the fetch route; it must never be")
	}
}
```

`newMemFS`, `nopBinder` and `readJSON` already exist in this package's tests — reuse them; if a helper's name differs, use the existing one rather than adding a second.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/apiimport/ -run EnvironmentCarriesTheRoute -race`
Expected: FAIL — `not enough arguments in call to apiimport.ImportInto`.

- [ ] **Step 3: Change the signature and thread the route**

In `internal/apiimport/write.go`:

```go
// ImportInto writes the collection at dest from the document in r.
//
// route is how the document was ACQUIRED, and the environment this mints
// inherits it: a collection fetched through a connection whose environment
// said `direct` is a collection where every request fails until the person
// sets by hand the thing they had already told the import (spec §6). Only
// Kind and ProfileID are inherited — InsecureTLS is the environment's own
// choice (apicoll/collection.go:126) and a one-off fetch may not make it.
func ImportInto(ctx context.Context, fsys FS, b BindWriter, dest string, r io.Reader, route apicoll.Route) ([]Unsupported, error) {
```

Pass it into the converter (`parseImport`'s caller already builds the `converter` struct — give that struct a `route apicoll.Route` field), and at each of the three mint sites in `postman.go` replace

```go
c.env = &apicoll.Environment{Name: name, Route: apicoll.Route{Kind: apicoll.RouteDirect}}
```

with

```go
c.env = &apicoll.Environment{Name: name, Route: c.arrivalRoute()}
```

and add, beside the other small helpers in `postman.go`:

```go
// arrivalRoute is the route the document arrived through, normalised: an
// empty kind is `direct` (the wire's own normalisation, wireRoute), and
// InsecureTLS is dropped — see ImportInto's comment.
func (c *converter) arrivalRoute() apicoll.Route {
	kind := c.route.Kind
	if kind == "" {
		kind = apicoll.RouteDirect
	}
	if kind != apicoll.RouteConnection {
		return apicoll.Route{Kind: apicoll.RouteDirect}
	}
	return apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: c.route.ProfileID}
}
```

- [ ] **Step 4: Export the ceiling**

In `postman.go`, rename `maxDocumentBytes` to `MaxDocumentBytes` and update its two uses (`:213`, `:217`). Keep the comment; add one line: `Exported because the URL entrance bounds its read by the same number, and two spellings of one ceiling is two ceilings.`

- [ ] **Step 5: Update the two existing callers**

`internal/capability/api.go`, both methods, pass the direct route explicitly:

```go
return apiimport.ImportInto(ctx, s.fsys, s.bindings, dest, f, apicoll.Route{Kind: apicoll.RouteDirect})
```

- [ ] **Step 6: Run the package's tests**

Run: `go test ./internal/apiimport/ ./internal/capability/ -race`
Expected: PASS, including the new test and every existing one unedited.

- [ ] **Step 7: Commit**

```bash
git add internal/apiimport internal/capability
git commit -m "feat(apiimport): the environment inherits the route the document arrived through (<bead-id>)"
```

---

### Task 2: `api.import.postman` takes a URL, and the backend fetches it

**Files:**

- Create: `internal/apifetch/fetch.go`, `internal/apifetch/fetch_test.go`
- Modify: `internal/capability/api.go:749` (interface), `:859` (constructor), new method beside `ImportPostmanDocument`
- Modify: `internal/transport/ws_api_handlers.go:783` (params), `:1879` (validator), `:569` (handler)
- Modify: `internal/app/app.go:622` (wiring, beside `apiSender`)
- Test: `internal/transport/ws_api_handlers_test.go` (validator cases + the over-the-wire conformance run)

**Interfaces:**

- Consumes: `apisend.Routes` (`internal/apisend/dialer.go:34`) = `func(ctx, routeID string) (httppolicy.Route, error)`; `apisend.RouteIDFor(apicoll.Route) (string, error)` (`routes.go:82`); `httppolicy.NewTransport(httppolicy.Params{Component, Route, Log, TLSClientConfig})`; `apiimport.MaxDocumentBytes` (Task 1).
- Produces:
  - `type Fetcher interface { Fetch(ctx context.Context, rawURL string, route apicoll.Route) ([]byte, error) }`
  - `func New(routes apisend.Routes, l log.Logger) *Client` implementing it
  - `var ErrScheme, ErrTooLarge, ErrNotADocument error`
  - `capability.APIImportService.ImportPostmanURL(ctx, rawURL string, route apicoll.Route, dest string) ([]apiimport.Unsupported, error)`

**Acceptance Criteria:**

- `api.import.postman` accepts exactly one of `path`, `document`, `url`; two or three, or none, is refused by name with one sentence naming all three.
- A `url` whose scheme is not `http` or `https` is refused before any dial.
- A body over `apiimport.MaxDocumentBytes` is refused and nothing is written.
- A response whose first non-space byte is neither `{` nor `[` is refused as "what is at that address is not a Postman export" — it never reaches `parseImport`, so no message mentions curl.
- `Content-Type` is not consulted anywhere in `apifetch`.
- The document is fully read before `ImportInto` is called: a fetch that fails mid-body leaves no folder at `dest`.
- On an ordinary machine, a `httptest` server serving a valid export imports it and answers `unsupported: []`.
- `TestAPIImportPostman_OverTheWireConformsToContract` also exercises the **URL** route.
- `deadcode -tags gtk3 -whylive '.../internal/apifetch.Client.Fetch' ./...` prints a path from `main`, contrasted in the commit body with an unwired symbol.

- [ ] **Step 1: Write the failing fetch tests**

`internal/apifetch/fetch_test.go`:

```go
package apifetch_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/httppolicy"
)

// directRoutes is the table under test: the one route this test needs,
// answered the way apisend answers the empty RouteID.
func directRoutes() apisend.Routes {
	return func(ctx context.Context, routeID string) (httppolicy.Route, error) {
		if routeID != "" {
			return nil, fmt.Errorf("unexpected route %q", routeID)
		}
		return httppolicy.Local(), nil
	}
}

const export = `{"info":{"name":"acme"},"item":[]}`

func TestFetch_ReturnsTheDocumentOnAnOrdinaryMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No Content-Type on purpose: the first byte is what decides.
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	got, err := apifetch.New(directRoutes(), nil).
		Fetch(context.Background(), srv.URL, apicoll.Route{Kind: apicoll.RouteDirect})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != export {
		t.Errorf("body = %q, want the export", got)
	}
}

func TestFetch_RefusesANonHTTPScheme(t *testing.T) {
	_, err := apifetch.New(directRoutes(), nil).
		Fetch(context.Background(), "file:///etc/passwd", apicoll.Route{Kind: apicoll.RouteDirect})
	if !errors.Is(err, apifetch.ErrScheme) {
		t.Fatalf("err = %v, want ErrScheme", err)
	}
}

func TestFetch_RefusesWhatIsNotADocumentWithoutMentioningCurl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><title>Sign in</title>"))
	}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).
		Fetch(context.Background(), srv.URL, apicoll.Route{Kind: apicoll.RouteDirect})
	if !errors.Is(err, apifetch.ErrNotADocument) {
		t.Fatalf("err = %v, want ErrNotADocument", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "curl") {
		t.Errorf("the refusal mentions curl, which this ask never offered: %v", err)
	}
}

func TestFetch_RefusesABodyOverTheCeiling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("["))
		chunk := strings.Repeat("x", 1<<16)
		for written := 1; written <= (16 << 20); written += len(chunk) {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).
		Fetch(context.Background(), srv.URL, apicoll.Route{Kind: apicoll.RouteDirect})
	if !errors.Is(err, apifetch.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestFetch_ReportsAServerRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := apifetch.New(directRoutes(), nil).
		Fetch(context.Background(), srv.URL, apicoll.Route{Kind: apicoll.RouteDirect})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the status in the sentence", err)
	}
}
```

Check `httppolicy.Local()` exists (`internal/apisend/dialer.go:42` calls it). If the helper's name differs, use whatever `apisend` itself passes for the direct route.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/apifetch/ -race`
Expected: FAIL — `no Go files in .../internal/apifetch`.

- [ ] **Step 3: Write the fetch seam**

`internal/apifetch/fetch.go`:

```go
// Package apifetch acquires an import document by URL, over the route the
// person chose.
//
// It is a package of its own for two reasons. internal/apiimport states that
// it does not reach the network (write.go), and it must go on being true —
// the converter is fed a reader and has no business knowing where the bytes
// came from. And the transport handler is not a place to keep an HTTP
// client: a fetch has a route, a ceiling, a timeout and a refusal
// vocabulary, which is a seam rather than four lines in a switch.
//
// WHAT IT REUSES, AND WHY IT IS NOT A SECOND SENDER. The route table is
// apisend's own (apisend.Routes): `direct` dials from this machine and
// `connection` leases the pooled SSH connection for a profile, refusing by
// name when it cannot — a fetch must never quietly go out around the bastion
// the person named, for the same reason a send must not (apisend/routes.go).
// The transport is httppolicy's, so the http:// address rule and the
// credential-across-origins rule apply here exactly as they do to every
// other request this product makes.
//
// WHAT IT DOES NOT REUSE: apisend.Client.Send. Its ceiling is 2 MiB with no
// option, deliberately (client.go), and an import document is bounded at
// apiimport.MaxDocumentBytes. Widening the sender for the import would put a
// knob on every send to serve one caller.
//
// IT NEVER VERIFIES LESS THAN NORMAL. route.InsecureTLS is ignored: the ask
// has no such control, and a fetch that could turn verification off would be
// a second place that decides it, beside the environment that owns it.
package apifetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
	"unicode"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/log"
)

const component = "apifetch"

// fetchTimeout bounds the whole exchange — dial, TLS, headers and body.
// A document that has not arrived in a minute is not arriving; the ask is
// modal and a person is watching it.
const fetchTimeout = time.Minute

var (
	// ErrScheme — the URL names something this cannot GET.
	ErrScheme = errors.New(component + ": an import URL must be http:// or https://")
	// ErrTooLarge — the body is over apiimport.MaxDocumentBytes.
	ErrTooLarge = errors.New(component + ": the document at that address is too large to import")
	// ErrNotADocument — the first byte says this is not JSON.
	//
	// It is refused HERE rather than passed on, and that is the point of
	// it: apiimport.ImportInto tells its entrances apart by first byte and
	// hands anything that is not JSON to the CURL parser, so a login page
	// fetched instead of an export would come back as a curl parse error in
	// a dialog that never offered curl.
	ErrNotADocument = errors.New(component + ": what is at that address is not a Postman export")
)

// Fetcher acquires the document at a URL. One method, because "get me those
// bytes" is the whole of what the import needs from the network.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, route apicoll.Route) ([]byte, error)
}

// Client is the Fetcher over a route table.
type Client struct {
	routes apisend.Routes
	log    log.Logger
}

// New builds a fetcher over the route table. A nil logger is allowed.
func New(routes apisend.Routes, l log.Logger) *Client {
	return &Client{routes: routes, log: l}
}

// Fetch GETs rawURL over route and returns the whole body.
//
// COMPLETELY, before anything is written: ImportInto's arrival is atomic and
// a half-read body must not be able to produce a half-collection. The cost is
// one document in memory, bounded by the same ceiling the parse uses.
func (c *Client) Fetch(ctx context.Context, rawURL string, route apicoll.Route) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %q is not a URL: %w", component, rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w (this one is %q)", ErrScheme, u.Scheme)
	}

	routeID, err := apisend.RouteIDFor(route)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", component, err)
	}
	r, err := c.routes(ctx, routeID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", component, err)
	}

	tr := httppolicy.NewTransport(httppolicy.Params{
		Component: component,
		Route:     r,
		Log:       c.log,
	})
	client := &http.Client{Transport: tr, CheckRedirect: tr.CheckRedirect, Timeout: fetchTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", component, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: fetching %s: %w", component, u.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: fetching %s: the server answered %d %s",
			component, u.Redacted(), resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, apiimport.MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", component, u.Redacted(), err)
	}
	if len(body) > apiimport.MaxDocumentBytes {
		return nil, fmt.Errorf("%w (the limit is %d bytes)", ErrTooLarge, apiimport.MaxDocumentBytes)
	}

	// The first byte, and never Content-Type: a server that mislabels JSON
	// is common and a server that labels HTML as JSON is not rare either,
	// so the bytes are the only witness worth believing. It is also the
	// same rule the rest of the import already lives by.
	for i, b := range body {
		if unicode.IsSpace(rune(b)) {
			continue
		}
		if b == '{' || b == '[' {
			return body[i:], nil
		}
		break
	}
	return nil, fmt.Errorf("%w: %s", ErrNotADocument, u.Redacted())
}
```

- [ ] **Step 4: Run the fetch tests to green**

Run: `go test ./internal/apifetch/ -race -v`
Expected: PASS, all five.

- [ ] **Step 5: Write the failing validator tests**

In `internal/transport/ws_api_handlers_test.go`, beside the existing `validateAPIImportPostmanRaw` cases:

```go
func TestValidateAPIImportPostman_ExactlyOneOfThreeSources(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   string // a substring the refusal must carry
	}{
		{"none", `{"dest":"/w/acme"}`, "path"},
		{"path and url", `{"path":"/w/a.json","url":"https://h/a.json","dest":"/w/acme"}`, "give one of them"},
		{"document and url", `{"document":"{}","url":"https://h/a.json","dest":"/w/acme"}`, "give one of them"},
		{"all three", `{"path":"/w/a.json","document":"{}","url":"https://h/a.json","dest":"/w/acme"}`, "give one of them"},
		{"route without url", `{"path":"/w/a.json","route":{"kind":"connection","profileId":"p"},"dest":"/w/acme"}`, "route"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateAPIImportPostmanRaw(json.RawMessage(c.params))
			if got == "" {
				t.Fatalf("accepted %s", c.params)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("refusal %q does not carry %q", got, c.want)
			}
		})
	}
}

func TestValidateAPIImportPostman_AcceptsAURLAlone(t *testing.T) {
	if msg := validateAPIImportPostmanRaw(
		json.RawMessage(`{"url":"https://h/a.json","route":{"kind":"direct"},"dest":"/w/acme"}`)); msg != "" {
		t.Fatalf("refused a valid URL import: %s", msg)
	}
}
```

- [ ] **Step 6: Run them and watch them fail**

Run: `go test ./internal/transport/ -run ValidateAPIImportPostman -race`
Expected: FAIL — the "path and url" case is accepted, because `url` is not yet a field.

- [ ] **Step 7: Widen the params and the validator**

`ws_api_handlers.go`, params — keep the existing comment and add to it:

```go
type apiImportPostmanParams struct {
	Path     string `json:"path"`
	Document string `json:"document"`
	// URL is the third and most general source: the document is neither on
	// the backend's disk nor in the renderer's hands, because it is behind a
	// network the renderer may not be on. Route says how to reach it.
	URL   string        `json:"url"`
	Route *apiRouteWire `json:"route"`
	Dest  string        `json:"dest"`
}
```

Validator — replace the two-way switch with the three-way one, keeping the by-name rule and its reasoning:

```go
func validateAPIImportPostmanRaw(raw json.RawMessage) string {
	var p apiImportPostmanParams
	if msg := decodeAPIParams(raw, &p); msg != "" {
		return msg
	}
	named := 0
	for _, given := range []bool{p.Path != "", p.Document != "", p.URL != ""} {
		if given {
			named++
		}
	}
	switch {
	case named > 1:
		return "path, document and url are three routes to one import — path names the export on the machine running nocx, document carries its bytes, url says where to fetch it; give one of them, not several"
	case named == 0:
		return "an import needs the export: path, naming it on the machine running nocx, document, carrying its bytes, or url, naming where to fetch it"
	}
	if p.Route != nil && p.URL == "" {
		return "route says how to REACH a url and means nothing beside path or document; give it with url or leave it out"
	}
	if p.Route != nil && !slices.Contains(apiRouteKinds, p.Route.Kind) {
		return fmt.Sprintf("route.kind must be one of %v", apiRouteKinds)
	}
	if p.Document != "" {
		if n := utf8.RuneCountInString(p.Document); n > maxAPIImportDocumentRunes {
			return fmt.Sprintf(
				"document exceeds %d characters; an export this large is imported with path, which names the file on the machine running nocx and sends no bytes over this socket",
				maxAPIImportDocumentRunes)
		}
	}
	if p.Path != "" {
		if msg := validateAPIFolderPath(p.Path, "path"); msg != "" {
			return msg
		}
	}
	return validateAPIFolderPath(p.Dest, "dest")
}
```

There is deliberately **no** length or shape check on `url` here — `apifetch` refuses a bad scheme by name and a bad host on the dial, and a second URL parser in the validator would be a second answer to "is this a URL".

- [ ] **Step 8: Add the capability method and dispatch to it**

`internal/capability/api.go`, on the interface:

```go
	// ImportPostmanURL fetches the export over route and writes the same
	// collection. It is the general route in the other direction from
	// ImportPostmanDocument: there the renderer had the bytes, here NOBODY
	// on this side has them yet, because the document lives on a network
	// the backend can reach and the renderer may not.
	ImportPostmanURL(ctx context.Context, rawURL string, route apicoll.Route, dest string) ([]apiimport.Unsupported, error)
```

On the service (which gains a `fetch apifetch.Fetcher` field, set by the constructor):

```go
func (s *apiImportService) ImportPostmanURL(ctx context.Context, rawURL string, route apicoll.Route, dest string) ([]apiimport.Unsupported, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	if s.fetch == nil {
		return nil, ErrImportURLUnavailable
	}
	// COMPLETELY, then write: ImportInto's arrival is atomic, and a fetch
	// that failed halfway must leave nothing behind at dest.
	doc, err := s.fetch.Fetch(ctx, rawURL, route)
	if err != nil {
		return nil, err
	}
	return apiimport.ImportInto(ctx, s.fsys, s.bindings, dest, bytes.NewReader(doc), route)
}
```

with, beside `ErrImportNotAFile`:

```go
// ErrImportURLUnavailable — this build has no fetcher, so the URL entrance
// is not offered. Absence is the capability (the rule the pickers follow);
// the renderer draws the entrance from what the backend answers rather than
// from what it hopes.
var ErrImportURLUnavailable = errors.New("capability: this build cannot fetch an import by URL")
```

Handler, in the existing chain:

```go
		switch {
		case p.URL != "":
			unsup, err = svc.ImportPostmanURL(ctx, p.URL, storedRoute(p.Route), p.Dest)
		case p.Document != "":
			unsup, err = svc.ImportPostmanDocument(ctx, p.Document, p.Dest)
		default:
			unsup, err = svc.ImportPostman(ctx, p.Path, p.Dest)
		}
```

and the small inverse beside `wireRoute`:

```go
// storedRoute is wireRoute's inverse for a route that may be ABSENT: no
// route is the direct one, which is the same normalisation wireRoute writes
// in the other direction.
func storedRoute(w *apiRouteWire) apicoll.Route {
	if w == nil {
		return apicoll.Route{Kind: apicoll.RouteDirect}
	}
	return apicoll.Route{Kind: w.Kind, ProfileID: w.ProfileID}
}
```

Note `InsecureTLS` is not carried: the ask has no such control (Task 1's rule, restated where a reader will look for it).

- [ ] **Step 9: Wire it at the composition root**

`internal/app/app.go`, beside `apiSender`:

```go
	// The import's URL entrance gets the SAME route table the sender has,
	// so "through prod-bastion" means one thing in this product: a fetch
	// and a send that name one connection lease the same pooled SSH
	// connection, and a connection that cannot be leased refuses both.
	apiRouteTable := apisend.NewRoutes(apiRoutes)
	apiSender := apisend.New(
		apisend.WithLogger(logger),
		apisend.WithRoutes(apiRouteTable),
	)
	apiFetcher := apifetch.New(apiRouteTable, logger)
```

and hand `apiFetcher` to whatever builds the import service (`capability.New…` call site — follow the existing parameter, do not add a package-level variable).

- [ ] **Step 10: Extend the over-the-wire conformance test**

Find `TestAPIImportPostman_OverTheWireConformsToContract` and add a URL case that runs the real result off the real socket, with an `httptest` server as the source. A test that only certifies two of three entrances certifies the wrong thing.

- [ ] **Step 11: Run everything you touched**

Run: `go test ./internal/apifetch/ ./internal/capability/ ./internal/transport/ ./internal/app/ -race`
Expected: PASS.

- [ ] **Step 12: Prove the seam is reachable**

Run:

```bash
deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/apifetch.Client.Fetch' ./...
deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/apifetch.Client.someUnwiredHelperIfAny' ./...
```

Expected: the first prints a path from `main`; put both outputs in the commit body. If there is no unwired symbol to contrast with, say so instead of inventing one.

- [ ] **Step 13: Commit**

```bash
git add internal/apifetch internal/capability internal/transport internal/app
git commit  # subject: feat(transport): api.import.postman takes a url, and the backend fetches it (<bead-id>)
```

---

### Task 3: The fetch goes through the connection the person chose

**Files:**

- Modify: `internal/apifetch/fetch.go` (nothing structural — this task is its tests plus the refusal's sentence)
- Test: `internal/apifetch/fetch_test.go`, `internal/transport/ws_api_handlers_test.go`
- Modify: `internal/capability/api_test.go` (the route reaches `ImportInto`)

**Interfaces:**

- Consumes: everything Task 2 produced; `apisend.ErrNoConnection` (`routes.go:51`).
- Produces: no new symbols. This task is the guarantee that the route parameter is not decorative.

**Acceptance Criteria:**

- A `route` naming `connection:<profile>` reaches the route table as exactly that id — asserted by a fake `apisend.Routes` recording what it was asked for.
- A connection that cannot be leased refuses the import with `apisend.ErrNoConnection`'s sentence, and **never** falls back to a direct dial. Asserted by a route table that returns `ErrNoConnection` and a dialer that fails the test if it is called.
- The collection minted from a connection-routed fetch has an environment whose route is that connection (the end-to-end of Task 1 through Task 2, asserted once at the capability level).
- `route.insecureTls: true` on the wire does not disable verification: the transport built for the fetch has no `TLSClientConfig`.

- [ ] **Step 1: Write the failing tests**

```go
func TestFetch_UsesTheRouteIdOfTheChosenConnection(t *testing.T) {
	var asked []string
	routes := apisend.Routes(func(ctx context.Context, routeID string) (httppolicy.Route, error) {
		asked = append(asked, routeID)
		return httppolicy.Local(), nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(export))
	}))
	defer srv.Close()

	_, err := apifetch.New(routes, nil).Fetch(context.Background(), srv.URL,
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(asked) != 1 || asked[0] != "connection:prod-bastion" {
		t.Fatalf("route table was asked for %v, want [connection:prod-bastion]", asked)
	}
}

func TestFetch_RefusesRatherThanDiallingDirectlyWhenTheConnectionIsDown(t *testing.T) {
	routes := apisend.Routes(func(context.Context, string) (httppolicy.Route, error) {
		return nil, apisend.ErrNoConnection
	})
	_, err := apifetch.New(routes, nil).Fetch(context.Background(), "https://example.invalid/a.json",
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion"})
	if !errors.Is(err, apisend.ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection — a fetch that fell back would go around the bastion", err)
	}
}
```

Add, in `internal/capability/api_test.go`, one test that a URL import through a connection writes an environment routed through it, using a stub `apifetch.Fetcher` that returns the export bytes.

- [ ] **Step 2: Run and watch the connection tests fail**

Run: `go test ./internal/apifetch/ ./internal/capability/ -race`
Expected: the route-id test fails first if `RouteIDFor` was not called; the capability test fails if the route is dropped between `Fetch` and `ImportInto`.

- [ ] **Step 3: Make them pass**

If Task 2 was written as specified, the fetch already asks `RouteIDFor` and passes `route` to `ImportInto`; these tests then pass on the first run. **Do not weaken a test to make it pass.** If one fails, the defect is in Task 2's code — fix it there.

- [ ] **Step 4: Run the suites**

Run: `go test ./internal/apifetch/ ./internal/capability/ ./internal/transport/ -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/apifetch internal/capability internal/transport
git commit  # subject: test(apifetch): a connection-routed fetch refuses rather than dialling around the bastion (<bead-id>)
```

---

### Task 4: The renderer decides what a paste IS, once

**Files:**

- Modify: `frontend/src/api/api-paths.ts`
- Test: `frontend/src/api/api-paths.test.ts`

**Interfaces:**

- Produces:
  - `export type PastedSource = { kind: 'url'; url: string } | { kind: 'document'; document: string } | { kind: 'unusable' }`
  - `export function classifyPastedSource(text: string): PastedSource`
  - `export function proposedDestinationFromDocument(defaultRoot: string, document: string): string`
  - `export function proposedDestinationFromURL(defaultRoot: string, url: string): string`

**Acceptance Criteria:**

- `classifyPastedSource` answers `url` for text whose trimmed form starts `http://` or `https://` (either case), `document` for text whose trimmed form starts `{` or `[`, and `unusable` for everything else, including `''`.
- `proposedDestinationFromDocument('/root', '{"info":{"name":"Acme API"}}')` is `/root/acme-api`, via the existing `slugify`.
- It answers `''` for malformed JSON, for a document with no `info.name`, for a name that slugifies to `''`, and for an empty `defaultRoot` — never throws.
- `proposedDestinationFromURL('/root', 'https://h/x/acme.postman_collection.json')` is `/root/acme`; a URL with no usable last segment (`https://api.postman.com/collections/1234-abc` → `1234-abc` IS usable; `https://h/` is not) answers `''`.
- No other module re-derives "is this a URL".

- [ ] **Step 1: Write the failing tests**

```ts
import { describe, expect, it } from 'vitest'
import {
  classifyPastedSource,
  proposedDestinationFromDocument,
  proposedDestinationFromURL,
} from './api-paths'

describe('classifyPastedSource', () => {
  it('calls http and https a URL, whatever the case and the surrounding space', () => {
    expect(classifyPastedSource('  https://h/a.json \n')).toEqual({
      kind: 'url',
      url: 'https://h/a.json',
    })
    expect(classifyPastedSource('HTTP://h/a.json')).toEqual({ kind: 'url', url: 'HTTP://h/a.json' })
  })

  it('calls JSON a document', () => {
    expect(classifyPastedSource(' {"info":{}} ')).toEqual({
      kind: 'document',
      document: '{"info":{}}',
    })
    expect(classifyPastedSource('[]')).toEqual({ kind: 'document', document: '[]' })
  })

  it('calls anything else unusable — including a curl line this ask never offered', () => {
    expect(classifyPastedSource('curl https://h -X POST')).toEqual({ kind: 'unusable' })
    expect(classifyPastedSource('')).toEqual({ kind: 'unusable' })
    expect(classifyPastedSource('   ')).toEqual({ kind: 'unusable' })
  })
})

describe('proposedDestinationFromDocument', () => {
  it('offers the collection name, slugified', () => {
    expect(proposedDestinationFromDocument('/root', '{"info":{"name":"Acme API"}}')).toBe(
      '/root/acme-api',
    )
  })

  it('offers nothing rather than throwing, for everything it cannot read', () => {
    expect(proposedDestinationFromDocument('/root', 'not json')).toBe('')
    expect(proposedDestinationFromDocument('/root', '{"info":{}}')).toBe('')
    expect(proposedDestinationFromDocument('/root', '{"info":{"name":"***"}}')).toBe('')
    expect(proposedDestinationFromDocument('', '{"info":{"name":"Acme"}}')).toBe('')
  })
})

describe('proposedDestinationFromURL', () => {
  it('offers the last segment without its suffixes', () => {
    expect(proposedDestinationFromURL('/root', 'https://h/x/acme.postman_collection.json')).toBe(
      '/root/acme',
    )
    expect(
      proposedDestinationFromURL('/root', 'https://api.postman.com/collections/1234-abc'),
    ).toBe('/root/1234-abc')
  })

  it('offers nothing when there is no last segment to take', () => {
    expect(proposedDestinationFromURL('/root', 'https://h/')).toBe('')
    expect(proposedDestinationFromURL('/root', 'not a url')).toBe('')
  })
})
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd frontend && npx vitest run src/api/api-paths.test.ts`
Expected: FAIL — the three functions do not exist.

- [ ] **Step 3: Implement them**

Append to `frontend/src/api/api-paths.ts`:

```ts
/**
 * What a pasted string IS, decided HERE and nowhere else.
 *
 * Two derivations of this question is the defect AGENTS.md names about `ssh`
 * without a trailing space: they agree on every case anybody tries and
 * disagree on the one that matters. The ask, the destination offer and the
 * client call all read this one answer.
 *
 * `unusable` is a real answer rather than an error: a person who pasted a
 * curl line gets a sentence from the ask, and no round trip is spent to
 * learn what the form already knew. Curl is not this ask's question — it has
 * its own door in the request editor.
 */
export type PastedSource =
  { kind: 'url'; url: string } | { kind: 'document'; document: string } | { kind: 'unusable' }

export function classifyPastedSource(text: string): PastedSource {
  const trimmed = text.trim()
  if (trimmed === '') return { kind: 'unusable' }
  if (/^https?:\/\//i.test(trimmed)) return { kind: 'url', url: trimmed }
  if (trimmed.startsWith('{') || trimmed.startsWith('['))
    return { kind: 'document', document: trimmed }
  return { kind: 'unusable' }
}

/**
 * The destination a PASTED EXPORT proposes: `<defaultRoot>/<slug of
 * info.name>`.
 *
 * A syntactic offer and not a parse of the format — the module's own rule at
 * the top of this file. It reads one field, validates nothing, refuses
 * nothing, and answers '' for every failure, because the backend is the only
 * reader of hostile input and this is a suggestion in a field the person can
 * overwrite.
 */
export function proposedDestinationFromDocument(defaultRoot: string, document: string): string {
  if (defaultRoot === '') return ''
  let name = ''
  try {
    const parsed: unknown = JSON.parse(document)
    const info = (parsed as { info?: { name?: unknown } } | null)?.info
    if (typeof info?.name === 'string') name = info.name
  } catch {
    return ''
  }
  const slug = slugify(name)
  if (slug === '') return ''
  return `${defaultRoot.replace(/[\\/]+$/, '')}/${slug}`
}

/**
 * The destination a URL proposes: the last path segment, without any of its
 * suffixes, exactly as proposedDestination treats a file name.
 *
 * '' when the URL has no last segment — a share link ending in a slash — and
 * the ask then opens the destination as an empty required field rather than
 * proposing a folder named after nothing.
 */
export function proposedDestinationFromURL(defaultRoot: string, url: string): string {
  if (defaultRoot === '') return ''
  let path = ''
  try {
    path = new URL(url).pathname
  } catch {
    return ''
  }
  const last =
    path
      .split('/')
      .filter((s) => s !== '')
      .pop() ?? ''
  const stem = decodeURIComponent(last).split('.')[0].trim()
  if (stem === '') return ''
  return `${defaultRoot.replace(/[\\/]+$/, '')}/${stem}`
}
```

- [ ] **Step 4: Run to green**

Run: `cd frontend && npx vitest run src/api/api-paths.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/api/api-paths.ts frontend/src/api/api-paths.test.ts
git commit  # subject: feat(frontend): what a paste IS is decided once, beside the destination it proposes (<bead-id>)
```

---

### Task 5: A URL is a third `ImportSource`

**Files:**

- Modify: `frontend/src/api/api-client.ts:257` (the union), `:211` (the method's comment), `:422` (the services interface — no signature change)
- Modify: `frontend/src/api/api-store.ts:395,1295` (comments only — the type flows through)
- Test: `frontend/src/api/api-client.test.ts`

**Interfaces:**

- Produces: `export type ImportSource = { path: string } | { document: string } | { url: string; route?: ApiRoute }`

**Acceptance Criteria:**

- `client.importPostman({ url: 'https://h/a.json', route: { kind: 'connection', profileId: 'p', insecureTls: false } }, '/w/acme')` dispatches `api.import.postman` with exactly `{ url, route, dest }` and no other key.
- The existing property test asserting that no method spells a filesystem path except `openCollection` and `importPostman` still passes unedited.
- A URL source without a route dispatches `{ url, dest }` — no `route: undefined` key on the wire.

- [ ] **Step 1: Write the failing test**

```ts
it('sends a URL import as url plus route and nothing else', async () => {
  const { client, calls } = makeClient()
  await client.importPostman(
    { url: 'https://h/a.json', route: { kind: 'connection', profileId: 'p', insecureTls: false } },
    '/w/acme',
  )
  expect(calls).toEqual([
    {
      method: 'api.import.postman',
      params: {
        url: 'https://h/a.json',
        route: { kind: 'connection', profileId: 'p', insecureTls: false },
        dest: '/w/acme',
      },
    },
  ])
})

it('omits route entirely when there is none', async () => {
  const { client, calls } = makeClient()
  await client.importPostman({ url: 'https://h/a.json' }, '/w/acme')
  expect(Object.keys(calls[0].params)).toEqual(['url', 'dest'])
})
```

Use whatever fixture the neighbouring tests already use to build a client and record dispatches — do not add a second one.

- [ ] **Step 2: Run and watch it fail**

Run: `cd frontend && npx vitest run src/api/api-client.test.ts`
Expected: FAIL — the object literal is not assignable to `ImportSource`.

- [ ] **Step 3: Widen the union**

```ts
/**
 * How the export reaches the import — three ways, spread onto the params as
 * the one field each carries, so `importPostman` never has to know which it
 * was handed.
 *
 * `path` names a file on the machine running the backend; `document` carries
 * the bytes; `url` names where the backend should fetch it, over `route`.
 * The third is the general case in the direction the second cannot serve: a
 * document behind a network the renderer is not on. Absent `route` is the
 * direct one, and it is absent from the object rather than present and
 * undefined — a key the backend would refuse to decode.
 */
export type ImportSource =
  { path: string } | { document: string } | { url: string; route?: ApiRoute }
```

`ApiRoute` is already exported from `./api-model`. The spread in `importPostman` needs no change; confirm that `{ url, route: undefined }` cannot be constructed by the callers in Task 7 (build the object conditionally there).

- [ ] **Step 4: Run to green**

Run: `cd frontend && npx vitest run src/api/api-client.test.ts && npx tsc --noEmit`
Expected: PASS, and no type errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/api/api-client.ts frontend/src/api/api-client.test.ts frontend/src/api/api-store.ts
git commit  # subject: feat(frontend): a URL is the import's third source, carrying its route (<bead-id>)
```

---

### Task 6: The ask loses its path fields

**Files:**

- Modify: `frontend/src/api/import-dialogs.tsx` (`PostmanImportDialog`)
- Modify: `frontend/src/api/api-pane.tsx:231-300` (state), `:633` (`askForImport`), `:928` (`runImport`), `:1190` (the props it passes)
- Test: `frontend/src/api/api-workbench.test.tsx`

**Interfaces:**

- Consumes: Task 4's `classifyPastedSource`, `proposedDestinationFromDocument`, `proposedDestinationFromURL`; Task 5's `ImportSource`.
- Produces: `PostmanImportDialogProps` gains `pasted: string`, `onPaste: (v: string) => void`, `sourceLabel: string` (the held source, or `''`), `onClearSource: () => void`, `editingDest: boolean`, `onEditDest: () => void`; loses nothing — `file`/`onFile` stay, because a native drop and the native picker still answer with a path.

**Acceptance Criteria:**

- Opening the ask shows: a paste box with `id="api-import-paste"`, the `DropZone`, and a destination summary line — and **no** visible `#api-import-postman-file` or `#api-import-postman-dest` field.
- Pasting `{"info":{"name":"Acme API"}}` shows the source line and proposes `<defaultRoot>/acme-api` in the summary.
- Pasting `curl https://h` shows a refusal in the ask, leaves the summary empty, keeps Import disabled, and dispatches nothing.
- Choosing a file (the existing picker) still fills the source line with the file's name and proposes `<defaultRoot>/<stem>`; the field `#api-import-postman-file` exists in the DOM only while the source is a path chosen that way (keep it hidden-but-present if the existing drop tests address it, and say so in a comment).
- Clicking the pencil reveals `#api-import-postman-dest` with its Browse button; the id is unchanged.
- Clearing the source empties the summary and disables Import.
- A second source replaces the first: pasting a document after choosing a file leaves exactly one source held, and the call carries `document`, not `path`.

- [ ] **Step 1: Write the failing tests**

In `api-workbench.test.tsx`, beside the existing import tests:

```tsx
it('opens with a paste box and no path fields', async () => {
  const { bar } = await mountApp({})
  await openImport(bar)
  expect(document.querySelector('#api-import-paste')).not.toBeNull()
  expect(reachable(document.querySelector('#api-import-postman-dest') as HTMLInputElement)).toBe(
    false,
  )
})

it('proposes the collection name from a pasted export', async () => {
  const { bar } = await mountApp({})
  await openImport(bar)
  fireEvent.input(field('api-import-paste'), { target: { value: '{"info":{"name":"Acme API"}}' } })
  await vi.waitFor(() =>
    expect(screen.getByText(`Imports into: ${DEFAULT_ROOT}/acme-api`)).toBeTruthy(),
  )
})

it('refuses a curl line here rather than spending a round trip on it', async () => {
  const importPostman = vi.fn()
  const { bar } = await mountApp({ importPostman })
  await openImport(bar)
  fireEvent.input(field('api-import-paste'), { target: { value: 'curl https://h -X POST' } })
  await vi.waitFor(() => expect(screen.getByText(/not a Postman export/i)).toBeTruthy())
  expect(importButton().disabled).toBe(true)
  expect(importPostman).not.toHaveBeenCalled()
})

it('sends the pasted document, and a second source replaces the first', async () => {
  const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
  const { bar } = await mountApp({ importPostman })
  await openImport(bar)
  // a path first, the way the picker answers
  fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/acme.json' } })
  fireEvent.input(field('api-import-paste'), { target: { value: '{"info":{"name":"Acme"}}' } })
  fireEvent.click(importButton())
  await vi.waitFor(() =>
    expect(importPostman).toHaveBeenCalledWith(
      { document: '{"info":{"name":"Acme"}}' },
      `${DEFAULT_ROOT}/acme`,
    ),
  )
})

it('reveals the destination field behind the pencil, under its own id', async () => {
  const { bar } = await mountApp({})
  await openImport(bar)
  fireEvent.input(field('api-import-paste'), { target: { value: '{"info":{"name":"Acme"}}' } })
  fireEvent.click(screen.getByRole('button', { name: /change where/i }))
  await vi.waitFor(() => expect(reachable(field('api-import-postman-dest'))).toBe(true))
})
```

`openImport`, `field`, `reachable`, `mountApp`, `DEFAULT_ROOT` and `importButton` follow the file's existing helpers — reuse them, and add `openImport`/`importButton` as helpers only if the file has none.

- [ ] **Step 2: Run and watch them fail**

Run: `cd frontend && npx vitest run src/api/api-workbench.test.tsx -t import`
Expected: FAIL — `#api-import-paste` does not exist.

- [ ] **Step 3: Reshape the dialog**

`import-dialogs.tsx`, `PostmanImportDialog`'s body becomes, in order:

```tsx
<TextField
  id="api-import-paste"
  label="Paste a Postman export or a URL"
  description="Read, never executed."
  multiline
  value={props.pasted}
  error={props.pasteRefusal !== '' ? props.pasteRefusal : undefined}
  onInput={props.onPaste}
  autoFocus
/>
<DropZone …unchanged…>
  {/* The path field stays, and stays ADDRESSABLE: a native drop and the
      system picker both answer with a path, and every existing drop test
      addresses this id. It is no longer a field a person types into —
      it is where the answer to the gesture lands. */}
  <TextField id="api-import-postman-file" … hidden={props.sourceKind !== 'path'} … />
</DropZone>
<Show when={props.sourceLabel !== ''}>
  <p class="api-import-source">
    {props.sourceLabel}
    <IconButton size="sm" ariaLabel="Forget this source" onClick={props.onClearSource}>
      <CloseIcon />
    </IconButton>
  </p>
</Show>
<Show when={props.editingDest} fallback={
  <p class="api-import-dest">
    {props.dest === '' ? 'Choose where this goes' : `Imports into: ${props.dest}`}
    <IconButton size="sm" ariaLabel="Change where this goes" onClick={props.onEditDest}>
      <PencilIcon />
    </IconButton>
  </p>
}>
  <TextField id="api-import-postman-dest" …unchanged, including its trailing Browse… />
</Show>
```

The two `<p>` elements carry layout only. If a colour, a border or a font is wanted there, that is a missing kit variant — add it in `ui/`, never here.

- [ ] **Step 4: Thread the state in the pane**

In `api-pane.tsx`, add `postmanPasted`, `pasteRefusal`, `editingDest` signals; on paste, run `classifyPastedSource` and set the source, the refusal (`''`, or "That is not a Postman export or a URL — paste the export's text, or drop the file below.") and the proposed destination via Task 4's helpers, respecting the existing rule that a destination the person edited is never overwritten. In `runImport`, build the `ImportSource` from the held source rather than from the file field alone.

- [ ] **Step 5: Run to green**

Run: `cd frontend && npx vitest run src/api/api-workbench.test.tsx && npx tsc --noEmit`
Expected: PASS — including every pre-existing import and drop test, unedited except where a test asserted a field is _visible_.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/api frontend/src/styles
git commit  # subject: feat(frontend): the import ask asks one question instead of two paths (<bead-id>)
```

---

### Task 7: A URL reveals the connection it travels through

**Files:**

- Modify: `frontend/src/api/import-dialogs.tsx`, `frontend/src/api/api-pane.tsx`
- Test: `frontend/src/api/api-workbench.test.tsx`

**Interfaces:**

- Consumes: `store.connections()` (`api-store.ts:322`), `ApiConnection`, `ApiRoute`, the `Select` kit component (`frontend/src/ui/select.tsx`).
- Produces: `PostmanImportDialogProps` gains `connections: readonly ApiConnection[]`, `route: ApiRoute`, `onRoute: (r: ApiRoute) => void`.

**Acceptance Criteria:**

- Pasting a URL reveals a connection `Select`; pasting a document hides it again.
- The `Select` offers "Direct" plus one option per connection, in `store.connections()`'s order, with the connection's `name` as the label and its `id` as the value — the same grammar as `environment-view.tsx:311`.
- Where `listConnections` is absent (no profile store), only "Direct" is drawn — a picker over nothing is not offered, which is the rule the other pickers already follow.
- Choosing a connection and pressing Import calls `importPostman({ url, route: { kind: 'connection', profileId, insecureTls: false } }, dest)`.
- Leaving it on Direct calls `importPostman({ url }, dest)` — with no `route` key at all.
- The workbench passes `store.connections()` to the ask; the ask does not fetch anything itself.

- [ ] **Step 1: Write the failing tests**

```tsx
it('reveals the connection picker for a URL and hides it for a document', async () => {
  const { bar } = await mountApp({
    listConnections: async () => [{ id: 'p1', name: 'prod-bastion' }],
  })
  await openImport(bar)
  fireEvent.input(field('api-import-paste'), { target: { value: 'https://h/acme.json' } })
  await vi.waitFor(() => expect(document.querySelector('#api-import-route')).not.toBeNull())
  fireEvent.input(field('api-import-paste'), { target: { value: '{"info":{"name":"A"}}' } })
  await vi.waitFor(() => expect(document.querySelector('#api-import-route')).toBeNull())
})

it('sends the chosen connection as the route', async () => {
  const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
  const { bar } = await mountApp({
    importPostman,
    listConnections: async () => [{ id: 'p1', name: 'prod-bastion' }],
  })
  await openImport(bar)
  fireEvent.input(field('api-import-paste'), { target: { value: 'https://h/acme.json' } })
  await vi.waitFor(() => expect(document.querySelector('#api-import-route')).not.toBeNull())
  fireEvent.change(field('api-import-route'), { target: { value: 'p1' } })
  fireEvent.click(importButton())
  await vi.waitFor(() =>
    expect(importPostman).toHaveBeenCalledWith(
      {
        url: 'https://h/acme.json',
        route: { kind: 'connection', profileId: 'p1', insecureTls: false },
      },
      `${DEFAULT_ROOT}/acme`,
    ),
  )
})

it('sends no route at all when the fetch goes direct', async () => {
  const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
  const { bar } = await mountApp({ importPostman })
  await openImport(bar)
  fireEvent.input(field('api-import-paste'), { target: { value: 'https://h/acme.json' } })
  fireEvent.click(importButton())
  await vi.waitFor(() =>
    expect(importPostman).toHaveBeenCalledWith(
      { url: 'https://h/acme.json' },
      `${DEFAULT_ROOT}/acme`,
    ),
  )
})
```

- [ ] **Step 2: Run and watch them fail**

Run: `cd frontend && npx vitest run src/api/api-workbench.test.tsx -t connection`
Expected: FAIL — `#api-import-route` does not exist.

- [ ] **Step 3: Draw the picker and build the source**

In the dialog, under the paste box:

```tsx
<Show when={props.sourceKind === 'url' && props.connections.length > 0}>
  <Field for="api-import-route" label="Fetch through">
    <Select
      ariaLabel="The connection this fetch goes through"
      value={props.route.kind === 'connection' ? props.route.profileId : ''}
      onChange={(profileId) =>
        props.onRoute(
          profileId === ''
            ? { kind: 'direct', profileId: '', insecureTls: false }
            : { kind: 'connection', profileId, insecureTls: false },
        )
      }
      options={props.connections.map((c) => ({ value: c.id, label: c.name }))}
      placeholder="Direct"
      placeholderValue=""
    />
  </Field>
</Show>
```

Give the underlying `<select>` the id `api-import-route` the way `environment-view.tsx` gives its own — follow that file, do not invent a second pattern. In the pane, build the source:

```ts
const importSource = (): ImportSource | null => {
  const held = source()
  if (held === null) return null
  if (held.kind === 'url') {
    // The route is OMITTED rather than sent as direct: the backend reads an
    // absent route as direct (storedRoute), and a key spelled `undefined`
    // is a key the strict decoder refuses.
    return route().kind === 'connection' ? { url: held.url, route: route() } : { url: held.url }
  }
  …
}
```

- [ ] **Step 4: Run to green**

Run: `cd frontend && npx vitest run src/api/api-workbench.test.tsx && npx tsc --noEmit && npx eslint .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/api
git commit  # subject: feat(frontend): a URL import says which connection it travels through (<bead-id>)
```

---

### Task 8: The epic's happy path, watched end to end

**Files:**

- Create: `e2e/api-import-url.spec.ts`
- Modify: `.internal/specs/2026-08-23-import-ask-postman-shape-design.md` (only if implementation proved a section wrong — say which and why in the commit)

**Interfaces:**

- Consumes: everything above. The backend under test is `cmd/devharness` (headless, no Wails, no display), per `e2e/preflight.ts`'s rules about `NOCX_E2E_HOME_DIR`.

**Acceptance Criteria:**

- One spec drives the real product: open the workbench, open the import ask, paste the URL of an export served by a local HTTP server the spec starts, press Import, and assert the collection appears in the tree.
- It then opens the collection's environment and asserts the route the import wrote — `direct` for a direct fetch, and, if the harness can offer a connection, the connection for a routed one. If it cannot, the spec says so in a comment and the gap gets a bead rather than a silence (the rule `nocx-twmaf` already established for this epic).
- The spec waits on observable state — a row in the tree, a value in a field — and never on a duration.
- `e2e/run-in-container.sh e2e/api-import-url.spec.ts` passes locally; the coordinator confirms in CI, which is the source of truth.

- [ ] **Step 1: Read what the epic already watched**

Run: `bd show nocx-twmaf` and read `e2e/` for the existing import spec. The new spec extends that vocabulary; it does not start a second one.

- [ ] **Step 2: Write the spec**

Serve the export from a `http.createServer` started inside the spec on an ephemeral port, so the fetch is real and the fixture is local. Assert, in order: the ask opens; the pasted URL reveals the route control; Import closes the ask; the collection row appears in the tree; the environment file carries the expected route.

- [ ] **Step 3: Run it**

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/api-import-url.spec.ts`
Expected: PASS. Remember the container's failure set is not CI's — a layout-sensitive failure there is checked against CI before it is "fixed".

- [ ] **Step 4: File what is left**

Any gap the spec could not watch becomes a bead with a dependency edge, in the same minute — a reported blocker that is not a bead evaporates between rounds.

- [ ] **Step 5: Commit**

```bash
git add e2e
git commit  # subject: test(e2e): watch an export arrive by URL and keep its route (<bead-id>)
```

---

## Self-Review

**Spec coverage:**

| Spec section                                                                                               | Task                                                       |
| ---------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| §1 the surface (paste box, drop region, summary line, source line, Import stays)                           | T6                                                         |
| §2 four entrances, one source, the discrimination rule, renderer-side refusal of non-JSON                  | T4, T6                                                     |
| §3 destination offers for file / document / URL, and the nothing-to-offer case                             | T4, T6                                                     |
| §4 the wire: exactly one of three, `route` beside `url`, result unchanged                                  | T2                                                         |
| §5 the fetch: seam, GET, no auth, route, ceiling, timeout, redirects, first-byte guard, fetch-before-write | T2, T3                                                     |
| §5 SSRF reasoning recorded rather than assumed                                                             | spec text; T2's package comment carries the operative half |
| §6 the environment inherits the route; `insecureTls` does not                                              | T1, T3                                                     |
| §7 the kit: no new component, no repaint                                                                   | T6, T7 (icons already exist — no registry task needed)     |
| §8 supersedes openapi §7                                                                                   | done in the spec commit `e211c15e`                         |
| §9 testing: happy path, failure paths + positives, over-the-wire on the URL route, `-whylive`              | T2, T3, T8                                                 |

**Deliberate note, surfaced rather than silent:** the spec says the pencil icon "goes into the icon registry". `PencilIcon` and `CloseIcon` are already there, so there is no task for it — the plan uses them.

**Placeholder scan:** no TBDs; every code step carries the code; no "similar to Task N".

**Type consistency:** `ImportInto(..., route apicoll.Route)` (T1) is called with the same parameter in T2's `ImportPostmanURL`; `apifetch.Fetcher.Fetch(ctx, rawURL, route)` is the only fetch signature used anywhere; `classifyPastedSource`'s `PastedSource` kinds (`url` / `document` / `unusable`) are the same strings T6 and T7 switch on; `ImportSource`'s URL member (`{ url, route? }`) matches what T7 constructs and what T2's params decode.
