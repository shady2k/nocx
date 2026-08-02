# Wave 1 — Credential correctness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** Editing a credential no longer destroys its stored password, create is
distinguishable from update for every entity, IDs are minted by the backend, and the
credential record carries secret **versions** so later waves can stage a fleet rotation
without rewriting this layer.

**Architecture:** The transport handler stops treating the renderer's DTO as the whole
record. A new `internal/profile` credential service owns the merge of a sparse renderer
patch onto the stored record, so backend-owned fields (`SecretID`, `PassphraseSecretID`,
version list) cannot be blanked by a round trip that never saw them. Secret material still
lives only behind `credential.SecretStore` (ADR-0011 §2); nothing here changes that.

**Tech Stack:** Go 1.x, `internal/profile` (domain + JSONStore over
`storage.DocumentStore`), `internal/transport` (JSON-RPC over WebSocket),
`internal/credential` (`SecretStore`, keychain-backed), `internal/connection` (resolver).
Tests are stdlib `testing`, run with `-race`.

## Global Constraints

- Spec: `.internal/specs/2026-07-29-connection-manager-design.md` (rev. 3). Every task here
  maps to its §2.1, §2.4, §3.9, or the `nocx-u5ai` entry in §2.11.
- **No secret value crosses to the renderer.** ADR-0011 §2 is a type boundary, not a
  redaction convention: `credentials.*` responses carry no `SecretID`, no
  `PassphraseSecretID`, and no plaintext. Any new RPC field is checked against this before
  it is added.
- **Backend-owned fields are rejected, not ignored, when the renderer sends them.** The
  existing handler already does this for `secretId`/`passphraseSecretId`
  (`ws.go:1113-1117`); the new version fields join that list.
- AGENTS.md: TDD red → green → refactor; every commit names its bead; the full local gate
  (`gofumpt -l .`, `golangci-lint run`, `go test -race ./...`) passes before push.
- This wave is **self-sufficient**: it lands on `main` with no half-built surface. It
  changes no UI.
- Wave 1 does **not** touch the pool. Spec rev. 3 withdrew that finding — `poolKeyFor`
  already keys on `cfg.SecretID` (`ssh_dial.go:38`). The only pool-adjacent requirement here
  is Task 7: the resolver must publish the **selected version's** `SecretID`.

---

## File Structure

