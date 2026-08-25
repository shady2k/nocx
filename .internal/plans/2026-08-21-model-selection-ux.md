# Model Selection UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A person installs an endpoint, chooses a model once, and asks a question — never having to discover the Roles page unaided, and never being told "Ready" by an assistant that cannot answer.

**Architecture:** `profile.ResolveRole` stays the one resolver and gains a default as a new _input_, not a second path. `agent.status` stops reporting endpoint existence as readiness and reports whether the `answering` role resolves, using the refusal vocabulary `ResolveRole` already returns. The renderer turns that into a one-rung-at-a-time ladder, shown both in the readiness line and in a chip in the editor's chrome row.

**Tech Stack:** Go (`internal/profile`, `internal/capability`, `internal/transport`), TypeScript + SolidJS (`frontend/src`), JSON Schema contracts, vitest, `cmd/devharness` for e2e.

**Spec:** `.internal/specs/2026-08-21-model-selection-ux-design.md`
**Brainstorming bead:** nocx-rikz5

## Global Constraints

- **One resolver.** `profile.ResolveRole` (`internal/profile/role.go:140`) is the only place a role becomes an (endpoint, model) pair (AD-8). No task adds a second resolution path, and no task lets a model client choose a model (ADR-0028).
- **No silent fallback.** `nocx-e6kn2`'s criterion is binding: a role that cannot resolve is a visible failure, never a quiet hop to another model. The default is legal only because the person authored it.
- **The default is user-authored.** Never "the first model of the first endpoint". A default that the product invented is the forbidden fallback.
- **Contracts.** Any changed result shape carries `additionalProperties: false` plus an explicit `required`, is regenerated with `npm run contracts:check`, and is asserted BOTH as a DTO and over the real socket.
- **The kit.** New UI joins the row it lives in. The editor chrome row is `.nocx-chip` (`frontend/src/style.css:204`); do not introduce `ui-badge` there.
- **Gates per task:** `go test ./internal/<pkg>/...` for Go tasks, `npx vitest run <file>` plus `npx tsc --noEmit` and `npx eslint` for frontend tasks. The full `make ci-full` belongs to whoever integrates, not to each task.
- **Copy is exact.** The ladder's sentences are written verbatim in Task 4 and reused unchanged in Tasks 5 and 6.

---

## File Structure

| File                                 | Responsibility                                           |
| ------------------------------------ | -------------------------------------------------------- |
| `internal/profile/role.go`           | `ResolveRole` + the default's type and validation        |
| `internal/profile/store.go`          | persistence of the default beside `Roles`                |
| `internal/capability/config.go`      | the service seam: load/save the default                  |
| `internal/transport/ws_roles.go`     | `roles.list` / `roles.setDefault` wire                   |
| `internal/transport/ws_assistant.go` | `agent.status` grows the role's resolution               |
| `contracts/agent.status.schema.json` | the readiness contract                                   |
| `contracts/roles.list.schema.json`   | the default on the wire                                  |
| `frontend/src/agent-status-line.ts`  | the ladder: one rung → one sentence → one target         |
| `frontend/src/roles-section.tsx`     | the default control, "As default", the green line's rule |
| `frontend/src/editor.ts`             | the model chip in `chromeLeft`                           |
| `frontend/src/terminal-content.ts`   | feeds the chip from status + active target               |
| `e2e/assistant-readiness.spec.ts`    | the end-to-end check                                     |

---

### Task 1: `ResolveRole` learns the default

**Files:**

- Modify: `internal/profile/role.go` (add `DefaultModel`, extend `ResolveRole`)
- Modify: `internal/profile/store.go:73-81` (persist it), `internal/profile/store.go:511`
- Test: `internal/profile/role_test.go`

**Interfaces:**

- Consumes: nothing (first task).
- Produces:
  - `type DefaultModel struct { EndpointID string \`json:"endpointId"\`; Model string \`json:"model"\` }`
  - `func (d DefaultModel) IsSet() bool`
  - `func ResolveRole(role ModelRole, assignments []RoleAssignment, def DefaultModel, endpoints []Endpoint) (Endpoint, string, error)` — **signature change: `def` is the new third parameter.**
  - `RoleRepository` grows `LoadDefaultModel() (DefaultModel, error)` and `SetDefaultModel(DefaultModel) error`.

**Acceptance Criteria:**