| File                                                | Responsibility                                                                                                                                              |
| --------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/profile/credential.go` _(new)_            | The `Credential` type and its versions, moved out of `profile.go`; `Validate`; `NewCredentialID`; the patch type and its merge. One file for one aggregate. |
| `internal/profile/credential_test.go` _(new)_       | Unit tests for the merge, the version model and ID minting.                                                                                                 |
| `internal/profile/profile.go` _(modify)_            | Loses the `Credential` block (lines 96-153) to `credential.go`. Nothing else moves in this wave.                                                            |
| `internal/profile/store.go` _(modify)_              | `SaveCredential` gains create/update distinction; `CredentialMetadataRepository` grows `CreateCredential`/`UpdateCredential`.                               |
| `internal/profile/store_test.go` _(new)_            | Store-level create/update/conflict tests.                                                                                                                   |
| `internal/transport/ws.go` _(modify)_               | `handleCredentialCRUDMethod` splits create from update and applies a patch; `handleProfileMethod`/`handleGroupMethod` likewise.                             |
| `internal/transport/ws_profiles_test.go` _(modify)_ | RPC-level tests, including the regression that names this wave.                                                                                             |
| `internal/connection/resolver.go` _(modify)_        | Selects the active version's secret references.                                                                                                             |
| `internal/connection/resolver_test.go` _(modify)_   | Version-selection tests.                                                                                                                                    |

---

## Task 1: The regression test that names this wave

**Files:**

- Test: `internal/transport/ws_profiles_test.go` (append)

**Interfaces:**

- Consumes: existing helpers `newRegWithStub`, `connectWS`, `jsonrpcCall` (`ws_test.go:24`,
  `:42`, `:57`), `profile.NewJSONStore`, `WithCredentialMetadataRepository`.
- Produces: nothing — this task only proves the defect exists.

**Acceptance Criteria:**

- A test creates a credential, stores a password for it, renames it through
  `credentials.update`, and then asserts the credential still has a reachable password.
- The test **fails** on current `main`, and its failure message names the lost `SecretID`.

- [ ] **Step 1: Write the failing test**

Append to `internal/transport/ws_profiles_test.go`:

```go
// TestCredentialsRPC_UpdatePreservesSecretID is the regression for the defect that
// started wave 1: credentials.list strips SecretID (correct, ADR-0011 §2), and the
// update handler used to write the stripped DTO back whole, so a rename orphaned the
// stored password. The keychain entry survived; nothing pointed at it any more.
func TestCredentialsRPC_UpdatePreservesSecretID(t *testing.T) {
	keyring.MockInit()

	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialMetadataRepository(ps),
		WithCredentials(credential.NewKeychain()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Create the credential.
	created := jsonrpcCall(t, conn, "credentials.create", map[string]any{
		"name":     "prod-ops",
		"username": "ops",
		"auth":     "password",
	})
	var createResp struct {
		Result profile.Credential `json:"result"`
	}
	if err := json.Unmarshal(created, &createResp); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	id := createResp.Result.ID
	if id == "" {
		t.Fatal("create returned an empty credential ID")
	}

	// Store a password for it.
	jsonrpcCall(t, conn, "credentials.savePassword", map[string]any{
		"credentialId": id,
		"password":     "s3cret",
	})

	before, _, err := ws.findCredentialByID(id)
	if err != nil {
		t.Fatalf("load before: %v", err)
	}
	if before.SecretID == "" {
		t.Fatal("precondition failed: savePassword did not set a SecretID")
	}

	// Rename it — exactly what the renderer sends, with no secret references,
	// because credentials.list never gave it any.
	jsonrpcCall(t, conn, "credentials.update", map[string]any{
		"id":       id,
		"name":     "prod-ops-renamed",
		"username": "ops",
		"auth":     "password",
	})

	after, ok, err := ws.findCredentialByID(id)
	if err != nil {
		t.Fatalf("load after: %v", err)
	}
	if !ok {
		t.Fatal("credential disappeared after update")
	}
	if after.Name != "prod-ops-renamed" {
		t.Errorf("Name = %q, want prod-ops-renamed", after.Name)
	}
	if after.SecretID != before.SecretID {
		t.Errorf("SecretID = %q, want %q — the update orphaned the stored password",
			after.SecretID, before.SecretID)
	}
}
```

- [ ] **Step 2: Run it and watch it fail for the right reason**

Run: `go test ./internal/transport/ -run TestCredentialsRPC_UpdatePreservesSecretID -v`

Expected: FAIL with `SecretID = "", want "sec:…" — the update orphaned the stored password`.
If it fails with a compile error on `WithCredentials`, check the option's real name with
`grep -n "func WithCredentials" internal/transport/ws.go` and use that — do not invent one.

- [ ] **Step 3: Commit the red test**

```bash
git add internal/transport/ws_profiles_test.go
git commit -m "test(transport): prove a credential rename orphans its password (nocx-52cd)"
```

---

## Task 2: A credential patch that cannot blank what it never saw

**Files:**

- Create: `internal/profile/credential.go`
- Create: `internal/profile/credential_test.go`
- Modify: `internal/profile/profile.go` (remove lines 96-153, the `Credential` block,
  `NewCredentialID`, `ErrCredentialHostRequired`, `Validate`)

**Interfaces:**

- Produces:
  - `type CredentialPatch struct { Name, Username *string; Auth *AuthMode; KeyPath *string }`
  - `func (c Credential) WithPatch(p CredentialPatch) Credential`
  - `func NewCredentialID(name string) string` (moved, unchanged behaviour)
- Consumes: `slugify`, `newUUID` from `profile.go` (same package).

**Acceptance Criteria:**

- A patch with only `Name` set changes only `Name`; `SecretID`, `PassphraseSecretID`,
  `Username`, `Auth` and `KeyPath` are carried over from the stored record untouched.
- A patch field that is present but empty (`*p.KeyPath == ""`) **clears** that field —
  presence is the signal, not emptiness. This is the same presence-versus-zero rule §3.3 of
  the spec makes mandatory for group defaults; it starts here.
- `Credential.Host`, `Credential.Port` and `ErrCredentialHostRequired` are **carried over
  unchanged**. Spec §3.1 deletes them in **wave 2**, in the same commit range that makes
  computed authorization live: `checkBinding` (`ssh_config.go:105`) refuses a connection
  whose `BoundHost` is empty, and the resolver fills it from `Credential.Host`
  (`resolver.go:85-86`), so removing the field here would break every stored password until
  wave 2 landed. The move is a file move, not a semantic change.
- `CredentialPatch` deliberately **does** carry `Host` and `Port` while they exist — they
  are renderer-owned today (the credential form edits them), unlike `SecretID`.

- [ ] **Step 1: Write the failing test**

Create `internal/profile/credential_test.go`:

```go
package profile

import "testing"

func TestCredentialWithPatch_PreservesUnsetFields(t *testing.T) {
	stored := Credential{
		ID:                 "cred:prod-ops:abc",
		Name:               "prod-ops",
		Username:           "ops",
		Auth:               AuthPassword,
		SecretID:           "sec:1111",
		PassphraseSecretID: "sec:2222",
	}

	name := "prod-ops-renamed"
	got := stored.WithPatch(CredentialPatch{Name: &name})

	if got.Name != "prod-ops-renamed" {
		t.Errorf("Name = %q, want prod-ops-renamed", got.Name)
	}
	if got.Username != "ops" {
		t.Errorf("Username = %q, want ops — an unset patch field must not clear it", got.Username)
	}
	if got.SecretID != "sec:1111" {
		t.Errorf("SecretID = %q, want sec:1111 — backend-owned and not patchable", got.SecretID)
	}
	if got.PassphraseSecretID != "sec:2222" {
		t.Errorf("PassphraseSecretID = %q, want sec:2222", got.PassphraseSecretID)
	}
	if got.ID != "cred:prod-ops:abc" {
		t.Errorf("ID = %q, want the stored ID — a patch never renames the record", got.ID)
	}
}

func TestCredentialWithPatch_PresentAndEmptyClears(t *testing.T) {
	stored := Credential{ID: "cred:x:1", Name: "x", Username: "u", Auth: AuthPublicKey, KeyPath: "/k"}

	empty := ""
	got := stored.WithPatch(CredentialPatch{KeyPath: &empty})

	if got.KeyPath != "" {
		t.Errorf("KeyPath = %q, want empty — a present-but-empty patch field clears", got.KeyPath)
	}
	if got.Username != "u" {
		t.Errorf("Username = %q, want u", got.Username)
	}
}
```

- [ ] **Step 2: Run it and verify it fails**

Run: `go test ./internal/profile/ -run TestCredentialWithPatch -v`
Expected: FAIL — `undefined: CredentialPatch`.

- [ ] **Step 3: Create `credential.go` with the moved type and the patch**

```go
package profile

import "strings"

// Credential is a reusable authentication identity. It holds identity only —
// never a host. Which endpoints a credential may be spent on is computed from
// the saved profiles that reference it — but NOT yet. Host and Port stay here
// through wave 1 because checkBinding (ssh_config.go:105) refuses a connection
// whose BoundHost is empty and the resolver fills it from this field. They are
// deleted in wave 2, in the same commit range that makes computed authorization
// live. This move is a file move, not a semantic change.
//
// Secrets live in the credential.SecretStore behind opaque references
// (ADR-0011 §2). SecretID and PassphraseSecretID are BACKEND-OWNED: they are
// stripped from every response and rejected on every request, so the renderer
// can neither read nor write them. That is why updates take a CredentialPatch
// rather than a whole record — a round trip through a renderer that was never
// shown these fields must not be able to blank them.
type Credential struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Username string   `json:"username"`
	Auth     AuthMode `json:"auth"`
	KeyPath  string   `json:"keyPath,omitempty"`

	// Host/Port: see the note above. Renderer-owned while they exist.
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`

	// SecretID is the opaque reference to the stored password.
	SecretID string `json:"secretId,omitempty"`
	// PassphraseSecretID is the opaque reference to the stored key passphrase.
	PassphraseSecretID string `json:"passphraseSecretId,omitempty"`
}

// CredentialPatch is a sparse update. A nil field means "not mentioned, leave
// it alone"; a non-nil field means "set it to this", including to the zero
// value. Presence is the signal, never emptiness — the same rule the group
// defaults merge needs in wave 2, established here on the smaller aggregate.
//
// There is deliberately no SecretID field: secret references move only through
// credentials.savePassword and its siblings, which mint their own IDs.
type CredentialPatch struct {
	Name     *string
	Username *string
	Auth     *AuthMode
	KeyPath  *string
	Host     *string // removed in wave 2 with the field
	Port     *int    // removed in wave 2 with the field
}

// WithPatch returns c with the patch applied. Fields the patch does not mention
// — and every backend-owned field, which the patch cannot mention — are carried
// over unchanged.
func (c Credential) WithPatch(p CredentialPatch) Credential {
	if p.Name != nil {
		c.Name = *p.Name
	}
	if p.Username != nil {
		c.Username = *p.Username
	}
	if p.Auth != nil {
		c.Auth = *p.Auth
	}
	if p.KeyPath != nil {
		c.KeyPath = *p.KeyPath
	}
	if p.Host != nil {
		c.Host = *p.Host
	}
	if p.Port != nil {
		c.Port = *p.Port
	}
	return c
}

// NewCredentialID generates a credential id: "cred:<slug>:<uuid>".
func NewCredentialID(name string) string {
	return "cred:" + slugify(name) + ":" + newUUID()
}

// Validate reports whether the credential may be stored. The host check is
// unchanged from profile.go and stays until wave 2 removes the field with the
// binding it feeds; the two identity checks are new, and they are what remains
// once the host check goes.
func (c Credential) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return ErrCredentialNameRequired
	}
	if strings.TrimSpace(c.Username) == "" {
		return ErrCredentialUsernameRequired
	}
	if strings.TrimSpace(c.Host) == "" {
		return ErrCredentialHostRequired
	}
	return nil
}
```

- [ ] **Step 4: Move the old block out of `profile.go`**

Remove lines 96-153 of `internal/profile/profile.go` — the duplicated `Credential` doc
comment, the struct, `NewCredentialID`, `ErrCredentialHostRequired` and the old `Validate`.
`ErrCredentialHostRequired` moves to `credential.go` **unchanged**; only its address
changes. Add the two new sentinels beside it:

```go
// ErrCredentialHostRequired is nocx-mon's policy, moved here verbatim. It goes
// away in wave 2 together with Credential.Host, when computed authorization
// replaces the binding it enforces — not before, because checkBinding refuses
// an empty BoundHost and the resolver has nothing else to fill it from.
var ErrCredentialHostRequired = errors.New("credential must be bound to a host")

// ErrCredentialNameRequired and ErrCredentialUsernameRequired are the identity
// completeness checks. They are additive: the host check above still runs.
var (
	ErrCredentialNameRequired     = errors.New("credential name is required")
	ErrCredentialUsernameRequired = errors.New("credential username is required")
)
```

- [ ] **Step 5: Run the package tests**

Run: `go test ./internal/profile/ -v`

Expected: the two new tests PASS **and every existing test still compiles and passes** —
this step is a move plus two additive checks, so a red suite here means something was
changed that should not have been. Confirm with
`git diff --stat internal/profile/` that `profile.go` only lost lines.

- [ ] **Step 6: Commit**

```bash
git add internal/profile/credential.go internal/profile/credential_test.go internal/profile/profile.go internal/profile/profile_test.go
git commit -m "refactor(profile): credential holds identity, not a host binding (nocx-52cd)"
```

---

## Task 3: The store tells create from update

**Files:**

- Modify: `internal/profile/store.go` (the `CredentialMetadataRepository` interface and the
  `SaveCredential` implementation, `store.go:26-31` and `:205-235`)
- Create: `internal/profile/store_test.go`

**Interfaces:**

- Produces, on `CredentialMetadataRepository`:
  - `CreateCredential(c Credential) error` — fails with `ErrCredentialExists` if the ID is
    taken, fails with `ErrCredentialIDRequired` if the ID is empty.
  - `UpdateCredential(id string, p CredentialPatch) (Credential, error)` — fails with
    `ErrCredentialNotFound` if the ID is absent; returns the merged record.
  - `LoadCredentials`, `DeleteCredential` unchanged.
  - `SaveCredential` is **removed** — it is the upsert that made create and update
    indistinguishable (`nocx-u5ai`).
- Consumes: `Credential.WithPatch` from Task 2.

**Acceptance Criteria:**

- `CreateCredential` with an ID that already exists returns `ErrCredentialExists` and does
  not modify the stored record.
- `CreateCredential` with an empty ID returns `ErrCredentialIDRequired`.
- `UpdateCredential` on a missing ID returns `ErrCredentialNotFound` and creates nothing.
- `UpdateCredential` performs the merge **under the store mutex**, so a concurrent password
  write cannot be lost between the read and the write.

- [ ] **Step 1: Write the failing tests**

Create `internal/profile/store_test.go`:

```go
package profile

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *JSONStore {
	t.Helper()
	return NewJSONStore(filepath.Join(t.TempDir(), "p.json"))
}

func TestCreateCredential_RejectsDuplicateID(t *testing.T) {
	s := newTestStore(t)
	c := Credential{ID: "cred:a:1", Name: "a", Username: "u", Auth: AuthPassword, SecretID: "sec:1"}
	if err := s.CreateCredential(c); err != nil {
		t.Fatalf("first create: %v", err)
	}

	dup := Credential{ID: "cred:a:1", Name: "impostor", Username: "u2", Auth: AuthAgent}
	if err := s.CreateCredential(dup); !errors.Is(err, ErrCredentialExists) {
		t.Fatalf("second create err = %v, want ErrCredentialExists", err)
	}

	got, err := s.LoadCredentials()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" || got[0].SecretID != "sec:1" {
		t.Fatalf("a refused create must not modify the stored record, got %+v", got)
	}
}

func TestCreateCredential_RejectsEmptyID(t *testing.T) {
	s := newTestStore(t)
	err := s.CreateCredential(Credential{Name: "a", Username: "u"})
	if !errors.Is(err, ErrCredentialIDRequired) {
		t.Fatalf("err = %v, want ErrCredentialIDRequired", err)
	}
}

func TestUpdateCredential_RejectsMissingID(t *testing.T) {
	s := newTestStore(t)
	_, err := s.UpdateCredential("cred:nope:1", CredentialPatch{})
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("err = %v, want ErrCredentialNotFound", err)
	}
	got, _ := s.LoadCredentials()
	if len(got) != 0 {
		t.Fatalf("a refused update must create nothing, got %d", len(got))
	}
}

func TestUpdateCredential_MergesAndKeepsSecretID(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateCredential(Credential{
		ID: "cred:a:1", Name: "a", Username: "u", Auth: AuthPassword, SecretID: "sec:1",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	name := "renamed"
	got, err := s.UpdateCredential("cred:a:1", CredentialPatch{Name: &name})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Name != "renamed" {
		t.Errorf("Name = %q, want renamed", got.Name)
	}
	if got.SecretID != "sec:1" {
		t.Errorf("SecretID = %q, want sec:1", got.SecretID)
	}
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/profile/ -run "Credential" -v`
Expected: FAIL — `s.CreateCredential undefined`.

- [ ] **Step 3: Replace `SaveCredential` in `store.go`**

```go
// ErrCredentialIDRequired, ErrCredentialExists and ErrCredentialNotFound make
// create and update distinguishable. The single SaveCredential upsert they
// replace accepted an empty ID and silently overwrote an existing record, so a
// create could destroy data it never read (nocx-u5ai).
var (
	ErrCredentialIDRequired = errors.New("credential ID is required")
	ErrCredentialExists     = errors.New("credential already exists")
	ErrCredentialNotFound   = errors.New("credential not found")
)

// CreateCredential stores a new credential. It refuses an empty ID and refuses
// to overwrite an existing one.
func (s *JSONStore) CreateCredential(c Credential) error {
	if c.ID == "" {
		return ErrCredentialIDRequired
	}
	if err := c.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	for _, existing := range d.Credentials {
		if existing.ID == c.ID {
			return fmt.Errorf("%s: %w", c.ID, ErrCredentialExists)
		}
	}
	d.Credentials = append(d.Credentials, c)
	return s.writeLocked(d)
}

// UpdateCredential merges a sparse patch onto the stored record and returns the
// result. The read-merge-write runs under the mutex: doing it in the caller
// would let a concurrent savePassword land between the read and the write and
// be silently discarded.
func (s *JSONStore) UpdateCredential(id string, p CredentialPatch) (Credential, error) {
	if id == "" {
		return Credential{}, ErrCredentialIDRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return Credential{}, err
	}
	for i, existing := range d.Credentials {
		if existing.ID == id {
			merged := existing.WithPatch(p)
			if err := merged.Validate(); err != nil {
				return Credential{}, err
			}
			d.Credentials[i] = merged
			if err := s.writeLocked(d); err != nil {
				return Credential{}, err
			}
			return merged, nil
		}
	}
	return Credential{}, fmt.Errorf("%s: %w", id, ErrCredentialNotFound)
}
```

Update the `CredentialMetadataRepository` interface at `store.go:26-31` to match, and delete
the old `SaveCredential` method.

- [ ] **Step 4: Fix the internal callers of `SaveCredential`**

`ws.go` calls it from `savePasswordForCredential`, `deletePasswordForCredential`,
`savePassphraseForCredential` and `deletePassphraseForCredential`. Those write a
**backend-owned** field, which `CredentialPatch` deliberately cannot express. Add a narrow
repository method for exactly that, rather than widening the patch:

```go
// SetSecretRefs repoints a credential's backend-owned secret references. It is
// the only way those fields ever change, and it is deliberately not reachable
// through CredentialPatch — the renderer must never name a SecretID.
func (s *JSONStore) SetSecretRefs(id string, secretID, passphraseSecretID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, err := s.load()
	if err != nil {
		return err
	}
	for i, existing := range d.Credentials {
		if existing.ID == id {
			d.Credentials[i].SecretID = secretID
			d.Credentials[i].PassphraseSecretID = passphraseSecretID
			return s.writeLocked(d)
		}
	}
	return fmt.Errorf("%s: %w", id, ErrCredentialNotFound)
}
```

Rewrite the four `ws.go` helpers to load, mint the new secret, then call `SetSecretRefs`
with both fields — keeping the existing write-before-repoint ordering, which is already
correct and documented at `ws.go:1233-1240`.

- [ ] **Step 5: Run the package and transport tests**

Run: `go test ./internal/profile/ ./internal/transport/ -race`
Expected: the new store tests PASS. Task 1's RPC regression still FAILS — the handler has
not been changed yet.

- [ ] **Step 6: Commit**

```bash
git add internal/profile/store.go internal/profile/store_test.go internal/transport/ws.go
git commit -m "feat(profile): create and update a credential are different operations (nocx-u5ai)"
```

---

## Task 4: The handler applies a patch and mints the ID

**Files:**

- Modify: `internal/transport/ws.go` — `handleCredentialCRUDMethod` (`ws.go:1089-1149`) and
  the method dispatch at `ws.go:564`

**Interfaces:**

- Consumes: `profile.CredentialPatch`, `CreateCredential`, `UpdateCredential`,
  `NewCredentialID`.
- Produces: `credentials.create` returns the stored record with secret references blanked;
  `credentials.update` the same.

**Acceptance Criteria:**

- Task 1's regression test PASSES.
- `credentials.create` **ignores** any renderer-supplied `id` and mints its own with
  `NewCredentialID`, so the "typed `p` first, stuck with `cred:p:…`" defect (§2.4) is gone.
- `credentials.update` requires an `id` and returns `-32602` without one.
- `secretId` / `passphraseSecretId` in either request are still rejected with `-32602`.
- Neither response carries a secret reference.

- [ ] **Step 1: Write the additional failing tests**

```go
func TestCredentialsRPC_CreateMintsItsOwnID(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialMetadataRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "credentials.create", map[string]any{
		"id":       "cred:p:whatever-the-renderer-guessed",
		"name":     "prod-ops",
		"username": "ops",
		"auth":     "password",
	})
	var out struct {
		Result profile.Credential `json:"result"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Result.ID == "cred:p:whatever-the-renderer-guessed" {
		t.Error("create used the renderer's ID; the backend must mint its own")
	}
	if !strings.HasPrefix(out.Result.ID, "cred:prod-ops:") {
		t.Errorf("ID = %q, want a cred:prod-ops: prefix from the final name", out.Result.ID)
	}
	if out.Result.SecretID != "" || out.Result.PassphraseSecretID != "" {
		t.Error("response leaked a secret reference")
	}
}

func TestCredentialsRPC_UpdateRequiresID(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialMetadataRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "credentials.update", map[string]any{"name": "x"})
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32602 {
		t.Fatalf("want -32602 Invalid params, got %+v", out.Error)
	}
}
```

- [ ] **Step 2: Run and verify all three fail**

Run: `go test ./internal/transport/ -run TestCredentialsRPC -v`
Expected: FAIL on all three.

- [ ] **Step 3: Split the handler**

Replace the `case "credentials.create", "credentials.update":` block in
`handleCredentialCRUDMethod` with two cases:

```go
case "credentials.create":
	var in credentialCreateDTO
	if err := json.Unmarshal(req.Params, &in); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}
	if in.SecretID != "" || in.PassphraseSecretID != "" {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602,
			"secretId/passphraseSecretId are backend-owned"))
		return
	}
	// The renderer's id is ignored, not honoured: it was minted from the first
	// keystroke of the name and never revised (spec §2.4).
	c := profile.Credential{
		ID:       profile.NewCredentialID(in.Name),
		Name:     in.Name,
		Username: in.Username,
		Auth:     in.Auth,
		KeyPath:  in.KeyPath,
	}
	if err := s.credMeta.CreateCredential(c); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, credentialErrorCode(err), err.Error()))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(c)))