- A role with its own assignment resolves to that assignment even when a default exists.
- A role with no assignment resolves to the default.
- With neither, `ResolveRole` returns `ErrRoleUnassigned`, unchanged.
- A default naming a **removed model** returns `ErrRoleModelGone` — the default is never silently repaired into a neighbouring model.
- **Deleting an endpoint CLEARS a default that names it, in the same write** (spec §6's interval: the default exists until it is overwritten or its endpoint is deleted, and must never point at nothing). After the delete, an unassigned role reports `ErrRoleUnassigned` — the ladder's _Choose a model_ — not `ErrRoleEndpointGone`.
- **A per-role assignment naming a deleted endpoint is left dangling**, and still reports `ErrRoleEndpointGone`. That asymmetry is deliberate and is existing, tested behaviour (`internal/profile/role.go:66`, `internal/profile/role_test.go:199`): an assignment is a statement about one role that the person made and must be told about, while the default is a single global convenience with nothing to reassign.
- `SetDefaultModel` with an empty pair clears the default; a half-set pair is refused.
- A default whose endpoint is deleted **by another process between load and resolve** still refuses with `ErrRoleEndpointGone` rather than resolving — the clear-on-delete is the tidy path, not the safety net.

- [ ] **Step 1: Write the failing tests**

```go
// internal/profile/role_test.go — package profile (NOT profile_test): the
// existing file is in-package, so names are unqualified. Adding a `profile.`
// qualifier here does not compile, because a package cannot import itself.
func TestResolveRole_FallsBackToTheDefault(t *testing.T) {
	eps := []Endpoint{{ID: "e1", Name: "openrouter", Models: []EndpointModel{{Name: "m-a"}, {Name: "m-b"}}}}
	def := DefaultModel{EndpointID: "e1", Model: "m-a"}

	// No assignment at all: the default answers.
	ep, model, err := ResolveRole(RoleAnswering, nil, def, eps)
	if err != nil {
		t.Fatalf("resolve with only a default: %v", err)
	}
	if ep.ID != "e1" || model != "m-a" {
		t.Fatalf("resolved to %q/%q, want e1/m-a", ep.ID, model)
	}

	// An explicit assignment OUTRANKS the default — the override is the point.
	as := []RoleAssignment{{Role: RoleAnswering, EndpointID: "e1", Model: "m-b"}}
	_, model, err = ResolveRole(RoleAnswering, as, def, eps)
	if err != nil {
		t.Fatalf("resolve with an assignment: %v", err)
	}
	if model != "m-b" {
		t.Fatalf("resolved to %q, want the role's own m-b", model)
	}
}

func TestResolveRole_NoDefaultAndNoAssignmentStaysUnassigned(t *testing.T) {
	eps := []Endpoint{{ID: "e1", Name: "openrouter", Models: []EndpointModel{{Name: "m-a"}}}}
	_, _, err := ResolveRole(RoleAnswering, nil, DefaultModel{}, eps)
	if !errors.Is(err, ErrRoleUnassigned) {
		t.Fatalf("err = %v, want ErrRoleUnassigned", err)
	}
}

func TestResolveRole_ADefaultPointingAtNothingRefusesRatherThanRepairs(t *testing.T) {
	eps := []Endpoint{{ID: "e1", Name: "openrouter", Models: []EndpointModel{{Name: "m-a"}}}}

	// A default naming an endpoint that is not there refuses. This is the
	// RACE path, not the ordinary one: DeleteEndpoint clears the default in
	// the same write (Step 4b), so in the ordinary case there is no default
	// left to dangle. Kept because clearing is the tidy path, never the
	// safety net — another process may delete between load and resolve.
	gone := DefaultModel{EndpointID: "deleted", Model: "m-a"}
	if _, _, err := ResolveRole(RoleAnswering, nil, gone, eps); !errors.Is(err, ErrRoleEndpointGone) {
		t.Fatalf("deleted endpoint: err = %v, want ErrRoleEndpointGone", err)
	}

	stale := DefaultModel{EndpointID: "e1", Model: "m-removed"}
	if _, _, err := ResolveRole(RoleAnswering, nil, stale, eps); !errors.Is(err, ErrRoleModelGone) {
		t.Fatalf("removed model: err = %v, want ErrRoleModelGone", err)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/profile/ -run TestResolveRole -v`
Expected: FAIL — `too many arguments in call to profile.ResolveRole` and `undefined: profile.DefaultModel`.

- [ ] **Step 3: Add the type and extend the resolver**

```go
// internal/profile/role.go

// DefaultModel is the ONE (endpoint, model) pair a person names once, used by
// every role that has no assignment of its own (spec §2). It is not a
// fallback the product invents — that is what nocx-e6kn2 forbids, because
// then nobody can say which model answered. It is a choice the person made,
// reused; the distinction is the whole reason this is a stored value rather
// than "the first model of the first endpoint".
//
// The zero value means "no default", and it is the state a fresh profile is
// in. Both fields set or neither: a half-set default names nothing.
type DefaultModel struct {
	EndpointID string `json:"endpointId"`
	Model      string `json:"model"`
}

// IsSet reports whether a default has been chosen. Both fields are required
// together, so a half-set value is not "partly set" — it is not set.
func (d DefaultModel) IsSet() bool { return d.EndpointID != "" && d.Model != "" }

// ValidateDefaultModel checks the SHAPE before storing: both present, or both
// empty. The empty pair is the CLEAR write — it removes the default and
// returns every unassigned role to the visible failure state.
func ValidateDefaultModel(d DefaultModel) error {
	if (d.EndpointID == "") != (d.Model == "") {
		return fmt.Errorf("a default names an endpoint and a model together, or neither")
	}
	return nil
}
```

Then change the resolver's lookup — the ONLY behavioural edit, everything
below the lookup is untouched:

```go
func ResolveRole(role ModelRole, assignments []RoleAssignment, def DefaultModel, endpoints []Endpoint) (Endpoint, string, error) {
	if !ValidModelRole(role) {
		return Endpoint{}, "", fmt.Errorf("role %q: %w", role, ErrRoleUnknown)
	}
	// The role's own assignment first: an override is only an override if it
	// outranks the thing it overrides.
	var a *RoleAssignment
	for i := range assignments {
		if assignments[i].Role == role {
			a = &assignments[i]
			break
		}
	}
	// Then the default, as an assignment this role did not write. Below this
	// point the two are indistinguishable ON PURPOSE: an endpoint that is gone
	// is gone whichever named it, and a default is never repaired into a
	// neighbour any more than an assignment is.
	if a == nil && def.IsSet() {
		a = &RoleAssignment{Role: role, EndpointID: def.EndpointID, Model: def.Model}
	}
	if a == nil {
		return Endpoint{}, "", fmt.Errorf("role %q: %w", role, ErrRoleUnassigned)
	}
	var ep *Endpoint
	for i := range endpoints {
		if endpoints[i].ID == a.EndpointID {
			ep = &endpoints[i]
			break
		}
	}
	if ep == nil {
		return Endpoint{}, "", fmt.Errorf("role %q: the assigned endpoint %q %w", role, a.EndpointID, ErrRoleEndpointGone)
	}
	for _, m := range ep.Models {
		if m.Name == a.Model {
			return *ep, a.Model, nil
		}
	}
	return Endpoint{}, "", fmt.Errorf("role %q: the assigned model %q %w (endpoint %q)", role, a.Model, ErrRoleModelGone, ep.Name)
}
```

Extend the repository interface:

```go
type RoleRepository interface {
	LoadRoleAssignments() ([]RoleAssignment, error)
	AssignRole(a RoleAssignment) error
	// LoadDefaultModel returns the stored default, or the zero value when
	// none has been chosen. Never an error for "unset" — unset is a value.
	LoadDefaultModel() (DefaultModel, error)
	// SetDefaultModel replaces the default. The empty pair clears it.
	SetDefaultModel(d DefaultModel) error
}
```

- [ ] **Step 4: Persist it**

```go
// internal/profile/store.go — beside `Roles []RoleAssignment` at :81
	// DefaultModel is the one pair every unassigned role resolves through
	// (nocx-rikz5). omitempty because the zero value IS "no default": an
	// absent key and an empty pair mean the same thing, and writing an
	// empty object would make two spellings of one state.
	DefaultModel DefaultModel `json:"defaultModel,omitempty"`
```

```go
// LoadDefaultModel returns the chosen default, or the zero value when none
// has been chosen — "unset" is a value here, never an error.
func (s *JSONStore) LoadDefaultModel() (DefaultModel, error) {
	d, err := s.load()
	if err != nil {
		return DefaultModel{}, err
	}
	return d.DefaultModel, nil
}

// SetDefaultModel replaces the default in one write. The empty pair clears
// it, returning every unassigned role to the visible failure state.
func (s *JSONStore) SetDefaultModel(m DefaultModel) error {
	if err := ValidateDefaultModel(m); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	d.DefaultModel = m
	return s.writeLocked(d)
}
```

- [ ] **Step 4b: Clear the default when its endpoint is deleted**

`DeleteEndpoint` (`internal/profile/store.go:489`) is already ONE atomic
document write that removes the endpoint and clears its credential reference
from every remaining record. The default joins that same write — a second
write could fail in between and leave a default naming an endpoint that is
gone, which is precisely the state spec §6 forbids:

```go
	for i, existing := range d.Endpoints {
		if existing.ID == id {
			ref := existing.CredentialRef
			d.Endpoints = append(d.Endpoints[:i], d.Endpoints[i+1:]...)
			clearSecretRefLocked(d, ref)
			// The default is a single global convenience with nothing to
			// reassign, so it goes with the endpoint it named (spec §6). A
			// per-role ASSIGNMENT deliberately does NOT: it is a statement
			// about one role that the person made, and they are entitled to
			// be told it broke rather than to find it silently forgotten
			// (role.go:66, already tested at role_test.go:199).
			if d.DefaultModel.EndpointID == id {
				d.DefaultModel = DefaultModel{}
			}
			if err := s.writeLocked(d); err != nil {
				return "", err
			}
			return ref, nil
		}
	}
```

Test both halves, because the asymmetry is the point:

```go
func TestDeleteEndpoint_ClearsADefaultNamingItButLeavesAssignmentsDangling(t *testing.T) {
	s := newTestStore(t)
	mustSaveEndpoint(t, s, Endpoint{ID: "e1", Name: "openrouter", Models: []EndpointModel{{Name: "m-a"}}})
	if err := s.SetDefaultModel(DefaultModel{EndpointID: "e1", Model: "m-a"}); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	if err := s.AssignRole(RoleAssignment{Role: RoleClassifier, EndpointID: "e1", Model: "m-a"}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if _, err := s.DeleteEndpoint("e1"); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}

	def, err := s.LoadDefaultModel()
	if err != nil {
		t.Fatalf("LoadDefaultModel: %v", err)
	}
	if def.IsSet() {
		t.Fatalf("the default survived its endpoint as %+v — it now points at nothing", def)
	}

	as, err := s.LoadRoleAssignments()
	if err != nil {
		t.Fatalf("LoadRoleAssignments: %v", err)
	}
	if len(as) != 1 || as[0].EndpointID != "e1" {
		t.Fatalf("assignments = %+v, want the classifier's kept so the person is told it broke", as)
	}

	// And the product consequence, which is the criterion that matters: an
	// unassigned role is back at "choose a model", not "endpoint gone".
	if _, _, err := ResolveRole(RoleAnswering, as, def, nil); !errors.Is(err, ErrRoleUnassigned) {
		t.Fatalf("after the delete: err = %v, want ErrRoleUnassigned", err)
	}
}
```

- [ ] **Step 5: Fix EVERY existing caller — the tests are callers too**

Go has no overloading, so the six in-package test calls break the build the
moment the signature changes. `grep -v _test` would hide exactly the calls
that stop `go test ./internal/profile/...` from running at all.

Run: `grep -rn "ResolveRole(" --include=*.go internal/`

The complete list at the time of writing, and the argument each gains:

| Site                                | Add                                     |
| ----------------------------------- | --------------------------------------- |
| `internal/capability/config.go:657` | `def` from `s.roles.LoadDefaultModel()` |
| `internal/profile/role_test.go:126` | `DefaultModel{}`                        |
| `internal/profile/role_test.go:142` | `DefaultModel{}`                        |
| `internal/profile/role_test.go:158` | `DefaultModel{}`                        |
| `internal/profile/role_test.go:180` | `DefaultModel{}`                        |
| `internal/profile/role_test.go:190` | `DefaultModel{}`                        |

Every existing test passes the zero default deliberately: they assert the
behaviour of a store with no default, which is still a state the product has,
and rewriting them to carry one would delete that coverage.

In `internal/capability/config.go` a `LoadDefaultModel` error is **returned**,
never swallowed into "no default" — a store that cannot answer must not look
like a person who chose nothing, or an unreadable file renders as an honest
_Choose a model_ and sends someone to re-choose what they already chose.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/profile/... ./internal/capability/... -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/profile internal/capability
git commit -m "feat(profile): a role with no assignment resolves through the default (nocx-rikz5)"
```

---

### Task 2: The default reaches the wire

**Files:**

- Modify: `internal/transport/ws_roles.go`, `internal/transport/ws_config_handlers.go` (registration)
- Modify: `internal/capability/config.go` (service methods)
- Modify: `contracts/roles.list.schema.json`
- Test: `internal/transport/ws_roles_test.go`, `internal/transport/ws_contract_test.go`

**Interfaces:**

- Consumes: `profile.DefaultModel`, `RoleRepository.LoadDefaultModel/SetDefaultModel` (Task 1).
- Produces:
  - `roles.list` result grows `"default": {"endpointId": string, "model": string} | null`.
  - New method `roles.setDefault` with params `{endpointId: string, model: string}`; the empty pair clears.
  - `capability.ConfigService` grows `DefaultModel() (profile.DefaultModel, error)` and `SetDefaultModel(profile.DefaultModel) error`.

**Acceptance Criteria:**

- `roles.list` returns `default: null` on a fresh profile and the chosen pair after `roles.setDefault`.
- `roles.setDefault` with an endpoint id that names no endpoint is refused `-32602`; nothing is stored.
- `roles.setDefault` with both fields empty clears the default and succeeds.
- The result validates against the contract **over the real socket**, not only as a DTO.

- [ ] **Step 1: Write the failing over-the-socket test**

```go
// internal/transport/ws_roles_test.go
func TestRolesSetDefault_IsReadBackByRolesList(t *testing.T) {
	ws, stop := newRolesWSServer(t, rolesStoreWithEndpoint(t, "e1", "openrouter", "m-a"))
	defer stop()
	conn := connectWS(t, ws)

	if resp := vaultCall(t, conn, "roles.list", nil, 1); resp.Error != nil {
		t.Fatalf("roles.list: %+v", resp.Error)
	} else if !bytes.Contains(resp.Result, []byte(`"default":null`)) {
		t.Fatalf("a fresh profile reported %s, want default null", resp.Result)
	}

	set := vaultCall(t, conn, "roles.setDefault", map[string]any{"endpointId": "e1", "model": "m-a"}, 2)
	if set.Error != nil {
		t.Fatalf("roles.setDefault: %+v", set.Error)
	}

	after := vaultCall(t, conn, "roles.list", nil, 3)
	var got struct {
		Default *struct {
			EndpointID string `json:"endpointId"`
			Model      string `json:"model"`
		} `json:"default"`
	}
	if err := json.Unmarshal(after.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Default == nil || got.Default.EndpointID != "e1" || got.Default.Model != "m-a" {
		t.Fatalf("default read back as %+v, want e1/m-a", got.Default)
	}
}

func TestRolesSetDefault_RefusesAnEndpointThatDoesNotExist(t *testing.T) {
	ws, stop := newRolesWSServer(t, rolesStoreWithEndpoint(t, "e1", "openrouter", "m-a"))
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "roles.setDefault", map[string]any{"endpointId": "ghost", "model": "m-a"}, 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602", resp.Error)
	}
	list := vaultCall(t, conn, "roles.list", nil, 2)
	if !bytes.Contains(list.Result, []byte(`"default":null`)) {
		t.Fatalf("a refused write left %s, want default null", list.Result)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/transport/ -run TestRolesSetDefault -v`
Expected: FAIL — `method not found: roles.setDefault`.

- [ ] **Step 3: Extend the contract**

```json
// contracts/roles.list.schema.json — add to "properties", and to "required"
"default": {
  "description": "The one (endpoint, model) pair every role with no assignment of its own resolves through (nocx-rikz5). Null when the person has chosen none — which is the state a fresh profile is in, and the state in which the assistant is not ready. It is never a pair the product picked: a default the product invented is the silent fallback nocx-e6kn2 forbids.",
  "anyOf": [
    {
      "type": "object",
      "additionalProperties": false,
      "required": ["endpointId", "model"],
      "properties": {
        "endpointId": { "type": "string" },
        "model": { "type": "string" }
      }
    },
    { "type": "null" }
  ]
}
```

- [ ] **Step 4: Implement the handler**

In `internal/transport/ws_roles.go`, `rolesListResponse` grows
`Default *defaultModelWire` with a `json:"default"` tag, filled from
`svc.DefaultModel()`. Add the `roles.setDefault` arm beside `roles.assign` and
register it in `ws_config_handlers.go` on the same `configSub` queue.

**Validation, decided here rather than inherited.** `roles.assign` does NOT
check that the endpoint exists — `validateRoleAssign`
(`internal/transport/ws_roles.go:97`) checks the role name, the pair shape,
the id's rune class and the model's length, and nothing else; the store
deliberately accepts an assignment naming no endpoint, which
`internal/profile/role_test.go:199` pins as intended. There is no existing
shared rule to follow, and this plan does not pretend otherwise.

`roles.setDefault` DOES validate — that the endpoint exists **and that it
offers the model**, both inside the store's write critical section against the
one loaded document, so a concurrent delete cannot slip between the check and
the write. (Corrected 2026-08-21 after review: an earlier draft validated only
existence and validated it a lock away from the write, which stored a default
naming a model nobody offers and then reported `model-gone` about a selection
that should never have been accepted.) The asymmetry with assignments is Task 1's: a
per-role assignment is a statement about one role the person is entitled to be
told about when it breaks, so a dangling one is a feature. The default is a
global convenience every unassigned role inherits silently, so a dangling one
breaks every role at once with nothing naming which choice did it. Refused at
the wire — and resolution still refuses at read time for the
delete-between-load-and-resolve race (Task 1, Step 1).

- [ ] **Step 5: Regenerate and run**

```bash
cd frontend && npm run contracts:check   # fails until the generated type is regenerated
npm run contracts                        # regenerate
cd .. && go test ./internal/transport/ -run 'TestRoles' -count=1
```

Expected: PASS, and `frontend/src/generated/roles.list.ts` carries `default`.

- [ ] **Step 6: Commit**

```bash
git add contracts internal/transport internal/capability frontend/src/generated
git commit -m "feat(transport): the default model is set and read back on the wire (nocx-rikz5)"
```

---

### Task 3: `agent.status` answers the role question

**Files:**

- Modify: `internal/transport/ws_assistant.go:43-47` (the result), `:106-155` (the handler)
- Modify: `contracts/agent.status.schema.json`
- Test: `internal/transport/ws_assistant_test.go`, `internal/transport/ws_contract_test.go`

**Interfaces:**

- Consumes: `ResolveRole` with the default (Task 1); `capability.ConfigService.ResolveRole` unchanged in name.
- Produces: `agentStatusResult` grows

```go
	// Answering is the resolution of the role the ask will use. "ready" or
	// one of the refusal reasons; never absent.
	Answering answeringWire `json:"answering"`
```

with `answeringWire{ Ready bool; Reason *string; Endpoint *string; Model *string }` and the reason enum
`"no-endpoints" | "no-models" | "unassigned" | "endpoint-gone" | "model-gone" | "unavailable"`.

Six values, not four: `no-models` and `unavailable` are states the real system
reaches and the four-value vocabulary answered wrongly (see the acceptance
criteria).

**Acceptance Criteria:**

- No endpoints at all → `answering.ready=false`, `reason="no-endpoints"`.
- An endpoint with a key but no default and no assignment → `ready=false`, `reason="unassigned"`. **This is the case that reports "Ready" today and is the reason this task exists.**
- A default set → `ready=true`, with `endpoint` and `model` naming what will answer.
- A default whose endpoint was deleted → `ready=false`, `reason="endpoint-gone"`.
- **The credential reported is the RESOLVED endpoint's, not the fleet's.** Today `handleAgentStatus` (`internal/transport/ws_assistant.go:123-152`) scans every endpoint and reports `resolvable` if ANY one resolves. That makes "the selected endpoint's key was deleted while an unrelated endpoint holds a valid one" report ready. Once readiness is about a role, the credential must be classified for exactly `ep.CredentialRef` of the endpoint the role resolved to. Fleet-wide endpoint health belongs on the Endpoints page, not here.
- `credential` is null when the role does not resolve — there is no endpoint the question is about.
- **An endpoint that offers zero models** reports `reason="no-models"`, not `unassigned`: _Choose a model_ would send a person to a picker with nothing in it.
- **A store that cannot answer** (`ListEndpoints`, `LoadDefaultModel` or `ListRoleAssignments` failing) reports `reason="unavailable"` rather than an RPC error, because the ladder must have a rung for it — an error toast is the soft degrade with no repair path.
- `endpointConfigured` and `lastProbe` keep their current meanings. `lastProbe` is reported ONLY when its endpoint and model match the current resolution — a probe from another model advertising success or failure for this one is a lie the person cannot see through.
- Validated over the real socket.

- [ ] **Step 1: Write the failing test — the exact lie, first**

```go
func TestAgentStatus_AnEndpointWithNoModelChosenIsNotReady(t *testing.T) {
	// The defect this task exists for: an endpoint with a resolvable key and
	// nothing assigned reported "Ready", and the refusal arrived one
	// keystroke later.
	ws, stop := newAssistantWSServer(t, storeWithKeyedEndpoint(t, "e1", "openrouter", "m-a"))
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "agent.status", nil, 1)
	var got struct {
		EndpointConfigured bool `json:"endpointConfigured"`
		Answering          struct {
			Ready  bool    `json:"ready"`
			Reason *string `json:"reason"`
		} `json:"answering"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.EndpointConfigured {
		t.Fatalf("endpointConfigured = false, want true — the endpoint exists")
	}
	if got.Answering.Ready {
		t.Fatalf("answering.ready = true with no model chosen — this is the lie")
	}
	if got.Answering.Reason == nil || *got.Answering.Reason != "unassigned" {
		t.Fatalf("reason = %v, want unassigned", got.Answering.Reason)
	}
}

func TestAgentStatus_NoEndpointsSaysSoRatherThanUnassigned(t *testing.T) {
	ws, stop := newAssistantWSServer(t, emptyStore(t))
	defer stop()
	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "agent.status", nil, 1)
	if !bytes.Contains(resp.Result, []byte(`"reason":"no-endpoints"`)) {
		t.Fatalf("status = %s, want reason no-endpoints", resp.Result)
	}
}

func TestAgentStatus_ADefaultMakesItReadyAndNamesTheModel(t *testing.T) {
	store := storeWithKeyedEndpoint(t, "e1", "openrouter", "m-a")
	if err := store.SetDefaultModel(profile.DefaultModel{EndpointID: "e1", Model: "m-a"}); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	ws, stop := newAssistantWSServer(t, store)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "agent.status", nil, 1)
	var got struct {
		Answering struct {
			Ready    bool    `json:"ready"`
			Endpoint *string `json:"endpoint"`
			Model    *string `json:"model"`
		} `json:"answering"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Answering.Ready || got.Answering.Endpoint == nil || *got.Answering.Endpoint != "openrouter" ||
		got.Answering.Model == nil || *got.Answering.Model != "m-a" {
		t.Fatalf("answering = %+v, want ready with openrouter/m-a", got.Answering)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/transport/ -run TestAgentStatus -v`
Expected: FAIL — `answering.ready = true with no model chosen — this is the lie`.

- [ ] **Step 3: Extend the contract**

Add `answering` to `contracts/agent.status.schema.json`'s `properties` and
`required`, `additionalProperties: false` on the nested object, `reason`
constrained to the four-value enum, and `endpoint`/`model` nullable (present
only when ready). Update the schema's top `description` so it says the status
reports the role's resolvability, not endpoint existence.

- [ ] **Step 4: Implement**

The handler is **restructured**, not extended. Today it decides the credential
by scanning every endpoint (`internal/transport/ws_assistant.go:128-153`) and
returns early when there are none (`:121-123`) — the early return would skip
the answering fact entirely, in the very case that most needs a reason.

The new order: resolve the role first, then ask about THAT endpoint's key.

```go
	eps, err := svc.ListEndpoints()
	if err != nil {
		// A store that cannot answer is a rung, not an RPC error: an error
		// toast leaves a person with nothing to do next.
		res.Answering = answeringWire{Reason: strPtr(reasonUnavailable)}
		return nil
	}
	res.EndpointConfigured = len(eps) > 0

	ep, model, resolveErr := svc.ResolveRole(profile.RoleAnswering)
	switch {
	case resolveErr == nil:
		name := ep.Name
		res.Answering = answeringWire{Ready: true, Endpoint: &name, Model: &model}
		// THE CREDENTIAL OF THE ENDPOINT THAT WILL ANSWER, and of no other.
		// The old aggregate ("any endpoint resolves") reported ready when the
		// selected endpoint's key was gone and an unrelated one was fine —
		// which is the same shape of lie as reporting readiness from endpoint
		// existence, one level down.
		cred := h.credentialStateFor(ctx, ep.CredentialRef)
		res.Credential = &cred
		// A probe describes ONE endpoint and model. Reported only when it
		// describes this one; otherwise "Last test ok" is about something the
		// person is not asking.
		if res.LastProbe != nil && !probeDescribes(res.LastProbe, ep, model) {
			res.LastProbe = nil
		}
	case len(eps) == 0:
		// Before `unassigned`: with no endpoints there is nothing to assign,
		// and sending a person to choose from an empty list is the one answer
		// worse than saying nothing (spec §3).
		res.Answering = answeringWire{Reason: strPtr(reasonNoEndpoints)}
	// THE SPECIFIC REFUSALS COME FIRST. Corrected 2026-08-21 after review: with
	// no-models above them, a default naming a deleted endpoint reported
	// "that endpoint offers no models" whenever the surviving endpoint
	// happened to be empty — and sent the person to Endpoints when the repair
	// was in Roles. A dangling selection keeps its own error whatever the
	// rest of the fleet looks like.
	case errors.Is(resolveErr, profile.ErrRoleEndpointGone):
		res.Answering = answeringWire{Reason: strPtr(reasonEndpointGone)}
	case errors.Is(resolveErr, profile.ErrRoleModelGone):
		res.Answering = answeringWire{Reason: strPtr(reasonModelGone)}
	case errors.Is(resolveErr, profile.ErrRoleUnassigned) && !anyEndpointOffersAModel(eps):
		// Nothing is selected AND there is nothing to select. "Choose a model"
		// would open a picker with no options — a repair the person cannot
		// perform, so the rung points at Endpoints instead.
		res.Answering = answeringWire{Reason: strPtr(reasonNoModels)}
	case errors.Is(resolveErr, profile.ErrRoleUnassigned):
		res.Answering = answeringWire{Reason: strPtr(reasonUnassigned)}
	default:
		// Includes a role-store read failure surfaced through ResolveRole.
		res.Answering = answeringWire{Reason: strPtr(reasonUnavailable)}
	}
	return nil
```

`credential` stays nil on every refusal arm: with no resolved endpoint there is
no key the question is about, and the old "first other endpoint's fact" would
be a sentence about an endpoint nobody chose.

- [ ] **Step 4b: The paired tests for the two new arms and the credential scope**

```go
func TestAgentStatus_TheCredentialIsTheResolvedEndpointsAndNoOther(t *testing.T) {
	// The defect: an unrelated healthy endpoint used to make the whole thing
	// report resolvable while the endpoint that would actually answer had no
	// key at all.
	store := storeWithEndpoints(t,
		keyedEndpoint("e1", "chosen", "m-a", noKey),
		keyedEndpoint("e2", "unrelated", "m-b", validKey),
	)
	mustSetDefault(t, store, "e1", "m-a")
	ws, stop := newAssistantWSServer(t, store)
	defer stop()

	resp := vaultCall(t, connectWS(t, ws), "agent.status", nil, 1)
	var got struct {
		Credential *string `json:"credential"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Credential == nil || *got.Credential != "none" {
		t.Fatalf("credential = %v, want none — e1 answers and e1 has no key", got.Credential)
	}
}

func TestAgentStatus_AnEndpointOfferingNoModelsSaysSo(t *testing.T) {
	ws, stop := newAssistantWSServer(t, storeWithEndpoints(t, endpointWithNoModels("e1", "empty")))
	defer stop()
	resp := vaultCall(t, connectWS(t, ws), "agent.status", nil, 1)
	if !bytes.Contains(resp.Result, []byte(`"reason":"no-models"`)) {
		t.Fatalf("status = %s, want no-models — Choose a model would open an empty picker", resp.Result)
	}
}

func TestAgentStatus_AStoreThatCannotAnswerIsARungNotAnError(t *testing.T) {
	ws, stop := newAssistantWSServer(t, failingStore(t, errors.New("disk gone")))
	defer stop()
	resp := vaultCall(t, connectWS(t, ws), "agent.status", nil, 1)
	if resp.Error != nil {
		t.Fatalf("error = %+v; a store failure must be a reported state, not an RPC error", resp.Error)
	}
	if !bytes.Contains(resp.Result, []byte(`"reason":"unavailable"`)) {
		t.Fatalf("status = %s, want unavailable", resp.Result)
	}
}
```

- [ ] **Step 5: Run**

Run: `go test ./internal/transport/... -count=1`
Expected: PASS, contract tests included.

- [ ] **Step 6: Commit**

```bash
git add contracts internal/transport frontend/src/generated
git commit -m "feat(transport): agent.status reports whether the role resolves, not whether an endpoint exists (nocx-rikz5)"
```

---

### Task 4: The ladder — one rung, one sentence, one target

**Files:**

- Modify: `frontend/src/agent-status-line.ts`
- Test: `frontend/src/agent-status-line.test.ts`

**Interfaces:**

- Consumes: `AgentStatusResult.answering` (Task 3, via the generated type).
- Produces:

```ts
export interface AgentStatusLine {
  tone: 'neutral' | 'success' | 'warning' | 'danger'
  text: string
  /** Where this rung is fixed. Absent when nothing is broken. */
  fix?: { label: string; page: 'endpoints' | 'roles' }
}
```

The exact copy, used verbatim by Tasks 5 and 6:

| reason          | text                                                        | tone    | fix.page    |
| --------------- | ----------------------------------------------------------- | ------- | ----------- |
| `no-endpoints`  | `Add an endpoint first`                                     | neutral | `endpoints` |
| `no-models`     | `That endpoint offers no models — check it`                 | warning | `endpoints` |
| `unassigned`    | `Choose a model`                                            | warning | `roles`     |
| `endpoint-gone` | `The model's endpoint is gone — choose another`             | danger  | `roles`     |
| `model-gone`    | `That model is no longer offered — choose another`          | danger  | `roles`     |
| `unavailable`   | `Settings could not be read — the assistant is unavailable` | danger  | _(none)_    |

**Precedence, stated because both halves can be broken at once.** The role
comes first and the credential second, and the order is not arbitrary: an
unresolved role has no endpoint, so there is no credential to have an opinion
about — the sentence would name an endpoint nobody chose. Once the role
resolves, the credential of THAT endpoint outranks any probe result, because a
key that is gone stops the ask whatever the last probe said. `unavailable`
carries no `fix`: no page repairs an unreadable store, and a button that leads
nowhere is worse than no button.

**Acceptance Criteria:**

- Role-first: an unready role produces its sentence even when `endpointConfigured` is true and the credential resolves.
- Each rung carries the `fix.page` and the tone from the table, exactly.
- **A test per rung — all six, not three.** Each asserts the sentence AND the target, because a rung that says the right thing and opens the wrong page is the defect the ladder exists to prevent.
- `unavailable` produces no `fix`.
- A ready role keeps today's behaviour: probe result, or `Ready`.
- Credential problems still win over a ready role (a resolvable role with a deleted key is not usable).
- `null` status still returns `null` — a surface shows its placeholder, not a lie.

- [ ] **Step 1: Write the failing test**

```ts
it('says to choose a model rather than Ready when nothing is assigned', () => {
  const line = agentStatusLine({
    endpointConfigured: true,
    credential: 'resolvable',
    lastProbe: null,
    answering: { ready: false, reason: 'unassigned', endpoint: null, model: null },
  })
  expect(line).toEqual({
    tone: 'warning',
    text: 'Choose a model',
    fix: { label: 'Choose a model', page: 'roles' },
  })
})

it('sends a person with no endpoints to endpoints, never to an empty model list', () => {
  const line = agentStatusLine({
    endpointConfigured: false,
    credential: null,
    lastProbe: null,
    answering: { ready: false, reason: 'no-endpoints', endpoint: null, model: null },
  })
  expect(line?.text).toBe('Add an endpoint first')
  expect(line?.fix?.page).toBe('endpoints')
})

it('keeps Ready for a role that resolves', () => {
  const line = agentStatusLine({
    endpointConfigured: true,
    credential: 'resolvable',
    lastProbe: null,
    answering: { ready: true, reason: null, endpoint: 'openrouter', model: 'm-a' },
  })
  expect(line).toEqual({ tone: 'success', text: 'Ready' })
})
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd frontend && npx vitest run src/agent-status-line.test.ts`
Expected: FAIL — the current function returns `{tone:'success',text:'Ready'}` for the first case.

- [ ] **Step 3: Invert the branch order**

```ts
export function agentStatusLine(st: AgentStatusResult | null): AgentStatusLine | null {
  if (!st) return null
  // THE ROLE FIRST (nocx-rikz5). This used to open on endpointConfigured, so
  // an endpoint with a valid key and no model chosen reported "Ready" and the
  // refusal arrived at the first question. Readiness is whether the role the
  // ask will use can resolve; the endpoint and the credential are reasons it
  // cannot, not a separate headline.
  if (!st.answering.ready) {
    switch (st.answering.reason) {
      case 'no-endpoints':
        return {
          tone: 'neutral',
          text: 'Add an endpoint first',
          fix: { label: 'Add an endpoint first', page: 'endpoints' },
        }
      case 'unassigned':
        return {
          tone: 'warning',
          text: 'Choose a model',
          fix: { label: 'Choose a model', page: 'roles' },
        }
      case 'endpoint-gone':
        return {
          tone: 'danger',
          text: "The model's endpoint is gone — choose another",
          fix: { label: 'Choose a model', page: 'roles' },
        }
      case 'model-gone':
        return {
          tone: 'danger',
          text: 'That model is no longer offered — choose another',
          fix: { label: 'Choose a model', page: 'roles' },
        }
    }
  }
  // A resolvable role still needs a usable credential: a key that is gone
  // stops the ask just as surely as an unassigned role.
  const line = credentialLine(st.credential)
  if (line) return line
  const p = st.lastProbe
  if (p && !p.ok) return { tone: 'danger', text: `Last test failed: ${p.error}` }
  if (p && p.ok) return { tone: 'success', text: `Last test ok (${p.model})` }
  return { tone: 'success', text: 'Ready' }
}
```

- [ ] **Step 4: Run**

Run: `npx vitest run src/agent-status-line.test.ts && npx tsc --noEmit`
Expected: PASS, no type errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/agent-status-line.ts frontend/src/agent-status-line.test.ts
git commit -m "feat(frontend): the readiness line names the rung and where it is fixed (nocx-rikz5)"
```

---

### Task 5: The Roles page — the default control and the line's new rule

**Files:**

- Modify: `frontend/src/roles-section.tsx` (the default control, "As default", the line at `:63-78`)
- Modify: `frontend/src/endpoints.ts` (the roles client: `listRoles` at `:98`, `assignRole` at `:105` — add `setDefault` beside them)
- Test: `frontend/src/roles-section.test.tsx`

**Interfaces:**

- Consumes: `roles.list`'s `default` and `roles.setDefault` (Task 2).
- **Type names, read from the files rather than recalled.** The page's row
  type is `WireRole` and the endpoint type is `Endpoint`, both exported from
  `frontend/src/endpoints.ts:37,47` — there is no `RoleDTO` in the frontend.
  The default's type is `RolesListResult['default']` from the generated
  `frontend/src/generated/roles.list.ts` (regenerated in Task 2 step 5) and is
  never hand-written: a hand-written renderer type carrying a field the
  backend does not send is the `vault.status` defect in AGENTS.md.
- **The helper is `roleStateLine`** (`frontend/src/roles-section.tsx:60`),
  already exported and unit-tested. It is EXTENDED, not replaced by a new
  name — the page and the ask must never grow two answers to "what does this
  role mean".
- Produces, and this is a **signature change to two existing methods**:
  - `EndpointClient.listRoles(): Promise<RolesListResult>` — today it returns
    `Promise<RolesListResult['roles']>` (`frontend/src/endpoints.ts:97`) and
    its `.then((r) => r.roles)` throws the default away before the page can
    ever see it. Same for `assignRole` at `:105`.
  - `EndpointClient.setDefault(input): Promise<RolesListResult>` where input is
    `{ endpointId: string; model: string }`.
  - Callers read `.roles` at the call site instead. Run
    `grep -rn "listRoles\|assignRole" frontend/src` — `roles-section.tsx` is
    the only production caller.

  Returning the whole result rather than adding a second `default` accessor is
  what makes load and write adopt both fields **atomically**: two calls would
  let the page render a default and a role table from different moments.

**Acceptance Criteria:**

- A "Default model" control renders above the roles; choosing a pair calls `roles.setDefault` and the page reflects it without a reload.
- Each role's select offers **"As default"** and shows it when the role has no assignment of its own.
- The green line is **absent** when a role's own assignment matches what the selects already show.
- The line **is** present, naming endpoint and model, when the role resolves through the default.
- The line names the failure when the role cannot resolve.
- A person can reach a working assistant from this page alone: set the default, and every role reads "As default".
- **The write's test goes through the dispatcher seam and observes adoption**, not a spy call count: after choosing the default, the page shows it AND every unassigned role visibly resolves through it. A test asserting only that `setDefault` was called stays green when the returned state is dropped on the floor, when the control never updates, and when the method name is wrong.
- Partial failure: when `roles.setDefault` rejects, the previous default stays on screen and a toast says so — the page never shows a default the store did not take.

- [ ] **Step 1: Write the failing tests**

```tsx
it('does not repeat the selects: an explicitly assigned role gets no status line', async () => {
  const client = mockedClient({
    roles: [{ role: 'answering', endpointId: 'e1', model: 'm-a' }],
    default: null,
    endpoints: [{ id: 'e1', name: 'openrouter', models: [{ name: 'm-a' }] }],
  })
  const container = mount(client)
  const row = await findRow(container, 'answering')
  expect(row.querySelector('.roles-role__state')).toBeNull()
})

it('names the model when the role resolves through the default, because the select only says "As default"', async () => {
  const client = mockedClient({
    roles: [{ role: 'answering', endpointId: null, model: null }],
    default: { endpointId: 'e1', model: 'm-a' },
    endpoints: [{ id: 'e1', name: 'openrouter', models: [{ name: 'm-a' }] }],
  })
  const container = mount(client)
  const row = await findRow(container, 'answering')
  expect(row.textContent).toContain('openrouter')
  expect(row.textContent).toContain('m-a')
})

it('adopts the default the wire returns, and every unassigned role then reads through it', async () => {
  // Through the dispatcher seam, not a spy on the client: the assertion is
  // that the PAGE changed, which is the thing a person can see.
  const dispatcher = new Dispatcher()
  vi.spyOn(dispatcher, 'call').mockImplementation((method: string) => {
    if (method === 'roles.list') {
      return Promise.resolve({
        roles: [{ role: 'answering', endpointId: null, model: null }],
        default: null,
      })
    }
    if (method === 'roles.setDefault') {
      return Promise.resolve({
        roles: [{ role: 'answering', endpointId: null, model: null }],
        default: { endpointId: 'e1', model: 'm-a' },
      })
    }
    if (method === 'endpoints.list') {
      return Promise.resolve({
        endpoints: [{ id: 'e1', name: 'openrouter', models: [{ name: 'm-a' }] }],
      })
    }
    return Promise.reject(new Error(`unexpected ${method}`))
  })
  const container = mount(new EndpointClient(dispatcher))
  const control = await findDefaultControl(container)
  fireEvent.change(within(control).getByLabelText('Endpoint'), { target: { value: 'e1' } })
  fireEvent.change(within(control).getByLabelText('Model'), { target: { value: 'm-a' } })

  // The role row now resolves THROUGH the default, and says so — which the
  // two selects cannot, because they read "As default".
  const row = await findRow(container, 'answering')
  await vi.waitFor(() => expect(row.textContent).toContain('As default: openrouter · m-a'))
})
```

- [ ] **Step 2: Run and watch them fail**

Run: `cd frontend && npx vitest run src/roles-section.test.tsx`
Expected: FAIL — no default control, and the status line renders for every row.

- [ ] **Step 3: Implement the line's rule**

```ts
/** The line exists to say what the two selects CANNOT (nocx-rikz5). When a
 *  role names its own endpoint and model, the selects already show both and a
 *  sentence repeating them is noise — so there is no line at all. The line
 *  speaks when resolution goes somewhere the controls do not show: through the
 *  default (the select reads "As default"), or nowhere.
 *
 *  This EXTENDS the existing roleStateLine rather than adding a second
 *  resolver beside it: the page and the ask may never disagree about what a
 *  role means, which is the reason that function was written pure and
 *  unit-tested in the first place. Note the return type gains `| null` — the
 *  silence is a value, not an empty string.
 */
export function roleStateLine(
  row: WireRole,
  def: RolesListResult['default'],
  endpoints: Endpoint[],
): { tone: StatusDotTone; text: string } | null {
  // An explicit assignment: the two selects already show it, so a healthy row
  // says nothing and a broken one keeps today's three sentences verbatim.
  if (row.endpointId !== null && row.model !== null) {
    return brokenLine(row.endpointId, row.model, endpoints)
  }
  if (!def) {
    return { tone: 'warning', text: 'No model assigned — the role cannot be used until it is' }
  }
  const broken = brokenLine(def.endpointId, def.model, endpoints)
  if (broken) return broken
  const ep = endpoints.find((e) => e.id === def.endpointId)!
  const model = ep.models.find((m) => m.name === def.model)!
  return { tone: 'ok', text: `As default: ${ep.name} · ${model.alias ?? model.name}` }
}

/** The two refusals a stored pair can carry, worded exactly as they are
 *  today. Null when the pair resolves. */
function brokenLine(
  endpointId: string,
  modelName: string,
  endpoints: Endpoint[],
): { tone: StatusDotTone; text: string } | null {
  const ep = endpoints.find((e) => e.id === endpointId)
  if (!ep) {
    return { tone: 'error', text: 'The assigned endpoint no longer exists — reassign this role' }
  }
  const model = ep.models.find((m) => m.name === modelName)
  if (!model) {
    return {
      tone: 'error',
      text: `The assigned model "${modelName}" is no longer offered by ${ep.name} — reassign this role`,
    }
  }
  return null
}
```

Add the default control above the `<For>` over roles, and give each role select
the extra option `{ value: '', label: 'As default' }` selected when
`row.endpointId === null`.

- [ ] **Step 4: Run**

Run: `npx vitest run src/roles-section.test.tsx && npx tsc --noEmit && npx eslint src/roles-section.tsx --max-warnings=0`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/roles-section.tsx frontend/src/roles-section.test.tsx frontend/src/endpoints.ts
git commit -m "feat(frontend): the default model is chosen here, and the line stops repeating the selects (nocx-rikz5)"
```

---

### Task 6: The model chip in the editor chrome

**Files:**

- Modify: `frontend/src/editor.ts` (a chip pair in `chromeLeft`, beside `cwdChip`)
- Modify: `frontend/src/terminal-content.ts` (feed it from `agent.status` + the active input target)
- Modify: `frontend/src/style.css` (the chip's own rule, beside `.nocx-editor-cwd`)
- Test: `frontend/src/editor.test.ts`, `frontend/src/terminal-content.test.ts`

**Interfaces:**

- Consumes: `agentStatusLine`'s `fix` (Task 4) and `AgentStatusResult.answering` (Task 3).
- **There is no status STATE in the renderer today, and the plan previously
  claimed there was.** `terminal-content.ts:1373` passes
  `status: () => new AgentClient(this.client.dispatcher).status()` into
  `AgentInputTarget` — a function called at refusal time, not a stored,
  refreshable fact. This task creates the owner.
- Produces:
  - `AgentReadiness` in a new `frontend/src/agent-readiness.ts`: one store
    holding the last `AgentStatusResult`, a `refresh()` and a subscribe seam.
    **One owner** (AD-8) — the chip and any later surface read the same store
    rather than each calling `agent.status`.
  - `Editor.setModelChip(state: ModelChipState | null): void`
  - `Editor.onModelChipClick(handler: (page: 'endpoints' | 'roles') => void)`
  - A new host hook `onOpenRoles?: () => void` beside the existing
    `onCreateEndpoint` (`terminal-content.ts:319`), wired in `main.tsx` to
    `openSettingsPane()` on the Roles page. `onCreateEndpoint` already exists
    and is reused for the `endpoints` target rather than duplicated.
- Produces `ModelChipState` where

```ts
export type ModelChipState =
  | { kind: 'ready'; endpoint: string; model: string }
  | { kind: 'action'; text: string; page: 'endpoints' | 'roles' | null }
```

**Acceptance Criteria:**

- The chip is present only when the active input target is the assistant (Ask); switching to Run removes it.
- Ready → two chips: the endpoint (click → Endpoints) and the model (click → Roles). **Both destinations are tested**, not only the unready one.
- Not ready → one chip carrying the ladder's sentence, click → that rung's page. A rung with no `fix` (`unavailable`) renders a chip that is not a button.
- The model id is truncated to one line, with the full value in `title` and the accessible name.
- **The chip refreshes when the facts change**, not only on mount. `AgentReadiness.refresh()` is called after: endpoint create/update/delete, `roles.assign`, `roles.setDefault`, entering Ask mode, and socket reconnect. Without this the end-to-end path in Task 7 cannot pass — the chip would still read _Add an endpoint first_ after an endpoint was added.
- **A late response never repaints a newer state.** Each refresh carries a monotonically increasing sequence; a reply whose sequence is below the last applied one is discarded. Two refreshes racing (add an endpoint, then immediately set a default) must not leave the older answer on screen.
- **The composer's height does not change** when the chip appears or disappears — asserted by measuring, not assumed: capture the chrome row's height in Run, switch to Ask with a 40-character model id, assert the height is unchanged and the chip is one line.

- [ ] **Step 1: Write the failing tests**

```ts
it('shows no model chip while Enter goes to the shell', async () => {
  const { content } = await mountTerminal(makeClipboard(), {}, clientWithStatus(READY_STATUS))
  expect(chipsOf(content)).toEqual([])
})

it('names the model that will answer once the target is the assistant', async () => {
  const { content } = await mountTerminal(makeClipboard(), {}, clientWithStatus(READY_STATUS))
  switchToAsk(content)
  await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
})

it('offers the rung, not the model, when nothing is chosen', async () => {
  const { content } = await mountTerminal(makeClipboard(), {}, clientWithStatus(UNASSIGNED_STATUS))
  switchToAsk(content)
  await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Choose a model']))
})

it('opens the page the rung names', async () => {
  const opened: string[] = []
  const { content } = await mountTerminal(
    makeClipboard(),
    { onOpenSettingsPage: (p: string) => opened.push(p) },
    clientWithStatus(NO_ENDPOINTS_STATUS),
  )
  switchToAsk(content)
  await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Add an endpoint first']))
  clickChip(content, 'Add an endpoint first')
  expect(opened).toEqual(['endpoints'])
})
```

```ts
it('opens Endpoints from the provider and Roles from the model', async () => {
  const opened: string[] = []
  const { content } = await mountTerminal(
    makeClipboard(),
    { onCreateEndpoint: () => opened.push('endpoints'), onOpenRoles: () => opened.push('roles') },
    clientWithStatus(READY_STATUS),
  )
  switchToAsk(content)
  await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
  clickChip(content, 'openrouter')
  clickChip(content, 'm-a')
  expect(opened).toEqual(['endpoints', 'roles'])
})

it('repaints when the facts change, not only on mount', async () => {
  // Without this the end-to-end path cannot pass: the chip would still read
  // "Add an endpoint first" after an endpoint had been added.
  const readiness = new AgentReadiness(clientWithStatus(NO_ENDPOINTS_STATUS))
  const { content } = await mountTerminal(makeClipboard(), { readiness }, undefined)
  switchToAsk(content)
  await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Add an endpoint first']))
  readiness._setStatusForTest(UNASSIGNED_STATUS)
  await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Choose a model']))
})

it('discards a late reply that would repaint an older state', async () => {
  const readiness = new AgentReadiness(slowThenFastClient(NO_ENDPOINTS_STATUS, READY_STATUS))
  const { content } = await mountTerminal(makeClipboard(), { readiness }, undefined)
  switchToAsk(content)
  void readiness.refresh() // slow, resolves LAST with the older facts
  void readiness.refresh() // fast, resolves first with the newer facts
  await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
  await flushAll()
  expect(chipsOf(content)).toEqual(['openrouter', 'm-a'])
})

it('does not change the composer height when the chip appears', async () => {
  // Measured, not assumed: a chip that wraps pushes the chrome to a second
  // row, and every text assertion above stays green while it does.
  const { content } = await mountTerminal(
    makeClipboard(),
    { attachToDocument: true },
    clientWithStatus(statusWithModel('deepseek/deepseek-v4-flash-0731-preview')),
  )
  const chrome = chromeRowOf(content)
  const before = chrome.getBoundingClientRect().height
  switchToAsk(content)
  await vi.waitFor(() => expect(chipsOf(content).length).toBe(2))
  expect(chrome.getBoundingClientRect().height).toBe(before)
})
```

- [ ] **Step 2: Run and watch them fail**

Run: `cd frontend && npx vitest run src/terminal-content.test.ts -t "model chip"`
Expected: FAIL — `chipsOf` finds no `.nocx-editor-model` element.

**On the height test and jsdom.** jsdom computes no layout, so
`getBoundingClientRect` returns zeroes there and the assertion passes
vacuously. Put THIS one in the Playwright suite (Task 7's file), where a real
engine measures, and leave the text assertions in vitest. A layout claim
asserted in jsdom is the green-suite-over-a-broken-product shape AGENTS.md
opens with.

- [ ] **Step 3: Implement the chip**

In `editor.ts`, beside `cwdChip` (the same `.nocx-chip` family — the row has no
`ui-badge` and must not grow one):

```ts
// The model that will answer, and the way to change it (nocx-rikz5).
// Buttons rather than spans because they are controls: `recoveryChip`
// above is the precedent, and a chip that navigates must be reachable by
// keyboard. Hidden until setModelChip is called with a state — exactly
// how locationChip behaves, so the row's height never moves.
this.modelEndpointChip = document.createElement('button')
this.modelEndpointChip.type = 'button'
this.modelEndpointChip.className = 'nocx-chip nocx-editor-model'
this.modelEndpointChip.style.display = 'none'
// The listener is installed ONCE and reads a slot, exactly as recoveryChip
// does at editor.ts:317/445. Re-adding a listener on every state change is
// how one click ends up firing three times.
this.modelEndpointChip.addEventListener('click', () => {
  const t = this._modelChipTargets.endpoint
  if (t) this._onModelChipClick?.(t)
})

this.modelChip = document.createElement('button')
this.modelChip.type = 'button'
this.modelChip.className = 'nocx-chip nocx-editor-model'
this.modelChip.style.display = 'none'
this.modelChip.addEventListener('click', () => {
  const t = this._modelChipTargets.model
  if (t) this._onModelChipClick?.(t)
})

this.chromeLeft.append(
  this.recoveryChip,
  this.locationChip,
  this.cwdChip,
  this.modelEndpointChip,
  this.modelChip,
)
```

```ts
  /** The model chip's one writer. Null hides both chips — the state a Run
   *  target is in, where no model answers anything and a chip claiming one
   *  would be decoration. */
  setModelChip(state: ModelChipState | null): void {
    // Targets are STORED, never captured in a closure: the chips' meaning
    // changes with every state while the listeners above are permanent.
    this._modelChipTargets = { endpoint: null, model: null }
    if (state === null) {
      this.modelEndpointChip.style.display = 'none'
      this.modelChip.style.display = 'none'
      return
    }
    if (state.kind === 'ready') {
      this._modelChipTargets = { endpoint: 'endpoints', model: 'roles' }
      this.modelEndpointChip.style.display = ''
      this.modelEndpointChip.textContent = state.endpoint
      this.modelEndpointChip.title = state.endpoint
      this.modelEndpointChip.setAttribute('aria-label', `Answers with ${state.endpoint}. Open Endpoints.`)
      this.modelChip.style.display = ''
      this.modelChip.textContent = state.model
      // The id is long and must not wrap: a wrapped chip is the layout shift
      // the row's single height exists to prevent.
      this.modelChip.title = state.model
      this.modelChip.setAttribute('aria-label', `Answers with the model ${state.model}. Open Roles.`)
      return
    }
    // An action rung. A rung with no destination (`unavailable`) is not a
    // control: a button that leads nowhere invites a click that does nothing,
    // which reads as the app being broken rather than the store unreadable.
    this._modelChipTargets = { endpoint: null, model: state.page }
    this.modelChip.disabled = state.page === null
    this.modelEndpointChip.style.display = 'none'
    this.modelChip.style.display = ''
    this.modelChip.textContent = state.text
    this.modelChip.title = state.text
    this.modelChip.setAttribute('aria-label', `${state.text}. Opens settings.`)
  }
```

```css
/* frontend/src/style.css, beside .nocx-editor-cwd */
.nocx-editor-model {
  cursor: pointer;
  max-width: 16rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
```

In `terminal-content.ts`, call `setModelChip` from the one place that already
knows both facts — the input-target switch and the `agent.status` read — so the
chip has a single writer.

- [ ] **Step 4: Run**

Run: `npx vitest run src/terminal-content.test.ts src/editor.test.ts && npx tsc --noEmit && npx eslint src --max-warnings=0`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/editor.ts frontend/src/terminal-content.ts frontend/src/style.css frontend/src/*.test.ts
git commit -m "feat(frontend): the composer names the model that will answer, and it is the way to change it (nocx-rikz5)"
```

---

### Task 7: The end-to-end check

**Files:**

- Create: `e2e/assistant-readiness.spec.ts`
- Modify: `e2e/harness.ts` (extend `bindEndpoint` with a "leave the model
  unchosen" mode, and add `setDefaultModel`)

**Interfaces:**

- Consumes: everything above, through the real backend and the real socket.
- **The fixtures already exist and are reused, not reinvented.**
  `e2e/agent-ask.spec.ts:67-73` is the pattern: `FakeOpenAI` from
  `e2e/fake-openai.ts` is a real local server that answers
  `/chat/completions`, and `bindEndpoint` / `settingsReady` from
  `e2e/harness.ts` configure an endpoint against it. Without `FakeOpenAI`
  there is no model to answer and "an answer arrives" cannot be made true by
  `cmd/devharness` alone.
- Produces: the epic's proof.

**Acceptance Criteria:**

- Runs against `cmd/devharness` on a disposable `$HOME`, in the container
  (`e2e/run-in-container.sh`).
- Watches the whole path: no endpoint → _Add an endpoint first_ → add one with
  a key, model unchosen → _Choose a model_, **never** _Ready_ → set the
  default from the chip's own destination → ask → the answer's text arrives.
- Asserts on observable state changes, never on a duration.
- Carries the composer-height assertion that jsdom cannot make (Task 6).

- [ ] **Step 1: Extend the harness**

`bindEndpoint` today configures an endpoint AND assigns the role, because
until now there was no way to have one without the other. Split it: a
`bindEndpoint(page, { assignRole: false })` mode that stops after the
endpoint, plus

```ts
/** Chooses the default model on Settings → Roles — the one choice the whole
 *  ladder exists to lead a person to. Separate from bindEndpoint because the
 *  point of the readiness spec is the state BETWEEN the two. */
export async function setDefaultModel(page: Page, model: string): Promise<void> {
  await page.getByRole('button', { name: 'Roles' }).click()
  const control = page.locator('.roles-default')
  await control.getByLabel('Model').selectOption(model)
  await expect(page.locator('.roles-default__state')).toContainText(model)
}
```

- [ ] **Step 2: Write the spec**

```ts
test('a person reaches a working assistant without discovering Roles unaided', async ({
  page,
  fake,
}) => {
  await page.goto(BASE_URL)
  await switchToAsk(page)

  const chip = page.locator('.nocx-editor-model')
  await expect(chip).toHaveText('Add an endpoint first')

  // The chip is the door, not a label: clicking it must land on Endpoints.
  await chip.click()
  await bindEndpoint(page, { baseURL: fake.url, model: 'm-a', assignRole: false })

  // THE ASSERTION THIS EPIC EXISTS FOR: an endpoint with a valid key is not
  // readiness. Before this work the line here read "Ready" and the refusal
  // arrived at the first question.
  await expect(chip).toHaveText('Choose a model')

  await chip.click()
  await setDefaultModel(page, 'm-a')

  // Ready means the model is NAMED, not that a box was ticked.
  await expect(page.locator('.nocx-editor-model').first()).toHaveText('openrouter')
  await expect(page.locator('.nocx-editor-model').last()).toHaveText('m-a')

  await askAQuestion(page, 'hello')
  // The answer's CONTENT, not merely a container becoming visible: an empty
  // answer block is exactly what a broken stream produces.
  await expect(page.locator('[data-answered-by]')).toContainText(fake.reply)
})

test('the chip does not change the composer height', async ({ page, fake }) => {
  // The layout claim jsdom cannot make (Task 6): a real engine measures here.
  await page.goto(BASE_URL)
  await bindEndpoint(page, { baseURL: fake.url, model: 'deepseek/deepseek-v4-flash-0731-preview' })
  const chrome = page.locator('.nocx-editor-chrome')
  const before = await chrome.boundingBox()
  await switchToAsk(page)
  await expect(page.locator('.nocx-editor-model')).toHaveCount(2)
  expect((await chrome.boundingBox())?.height).toBe(before?.height)
})
```

- [ ] **Step 3: Run it in the container**

```bash
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/assistant-readiness.spec.ts
```

Expected: PASS. A failure that is only red in the container is checked against
CI before being "fixed" — the container is Linux WebKit at a container
viewport and its failure set is not CI's. The height spec is exactly the kind
that can differ, so confirm it in CI before touching it.

- [ ] **Step 4: Commit**

```bash
git add e2e/assistant-readiness.spec.ts e2e/harness.ts
git commit -m "test(e2e): a person reaches a working assistant without finding Roles unaided (nocx-rikz5)"
```

---

## Task ordering

```
1 → 2 → 3 → 4 → 5
              ↘ 6
1..6 → 7
```

`bd dep add` edges: 2 blocks on 1; 3 blocks on 1; 4 blocks on 3; 5 blocks on 2;
6 blocks on 4; 7 blocks on 5 and 6. Tasks 5 and 6 are the parallel pair.

## Spec coverage

| Spec requirement                                                                      | Task                                                                     |
| ------------------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| §1 readiness is role resolvability; `agent.status` grows the role                     | 3                                                                        |
| §1 endpoint and credential become reasons, not the headline                           | 3 (credential scoped to the resolved endpoint)                           |
| §1 `agentStatusLine` inverts to role-first                                            | 4                                                                        |
| §2 the default as an input to the one resolver                                        | 1                                                                        |
| §2 the default is set at the top of the Roles page                                    | 5                                                                        |
| §2 per-role override, "As default" as the initial value                               | 5                                                                        |
| §3 the ladder's states, each with one fix location                                    | 4 (copy + precedence), 5 + 6 (surfaces)                                  |
| §4 the chip: Ask-only, provider → Endpoints, model → Roles, truncation                | 6                                                                        |
| §4 the chip stays current as the facts change                                         | 6 (`AgentReadiness`, refresh events, stale-reply ordering)               |
| §5 the green line says only what the controls cannot                                  | 5 (`roleStateLine` extended, returns null for silence)                   |
| §6 the end-to-end check                                                               | 7                                                                        |
| §6 one test per rung                                                                  | 4 (all six rungs, sentence AND target)                                   |
| §6 the interval: deleting a default's endpoint returns the ladder to "choose a model" | 1, Step 4b — **cleared inside `DeleteEndpoint`'s existing single write** |
| §6 contracts asserted over the socket                                                 | 2, 3                                                                     |
| §6 the composer's height is unchanged                                                 | 7 (a real engine measures; jsdom cannot)                                 |

## Corrections applied after review

Codex reviewed this plan against the code on 2026-08-21. Nine findings were
verified against the files and applied above. Two are worth naming because
they were the plan asserting something false about the codebase:

- The plan claimed `roles.assign` validates that the endpoint exists. It does
  not (`ws_roles.go:97`), and the store deliberately accepts a dangling
  assignment (`role_test.go:199`). Task 2 now decides the policy explicitly
  instead of inheriting one that was never there.
- The plan claimed `terminal-content.ts` already holds an `agent.status`
  read it could feed the chip from. It holds a per-refusal callback
  (`:1373`), not state. Task 6 now creates the owner, and without that the
  end-to-end path could not have passed.

Task 1 also contradicted this plan's own spec: it left a default pointing at
a deleted endpoint, while spec §6 requires the ladder to return to _choose a
model_. The coverage row above said "covered". Fixed in Step 4b.

**One finding is REJECTED, with evidence.** The review argued the default
violates `nocx-e6kn2` because "after asking there is no immutable model
attribution on the answer block", and that the closed decision must therefore
be reopened. That is false in two places: `content.RunFacts`
(`internal/content/ledger.go:703`) persists `EndpointID` and `Model` on the
run so that "a later endpoint change never reinterprets what the run used",
and `frontend/src/agent-ask.ts:241` sets `dataset.answeredBy = ask.model` on
the answer element, in a comment that cites `nocx-e6kn2` by name. The
attribution mechanism the review says is missing was built for this decision.

What survives of that finding is smaller and is filed separately rather than
smuggled in here: `data-answered-by` is an attribute, not visible text, so a
person can read which model answered only by inspecting the DOM. Whether the
answer block should SHOW it is a real question — and a display task, not a
reason to reopen a closed decision or to block this epic.