case "credentials.update":
	var in credentialUpdateDTO
	if err := json.Unmarshal(req.Params, &in); err != nil || in.ID == "" {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: id required"))
		return
	}
	if in.SecretID != "" || in.PassphraseSecretID != "" {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602,
			"secretId/passphraseSecretId are backend-owned"))
		return
	}
	merged, err := s.credMeta.UpdateCredential(in.ID, in.Patch())
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, credentialErrorCode(err), err.Error()))
		return
	}
	merged.SecretID = ""
	merged.PassphraseSecretID = ""
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(merged)))
```

Add the DTOs and the error mapper near the handler:

```go
// credentialCreateDTO is the renderer's create payload. SecretID and
// PassphraseSecretID appear here only so a renderer that sends them can be
// REJECTED rather than silently ignored — they are never read into the record.
type credentialCreateDTO struct {
	Name               string           `json:"name"`
	Username           string           `json:"username"`
	Auth               profile.AuthMode `json:"auth"`
	KeyPath            string           `json:"keyPath"`
	SecretID           string           `json:"secretId"`
	PassphraseSecretID string           `json:"passphraseSecretId"`
}

// credentialUpdateDTO is sparse: a field the renderer did not send stays nil
// and the stored value survives. That is the whole fix for the orphaned-secret
// defect — the previous handler decoded into the record type, so an absent
// field arrived as a zero value indistinguishable from a deliberate clear.
type credentialUpdateDTO struct {
	ID                 string            `json:"id"`
	Name               *string           `json:"name"`
	Username           *string           `json:"username"`
	Auth               *profile.AuthMode `json:"auth"`
	KeyPath            *string           `json:"keyPath"`
	SecretID           string            `json:"secretId"`
	PassphraseSecretID string            `json:"passphraseSecretId"`
}

func (d credentialUpdateDTO) Patch() profile.CredentialPatch {
	return profile.CredentialPatch{
		Name: d.Name, Username: d.Username, Auth: d.Auth, KeyPath: d.KeyPath,
	}
}

// credentialErrorCode maps a store error to a JSON-RPC code. A caller mistake
// is -32602 so the renderer can name the field to fix; anything else is -32603
// (nocx-wd2m established this distinction for the host binding).
func credentialErrorCode(err error) int {
	switch {
	case errors.Is(err, profile.ErrCredentialExists),
		errors.Is(err, profile.ErrCredentialNotFound),
		errors.Is(err, profile.ErrCredentialIDRequired),
		errors.Is(err, profile.ErrCredentialNameRequired),
		errors.Is(err, profile.ErrCredentialUsernameRequired):
		return -32602
	default:
		return -32603
	}
}
```

Update the dispatch at `ws.go:564` so `credentials.create` and `credentials.update` still
route to `handleCredentialCRUDMethod` (the case list already names both).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/transport/ -run TestCredentialsRPC -race -v`
Expected: all PASS, including Task 1's regression.

- [ ] **Step 5: Commit**

```bash
git add internal/transport/ws.go internal/transport/ws_profiles_test.go
git commit -m "fix(transport): a credential update patches the record instead of replacing it (nocx-52cd)"
```

---

## Task 5: Profiles and groups get the same treatment

**Files:**

- Modify: `internal/transport/ws.go` — `handleProfileMethod` (`ws.go:1009-1047`),
  `handleGroupMethod` (`ws.go:1049-1087`)
- Modify: `internal/profile/store.go` — `CreateProfile`/`UpdateProfile`,
  `CreateGroup`/`UpdateGroup`, removing both `Save*` upserts
- Modify: `internal/transport/ws_profiles_test.go`

**Interfaces:**

- Produces: `CreateProfile(p SSHProfile) error`, `UpdateProfile(p SSHProfile) error`,
  `CreateGroup(g ProfileGroup) error`, `UpdateGroup(g ProfileGroup) error`, each with the
  same `Exists`/`NotFound`/`IDRequired` sentinels as Task 3.

**Acceptance Criteria:**

- `profiles.create` with an existing ID returns `-32602` and changes nothing;
  `profiles.update` with a missing ID returns `-32602` and creates nothing. Same for groups.
- `profiles.create` mints its own ID via `NewProfileID` when the renderer sends none.
- `nocx-u5ai` is fully satisfied and can be closed.

> **Why profiles keep a whole-record update while credentials took a patch:** a profile has
> no backend-owned field the renderer cannot see, so a whole-record update loses nothing.
> That changes in wave 2, when provenance arrives and an inherited value must not be written
> back as an explicit one — at which point profiles get a patch too. Do not pre-build it here.

- [ ] **Step 1: Write the failing tests**

```go
func TestProfilesRPC_CreateRejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialMetadataRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	id := profile.NewProfileID("ssh", "web-1")
	first := map[string]any{
		"id": id, "type": "ssh", "name": "web-1",
		"options": map[string]any{"host": "10.0.0.1", "port": 22, "user": "ops"},
	}
	jsonrpcCall(t, conn, "profiles.create", first)

	second := map[string]any{
		"id": id, "type": "ssh", "name": "impostor",
		"options": map[string]any{"host": "evil.example", "port": 22, "user": "root"},
	}
	resp := jsonrpcCall(t, conn, "profiles.create", second)
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32602 {
		t.Fatalf("want -32602 for a duplicate create, got %+v", out.Error)
	}

	stored, err := ps.LoadProfiles()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(stored) != 1 || stored[0].Options.Host != "10.0.0.1" {
		t.Fatalf("a refused create overwrote the record: %+v", stored)
	}
}
```

Write the mirror tests `TestProfilesRPC_UpdateRejectsMissingID`,
`TestGroupsRPC_CreateRejectsDuplicateID` and `TestGroupsRPC_UpdateRejectsMissingID` the same
way, changing only the method names and the payload shape (a group payload is
`{"id": …, "name": …, "parentGroupId": …}`).

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/transport/ -run "TestProfilesRPC_Create|TestProfilesRPC_Update|TestGroupsRPC_" -v`
Expected: FAIL — the upsert accepts both calls.

- [ ] **Step 3: Add the store methods**

Mirror Task 3's `CreateCredential`/`UpdateCredential` for profiles and groups, with
`ErrProfileIDRequired`/`ErrProfileExists`/`ErrProfileNotFound` and the group equivalents.
`UpdateProfile` takes a whole `SSHProfile` (see the note above) and replaces the record at
its ID, failing if absent. Delete `SaveProfile` and `SaveGroup`.

- [ ] **Step 4: Fix the remaining `SaveProfile` callers**

`grep -rn "SaveProfile\|SaveGroup" --include=*.go . | grep -v _test` finds
`internal/importer/tabby.go` and `internal/export/import.go`. Both are alternate writers
(spec §2.8) and wave 3 routes them through the domain service properly. **For this wave**,
point them at `CreateProfile` and, on `ErrProfileExists`, `UpdateProfile` — preserving
today's overwrite-on-reimport behaviour explicitly rather than by accident. Leave a comment
naming wave 3 so the next reader knows this is a way station, and file the bead before
writing the comment (AGENTS.md: a TODO in source is not a task).

- [ ] **Step 5: Split the handlers**

In `handleProfileMethod`, replace `case "profiles.create", "profiles.update":` with two
cases following Task 4's shape: create mints an ID when absent and calls `CreateProfile`;
update requires an ID and calls `UpdateProfile`. Same for `handleGroupMethod`.

- [ ] **Step 6: Run the full suite**

Run: `go test ./... -race`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/profile/store.go internal/transport/ws.go internal/transport/ws_profiles_test.go internal/importer/tabby.go internal/export/import.go
git commit -m "fix(profile): profile and group create no longer silently overwrite (nocx-u5ai)"
```

---

## Task 6: Credential secret versions

**Files:**

- Modify: `internal/profile/credential.go`
- Modify: `internal/profile/credential_test.go`
- Modify: `internal/profile/store.go` (`SetSecretRefs` becomes version-aware)
- Modify: `internal/transport/ws.go` (the four secret helpers)

**Interfaces:**

- Produces:
  - `type CredentialVersion struct { ID string; PasswordSecretID string; PassphraseSecretID string; KeyFingerprint string }`
  - `Credential.Versions []CredentialVersion`, `Credential.CurrentVersionID string`,
    `Credential.CandidateVersionID string`
  - `func (c Credential) Current() (CredentialVersion, bool)`
  - `func (c Credential) Version(id string) (CredentialVersion, bool)`
- Consumes: `credential.NewSecretID` for minting.

**Acceptance Criteria:**

- A credential with no versions and a legacy `SecretID` is read as a single `current`
  version — existing stores load without migration or data loss.
- `savePassword` creates a **new version** and points `current` at it, rather than
  overwriting the previous version's reference; the previous version remains addressable.
- The version shape is auth-method-specific per spec §3.9: a password version carries
  `PasswordSecretID`; a public-key version carries `KeyFingerprint` plus an optional
  `PassphraseSecretID`; an agent version carries neither. A version that carries fields for
  a method it is not for is a validation error.
- No response exposes a version's secret references.

> **Decide first, then code:** spec §9 item 2 lists the auth-specific version schema as a
> plan input. This task is where it is decided. Write the chosen shape into the spec's §3.9
> before writing the test, so the document and the code agree.

- [ ] **Step 1: Write the failing tests**

```go
func TestCredentialCurrent_LegacyRecordReadsAsOneVersion(t *testing.T) {
	// A store written before versions existed: SecretID on the record, no list.
	c := Credential{ID: "cred:a:1", Name: "a", Username: "u", Auth: AuthPassword, SecretID: "sec:1"}

	v, ok := c.Current()
	if !ok {
		t.Fatal("a legacy credential must read as one current version")
	}
	if v.PasswordSecretID != "sec:1" {
		t.Errorf("PasswordSecretID = %q, want sec:1", v.PasswordSecretID)
	}
}

func TestCredentialVersions_CurrentSelectsByID(t *testing.T) {
	c := Credential{
		ID: "cred:a:1", Name: "a", Username: "u", Auth: AuthPassword,
		Versions: []CredentialVersion{
			{ID: "v7", PasswordSecretID: "sec:7"},
			{ID: "v8", PasswordSecretID: "sec:8"},
		},
		CurrentVersionID:   "v7",
		CandidateVersionID: "v8",
	}

	v, ok := c.Current()
	if !ok || v.ID != "v7" {
		t.Fatalf("Current() = %+v, %v; want v7", v, ok)
	}
	if got, ok := c.Version("v8"); !ok || got.PasswordSecretID != "sec:8" {
		t.Fatalf("Version(v8) = %+v, %v", got, ok)
	}
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./internal/profile/ -run TestCredentialVersions -v`
Expected: FAIL — `undefined: CredentialVersion`.

- [ ] **Step 3: Implement the version model**

Add to `credential.go` the `CredentialVersion` type, the three new `Credential` fields, and:

```go
// Current returns the version a normal connection uses. A record written before
// versions existed has no list and a bare SecretID; it reads as a single
// current version, so an existing store loads with no migration step and no
// window in which a password is unreachable.
func (c Credential) Current() (CredentialVersion, bool) {
	if len(c.Versions) == 0 {
		if c.SecretID == "" && c.PassphraseSecretID == "" {
			return CredentialVersion{}, false
		}
		return CredentialVersion{
			ID:                 legacyVersionID,
			PasswordSecretID:   c.SecretID,
			PassphraseSecretID: c.PassphraseSecretID,
		}, true
	}
	return c.Version(c.CurrentVersionID)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/profile/ -race -v`
Expected: PASS.

- [ ] **Step 5: Point the secret helpers at versions**

Rewrite `savePasswordForCredential` to append a new `CredentialVersion` and move
`CurrentVersionID` to it, keeping the existing write-before-repoint ordering. The old
version stays in the list; wave 8 decides when it is retired.

- [ ] **Step 6: Run the full suite and commit**

```bash
go test ./... -race
git add internal/profile/ internal/transport/ws.go .internal/specs/2026-07-29-connection-manager-design.md
git commit -m "feat(profile): a credential carries secret versions (nocx-52cd)"
```

---

## Task 7: The resolver selects the current version

**Files:**

- Modify: `internal/connection/resolver.go` (`buildConfig`, lines 76-96)
- Modify: `internal/connection/resolver_test.go`

**Interfaces:**

- Consumes: `Credential.Current()` from Task 6.
- Produces: `cfg.SecretID` and `cfg.PassphraseSecretID` carry the **selected version's**
  references.

**Acceptance Criteria:**

- A profile referencing a credential with `current = v7` resolves to v7's
  `PasswordSecretID`.
- Because `poolKeyFor` (`ssh_dial.go:38`) keys on `cfg.SecretID`, moving `current` to a new
  version changes the pool key with no change to `internal/ssh`. A test asserts the
  resolved `SecretID` differs across two versions — that is the property the pool relies on,
  and it is asserted here rather than assumed.
- `cfg.BoundHost` / `cfg.BoundPort` keep being set from `cred.Host` / `cred.Port`, exactly
  as today (`resolver.go:85-86`). Wave 1 does not touch the binding: `Credential.Host`
  survives until wave 2 replaces `checkBinding` with computed authorization (spec §3.1).
  **Do not "tidy" these two lines away** — removing them here breaks every stored password
  at connect time, and that is precisely the flaw writing this plan found in the spec's
  first wave boundary.

- [ ] **Step 1: Read the existing fixtures, then write the failing test**

Read `internal/connection/resolver_test.go` first and reuse its store fakes and constructor
calls verbatim. Do **not** invent helper names — a memory from a previous session
(`bd memories`, "never write test code that calls a helper you have not read in that exact
test file") records what that costs.

The test asserts the property the pool depends on, rather than assuming it:

```go
// TestResolve_UsesCurrentVersionSecret pins the contract between the credential
// version model and the connection pool. poolKeyFor (ssh_dial.go:38) keys on
// cfg.SecretID, so publishing the SELECTED version's reference is what makes
// moving `current` produce a different pool key — with no change anywhere in
// internal/ssh. Asserting it here is what stops a later refactor from quietly
// publishing the record-level SecretID again and re-pooling two versions
// together.
func TestResolve_UsesCurrentVersionSecret(t *testing.T) {
	cred := profile.Credential{
		ID: "cred:ops:1", Name: "ops", Username: "ops", Auth: profile.AuthPassword,
		Host: "10.0.0.1", // still required in wave 1 — see the note above
		Versions: []profile.CredentialVersion{
			{ID: "v7", PasswordSecretID: "sec:7"},
			{ID: "v8", PasswordSecretID: "sec:8"},
		},
		CurrentVersionID: "v7",
	}
	prof := profile.SSHProfile{
		Base:    profile.Base{ID: "ssh:custom:web-1:1", Type: "ssh", Name: "web-1"},
		Options: profile.SSHProfileOptions{Host: "10.0.0.1", Port: 22, CredentialID: cred.ID},
	}

	// <construct the resolver over fakes holding prof and cred — copy the
	//  construction from the neighbouring test in this file>

	_, cfg, err := r.Resolve(prof.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(cfg.SecretID) != "sec:7" {
		t.Fatalf("SecretID = %q, want sec:7 (the current version)", cfg.SecretID)
	}
	first := cfg.SecretID

	// Move current to v8 and resolve again.
	cred.CurrentVersionID = "v8"
	// <write cred back through the same fake>

	_, cfg2, err := r.Resolve(prof.ID)
	if err != nil {
		t.Fatalf("Resolve after promotion: %v", err)
	}
	if string(cfg2.SecretID) != "sec:8" {
		t.Fatalf("SecretID = %q, want sec:8", cfg2.SecretID)
	}
	if cfg2.SecretID == first {
		t.Fatal("promoting a version left the SecretID unchanged; the pool would reuse the old transport")
	}
}
```

The two `<…>` lines are the only places to fill in, and they are fixture construction copied
from the neighbouring test — not design decisions.

- [ ] **Step 2: Run it and verify it fails**

Run: `go test ./internal/connection/ -run TestResolve_UsesCurrentVersionSecret -v`
Expected: FAIL — `cfg.SecretID` is the record-level `SecretID` (empty here, since this
credential has versions and no legacy reference), not `sec:7`.

- [ ] **Step 3: Select the current version in `buildConfig`**

In `internal/connection/resolver.go`, replace the two secret-reference assignments
(`resolver.go:91-96`) with:

```go
	// The SELECTED version's references, not the record's. poolKeyFor keys on
	// cfg.SecretID, so this is also what makes a promotion produce a new pool
	// entry without any change in internal/ssh.
	if v, ok := cred.Current(); ok {
		if v.PasswordSecretID != "" {
			cfg.SecretID = credential.SecretID(v.PasswordSecretID)
		}
		if v.PassphraseSecretID != "" {
			cfg.PassphraseSecretID = credential.SecretID(v.PassphraseSecretID)
		}
	}
```

Leave `cfg.BoundHost = cred.Host` and `cfg.BoundPort = cred.Port` exactly where they are.

- [ ] **Step 4: Run the test and the suite**

Run: `go test ./internal/connection/ ./internal/ssh/ ./internal/transport/ -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connection/resolver.go internal/connection/resolver_test.go
git commit -m "feat(connection): resolve the credential's current version, not its record (nocx-52cd)"
```

---

## Self-Review

**1. Spec coverage.** §2.1 → Tasks 1-4. §2.4 → Task 4. `nocx-u5ai` (§2.11) → Tasks 3 and 5.
§3.9 → Tasks 6 and 7. §9 item 2 (auth-specific version schema) → Task 6, decided there.
**Not covered, and deliberately deferred to their stated waves:** §2.2, §2.3, §2.5, §2.6,
§2.7, §2.8, §2.9, §2.10. §3.1's deletion of `Credential.Host` is deferred to **wave 2** by
the owner's decision of 2026-07-29 — the spec now says so in §3.1 and §7, so this is a
recorded boundary rather than an omission.

**2. Placeholders.** None. Task 7's two `<…>` markers name fixture construction to copy from
an adjacent test, which the plan deliberately does not transcribe because a stale copy of a
fixture is worse than a pointer to the live one.

**3. Type consistency.** `CredentialPatch` (Task 2) is consumed by `UpdateCredential`
(Task 3) and produced by `credentialUpdateDTO.Patch()` (Task 4) — same field names and
pointer types throughout. `CredentialVersion.PasswordSecretID` (Task 6) is read by
`Current()` and consumed by the resolver (Task 7) under that name; the record's legacy field
stays `SecretID` and the two are deliberately different names so a confusion is a compile
error.
