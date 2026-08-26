# Agent policy UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** A person answers "stop asking me this" at the moment they are asked, and later sees and revokes every such answer on the Agent policy page.

**Architecture:** The approval prompt grows six answers (allow/deny × once/session/always). "Always" flips one row of the existing effect matrix; "this session" writes an in-memory per-session overlay that dies with the session. The backend applies both — the renderer never edits the matrix and never derives an effect from a tool name. The Agent policy page keeps the matrix's accepted shape, writes on change, and speaks product words.

**Tech Stack:** Go 1.x (`internal/content`, `internal/assistant`, `internal/transport`), SolidJS + TypeScript (`frontend/src`), JSON Schema contracts with generated TS types, Playwright e2e via `cmd/devharness`.

**Spec:** `.internal/specs/2026-08-21-agent-policy-ux-design.md` — read it before Task 1. It is binding; this plan is how it is built.

## Global Constraints

- **ADR-0020 §7 as amended (accepted 2026-08-16) is not reopened.** The matrix keeps its shape: one row per effect class, one decision per row, resource scopes per row, unstated fails toward asking.
- **ADR-0028 decision 4: no configuration path may express a rule over a tool NAME.** Every mechanism here writes an effect row. The renderer must never map a tool name to an effect — that is why Task 3 exists.
- **Fail toward asking.** No new zero value may mean permit. An absent session override is not a permit; an unparseable policy asks.
- **Egress asks keep two answers**, Allow / Deny, once only. The six answers are for `reason: "policy"` only.
- **Contracts (`AGENTS.md` rule 5).** Every schema touched gets `additionalProperties: false` AND an explicit `required`, the generated TS regenerated and committed (`cd frontend && npm run contracts`), and an `_OverTheWireConformsToContract` test asserting the REAL result off the REAL socket.
- **Kit first (`frontend/src/ui/README.md`).** Read it and list `frontend/src/ui/` before adding any control. A surface may place a kit component and may never repaint it.
- **Commit messages** follow `AGENTS.md`: `<type>(<scope>): <subject> (<bead-id>)` with a prose body saying what was wrong and why this way.
- **A worker runs the unit tests for the files it changed and stops there.** The coordinator runs `make ci-full` once on the merged tree.

---

## File Structure

| File                                                                                                   | Responsibility                                                                                              |
| ------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------- |
| `internal/content/effectpolicy.go`                                                                     | `SessionOverrides`, `ResolvePolicy`'s third layer. The ONE place the resolution order is stated.            |
| `internal/transport/ws_sessionpolicy.go` (new)                                                         | The per-session override store and its teardown. One file so the teardown and the store cannot drift apart. |
| `internal/transport/ws.go`                                                                             | The three session-teardown call sites drop the overrides.                                                   |
| `internal/transport/ws_readscreen.go`                                                                  | `runGrantFor` passes the session's overrides.                                                               |
| `internal/assistant/policy.go`                                                                         | `ApprovalRequest` carries `Effect` and `Resource`; `request()` fills them.                                  |
| `internal/transport/ws_agent.go`                                                                       | The notification and `approveParams` carry the new fields; `handleApprove` applies the scope.               |
| `internal/transport/ws_policy.go`                                                                      | `policy.get` reports `live`.                                                                                |
| `contracts/agent.approvalRequested.schema.json`, `agent.approve.schema.json`, `policy.get.schema.json` | The wire, declared once.                                                                                    |
| `frontend/src/agent-approval-prompt.tsx`                                                               | Six answers, product words, egress still two.                                                               |
| `frontend/src/main.tsx`                                                                                | `decideApproval` carries the scope.                                                                         |
| `frontend/src/agent-policy-section.tsx`                                                                | Write-on-change, product labels, live rows.                                                                 |
| `frontend/src/policy-client.ts`                                                                        | `live` on the adapted shape.                                                                                |
| `e2e/agent-policy.spec.ts` (new)                                                                       | The happy path.                                                                                             |

---

### Task 1: The session layer in the resolver

**Files:**

- Modify: `internal/content/effectpolicy.go:291-303` (`ResolvePolicy`)
- Test: `internal/content/effectpolicy_test.go`

**Interfaces:**

- Consumes: nothing from earlier tasks.
- Produces: `content.SessionOverrides` (`map[content.Effect]content.Decision`); `content.ResolvePolicy(global EffectPolicy, workspace *EffectPolicy, session SessionOverrides) EffectPolicy`; `content.EffectPolicy.SetRowDecision(e Effect, d Decision) EffectPolicy` (Task 4 consumes it).

**Acceptance Criteria:**

- A session override for one effect changes that row's decision and no other row.
- A session override does NOT touch the row's scopes — the resolved row keeps the scopes it had.
- An empty or nil `SessionOverrides` resolves exactly as the two-argument form did.
- An override carrying a decision outside the enum is IGNORED, not applied — an invalid value must never be able to widen a row.
- `SetRowDecision` replaces one row's decision, keeps that row's scopes, leaves the other six rows untouched, and ignores a decision outside the enum.
- **The tests that pin the accepted model still pass**: the preset-as-matrix equivalences, `ParseEffectPolicy` rejecting a tool name as a row key and a tool-kind scope, and `PermittedEffects` dropping a refused effect. Run them explicitly in step 5.
- The workspace override still replaces the global wholesale, and the session overlay applies ON TOP of whichever won.

- [ ] **Step 1: Write the failing tests**

Append to `internal/content/effectpolicy_test.go`:

```go
func TestResolvePolicy_SessionOverrideChangesOneRowOnly(t *testing.T) {
	global := EffectPolicy{
		Observe:           EffectRow{Decision: DecisionAsk, Scopes: []GrantScope{{Kind: ResourcePath, ID: "/w"}}},
		MutateDestructive: EffectRow{Decision: DecisionAsk},
	}
	got := ResolvePolicy(global, nil, SessionOverrides{EffectObserve: DecisionPermit})

	if d := got.DecisionFor(EffectObserve); d != DecisionPermit {
		t.Fatalf("observe: got %q, want permit", d)
	}
	if d := got.DecisionFor(EffectMutateDestructive); d != DecisionAsk {
		t.Fatalf("mutate-destructive: got %q, want ask (untouched)", d)
	}
	// The overlay decides; it never re-scopes.
	scopes := got.RowScopes(EffectObserve)
	if len(scopes) != 1 || scopes[0].ID != "/w" {
		t.Fatalf("observe scopes: got %+v, want the global's [/w]", scopes)
	}
}

func TestResolvePolicy_NoSessionOverridesIsTheOldBehaviour(t *testing.T) {
	global := EffectPolicy{Observe: EffectRow{Decision: DecisionPermit}}
	ws := &EffectPolicy{Observe: EffectRow{Decision: DecisionRefuse}}

	if d := ResolvePolicy(global, nil, nil).DecisionFor(EffectObserve); d != DecisionPermit {
		t.Fatalf("nil overrides, no workspace: got %q, want permit", d)
	}
	if d := ResolvePolicy(global, ws, SessionOverrides{}).DecisionFor(EffectObserve); d != DecisionRefuse {
		t.Fatalf("empty overrides, workspace wins: got %q, want refuse", d)
	}
}

func TestResolvePolicy_SessionOverlaysOnTopOfTheWorkspace(t *testing.T) {
	global := EffectPolicy{Observe: EffectRow{Decision: DecisionRefuse}}
	ws := &EffectPolicy{Observe: EffectRow{Decision: DecisionAsk}}

	got := ResolvePolicy(global, ws, SessionOverrides{EffectObserve: DecisionPermit})
	if d := got.DecisionFor(EffectObserve); d != DecisionPermit {
		t.Fatalf("session over workspace: got %q, want permit", d)
	}
}

func TestSetRowDecision_ReplacesOneDecisionAndKeepsItsScopes(t *testing.T) {
	p := EffectPolicy{
		Observe:           EffectRow{Decision: DecisionAsk, Scopes: []GrantScope{{Kind: ResourcePath, ID: "/w"}}},
		MutateDestructive: EffectRow{Decision: DecisionAsk},
	}
	got := p.SetRowDecision(EffectObserve, DecisionPermit)

	if d := got.DecisionFor(EffectObserve); d != DecisionPermit {
		t.Fatalf("observe: got %q, want permit", d)
	}
	if sc := got.RowScopes(EffectObserve); len(sc) != 1 || sc[0].ID != "/w" {
		t.Fatalf("observe scopes: got %+v, want [/w] kept", sc)
	}
	if d := got.DecisionFor(EffectMutateDestructive); d != DecisionAsk {
		t.Fatalf("mutate-destructive: got %q, want the untouched ask", d)
	}
	if d := p.DecisionFor(EffectObserve); d != DecisionAsk {
		t.Fatal("SetRowDecision mutated its receiver; it must return a copy")
	}
}

func TestSetRowDecision_IgnoresADecisionOutsideTheEnum(t *testing.T) {
	p := EffectPolicy{Observe: EffectRow{Decision: DecisionAsk}}
	if d := p.SetRowDecision(EffectObserve, Decision("maybe")).DecisionFor(EffectObserve); d != DecisionAsk {
		t.Fatalf("got %q, want the untouched ask", d)
	}
}

func TestResolvePolicy_InvalidSessionDecisionIsIgnored(t *testing.T) {
	// Fail toward asking: a value outside the enum must never widen a row.
	global := EffectPolicy{Observe: EffectRow{Decision: DecisionAsk}}
	got := ResolvePolicy(global, nil, SessionOverrides{EffectObserve: Decision("yes-please")})
	if d := got.DecisionFor(EffectObserve); d != DecisionAsk {
		t.Fatalf("invalid override: got %q, want the untouched ask", d)
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

```bash
go test ./internal/content/ -run TestResolvePolicy -v
```

Expected: compile failure — `undefined: SessionOverrides`, and `ResolvePolicy` called with 3 arguments.

- [ ] **Step 3: Implement**

Replace `ResolvePolicy` in `internal/content/effectpolicy.go`:

```go
// SessionOverrides is the per-session overlay: what a person answered "in
// this session" to, one decision per effect class. It is NOT a third matrix
// — it is produced by clicks rather than authored, so it carries no scopes
// and has no notion of an unstated row. An effect absent from the map is
// not an answer, and therefore never a permit.
type SessionOverrides map[Effect]Decision

// ResolvePolicy is the ONE place the resolution order is stated (ADR-0020 §7
// as amended): the session overlay over the workspace policy over the global
// default. The workspace, when one exists, REPLACES the global wholesale; the
// session overlay then applies per row on top of whichever won, changing that
// row's decision and nothing else — never its scopes, which the overlay has
// no way to express. Today there is no workspace grant source — nocx-mp2vd
// owns that seam — so callers pass nil and the global resolves.
//
// An override whose decision is outside the enum is ignored rather than
// applied: this function fails toward asking like every other layer, and an
// invalid value must never be able to widen a row.
func ResolvePolicy(global EffectPolicy, workspace *EffectPolicy, session SessionOverrides) EffectPolicy {
	out := global
	if workspace != nil {
		out = *workspace
	}
	for _, e := range []Effect{
		EffectObserve, EffectMutateReversible, EffectMutateDestructive,
		EffectPrivilegeChange, EffectDisclose, EffectCrossBoundary, EffectDelegate,
	} {
		d, ok := session[e]
		if !ok || !d.valid() {
			continue
		}
		row := out.rowFor(e)
		row.Decision = d
		out.setRow(e, row)
	}
	return out
}

// SetRowDecision returns a copy of the matrix with ONE row's decision
// replaced, keeping that row's scopes. It is how a standing answer from the
// approval prompt is applied (the transport calls it inside the policy
// store's write), and the only exported mutator — a caller reaching into the
// struct fields would be a second place that knows the lattice's shape.
//
// A decision outside the enum is ignored: every layer here fails toward
// asking, and an invalid value must never widen a row.
func (p EffectPolicy) SetRowDecision(e Effect, d Decision) EffectPolicy {
	if !d.valid() {
		return p
	}
	row := p.rowFor(e)
	row.Decision = d
	out := p
	out.setRow(e, row)
	return out
}

// setRow writes one row by effect — the inverse of rowFor, and the only
// mutator on the matrix. Kept beside it so the two switches cannot drift.
func (p *EffectPolicy) setRow(e Effect, row EffectRow) {
	switch e {
	case EffectObserve:
		p.Observe = row
	case EffectMutateReversible:
		p.MutateReversible = row
	case EffectMutateDestructive:
		p.MutateDestructive = row
	case EffectPrivilegeChange:
		p.PrivilegeChange = row
	case EffectDisclose:
		p.Disclose = row
	case EffectCrossBoundary:
		p.CrossBoundary = row
	case EffectDelegate:
		p.Delegate = row
	}
}
```

- [ ] **Step 4: Fix the one existing caller**

`internal/transport/ws_readscreen.go:238` currently reads:

```go
	p := content.ResolvePolicy(s.agentPolicy.Policy(), nil)
```

Change to (Task 2 replaces the `nil` with the real overlay):

```go
	p := content.ResolvePolicy(s.agentPolicy.Policy(), nil, nil)
```

- [ ] **Step 5: Run the tests and the package**

```bash
go test ./internal/content/ -run 'TestResolvePolicy|TestSetRowDecision' -count=1 -v
# The accepted model, still pinned — these must not have moved:
go test ./internal/content/ -run 'Preset|ParseEffectPolicy|PermittedEffects' -count=1 -v
go test ./internal/content/ ./internal/transport/ -count=1
go build ./...
```

Expected: PASS everywhere, and the build clean. If a preset equivalence broke, the overlay is touching a row it must not.

- [ ] **Step 6: Commit**

```bash
git add internal/content/effectpolicy.go internal/content/effectpolicy_test.go internal/transport/ws_readscreen.go
git commit -m "feat(content): the resolver gains a per-session overlay (<bead-id>)"
```

---

### Task 2: The per-session store, and dropping it at every teardown

**Files:**

- Create: `internal/transport/ws_sessionpolicy.go`
- Create: `internal/transport/ws_sessionpolicy_test.go`
- Modify: `internal/transport/ws.go` (the struct's field; the three teardown sites at ~1221, ~2380, ~2503)
- Modify: `internal/transport/ws_readscreen.go:238` (`runGrantFor` reads the overlay)

**Interfaces:**

- Consumes: `content.SessionOverrides` (Task 1).
- Produces: `sessionPolicyStore` with `Set(sid session.ID, e content.Effect, d content.Decision)`, `For(sid session.ID) content.SessionOverrides`, `Drop(sid session.ID)`.

**Acceptance Criteria:**

- An override set for one session is returned for that session and NOT for another.
- `Drop` removes every override of that session; `For` then returns an empty overlay, and a subsequent run of that session asks again.
- **Every one of the three teardown sites drops the overrides.** Asserted per site, because one missed site is a permission that outlives its session.
- `runGrantFor` resolves with the session's overlay.
- The store is safe under concurrent use (`-race`).
- A question still on screen when the session ends leaves no overlay behind — the spec's claim, asserted rather than assumed.

**Why a new file rather than a field on an existing store:** `ApprovalStore` is process-lifetime and keyed by the exact proposal; it is not per-session and cannot host this. Keeping the store and its teardown in one file is what stops the drop from being forgotten when a fourth teardown path appears.

- [ ] **Step 1: Write the failing tests**

Create `internal/transport/ws_sessionpolicy_test.go`:

```go
package transport

import (
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

func TestSessionPolicyStore_ScopedToOneSession(t *testing.T) {
	s := newSessionPolicyStore()
	s.Set(session.ID("a"), content.EffectObserve, content.DecisionPermit)

	if got := s.For(session.ID("a"))[content.EffectObserve]; got != content.DecisionPermit {
		t.Fatalf("session a: got %q, want permit", got)
	}
	if _, ok := s.For(session.ID("b"))[content.EffectObserve]; ok {
		t.Fatal("session b saw session a's answer")
	}
}

func TestSessionPolicyStore_DropClearsTheSession(t *testing.T) {
	s := newSessionPolicyStore()
	s.Set(session.ID("a"), content.EffectObserve, content.DecisionPermit)
	s.Set(session.ID("a"), content.EffectMutateDestructive, content.DecisionRefuse)
	s.Drop(session.ID("a"))

	if got := s.For(session.ID("a")); len(got) != 0 {
		t.Fatalf("after drop: got %+v, want empty", got)
	}
}

func TestSessionPolicyStore_ConcurrentUse(t *testing.T) {
	s := newSessionPolicyStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); s.Set(session.ID("a"), content.EffectObserve, content.DecisionPermit) }()
		go func() { defer wg.Done(); _ = s.For(session.ID("a")) }()
		go func() { defer wg.Done(); s.Drop(session.ID("a")) }()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run and watch it fail**

```bash
go test ./internal/transport/ -run TestSessionPolicyStore -v
```

Expected: `undefined: newSessionPolicyStore`.

- [ ] **Step 3: Implement the store**

Create `internal/transport/ws_sessionpolicy.go`:

```go
package transport

// The per-session policy overlay — what a person answered "allow in this
// session" (or "deny in this session") to, and where it dies.
//
// This is the first per-session store on the assistant path. ApprovalStore
// is process-lifetime and keyed by the exact proposal, so it cannot host
// this: an answer that covers the NEXT proposal is a different fact from an
// answer that covered one.
//
// The store and the drop live in one file on purpose. The permission's whole
// promise is that it does not outlive its session, and that promise is kept
// by a call at every teardown path — three of them today. A store defined
// somewhere else is a store whose drop gets forgotten when a fourth appears.

import (
	"sync"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

type sessionPolicyStore struct {
	mu sync.RWMutex
	by map[session.ID]content.SessionOverrides
}

func newSessionPolicyStore() *sessionPolicyStore {
	return &sessionPolicyStore{by: make(map[session.ID]content.SessionOverrides)}
}

// Set records one answer for one session.
func (s *sessionPolicyStore) Set(sid session.ID, e content.Effect, d content.Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by[sid] == nil {
		s.by[sid] = make(content.SessionOverrides, 1)
	}
	s.by[sid][e] = d
}

// For returns a COPY of one session's overlay. A copy because the resolver
// reads it without the lock, and a map handed out under a read lock is a map
// read after the lock is gone.
func (s *sessionPolicyStore) For(sid session.ID) content.SessionOverrides {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.by[sid]
	out := make(content.SessionOverrides, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Drop forgets every answer of one session. Called from every session
// teardown path; see the file comment.
func (s *sessionPolicyStore) Drop(sid session.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.by, sid)
}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./internal/transport/ -run TestSessionPolicyStore -race -count=1 -v
```

Expected: PASS, no race.

- [ ] **Step 5: Wire it into the server**

In `internal/transport/ws.go`, add the field to the `WSServer` struct beside the other assistant state:

```go
	// sessionPolicy holds each session's "allow in this session" answers.
	// Dropped at every session teardown (ws_sessionpolicy.go).
	sessionPolicy *sessionPolicyStore
```

and initialise it where the server is constructed, beside `NewApprovalStore()`:

```go
	s.sessionPolicy = newSessionPolicyStore()
```

Then at EACH of the three sites that call `s.gitSessionClosed(...)` — `ws.go:1221`, `ws.go:2380`, `ws.go:2503` — add the drop immediately before the existing call, with the session id that site already has:

```go
	s.sessionPolicy.Drop(sess.ID()) // ws.go:1221 and ws.go:2380 — they hold `sess`
	s.gitSessionClosed(sess.ID(), nil)
```

```go
	s.sessionPolicy.Drop(sid) // ws.go:2503 — this site holds `sid`
	s.gitSessionClosed(sid, wconn)
```

> Read each site before editing: the variable naming differs and 1221 passes a nil conn. Do not "tidy" the three into one helper in this task — that is a refactor with its own review, and this task's job is that no path is missed.

- [ ] **Step 6: `runGrantFor` reads the overlay**

In `internal/transport/ws_readscreen.go`, replace the body of `runGrantFor`:

```go
func (s *WSServer) runGrantFor(sessionID string) *content.Grant {
	if s.agentPolicy == nil {
		return nil
	}
	// The session's own answers overlay the global policy — an "allow in
	// this session" is in force from the answer until the session ends, and
	// the store is what ends it.
	overrides := s.sessionPolicy.For(session.ID(sessionID))
	p := content.ResolvePolicy(s.agentPolicy.Policy(), nil, overrides)
	g := p.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: sessionID}})
	return &g
}
```

Add the `session` import if it is not already there.

- [ ] **Step 7: Write the teardown test, one assertion per site**

Append to `internal/transport/ws_sessionpolicy_test.go` a test per teardown path. Follow the harness the existing `ws_test.go` session-close tests use — find them with:

```bash
grep -n "gitSessionClosed" internal/transport/*_test.go
```

Each test drives ONE teardown path against a real server and asserts the overlay is gone. The shared body is the same three moves; only the path differs:

```go
// assertDroppedBy stands a server up with one live session, records an
// override for it, runs `teardown`, and fails unless the overlay is empty.
// One helper so the three paths are compared like for like — a path that
// drops it by luck rather than by a call would still pass one of these
// individually, and would differ here.
func assertDroppedBy(t *testing.T, teardown func(s *WSServer, sid session.ID)) {
	t.Helper()
	s, sid := serverWithLiveSession(t) // the harness the other ws_test session tests use
	s.sessionPolicy.Set(sid, content.EffectObserve, content.DecisionPermit)
	if got := s.sessionPolicy.For(sid)[content.EffectObserve]; got != content.DecisionPermit {
		t.Fatalf("precondition: got %q, want permit before teardown", got)
	}

	teardown(s, sid)

	if got := s.sessionPolicy.For(sid); len(got) != 0 {
		t.Fatalf("after teardown: got %+v, want empty — this path leaks the permission", got)
	}
}

func TestSessionPolicy_DroppedOnConnectionLoss(t *testing.T) {
	// ws.go:1221 — the conn is gone, so the drop must not depend on one.
	assertDroppedBy(t, func(s *WSServer, sid session.ID) { s.dropSessionOnConnectionLoss(sid) })
}

func TestSessionPolicy_DroppedOnSessionClose(t *testing.T) {
	// ws.go:2380 — the ordinary close, with a live conn to notify.
	assertDroppedBy(t, func(s *WSServer, sid session.ID) { s.closeSession(sid) })
}

func TestSessionPolicy_DroppedOnExplicitTeardown(t *testing.T) {
	// ws.go:2503 — the explicit teardown request.
	assertDroppedBy(t, func(s *WSServer, sid session.ID) { s.tearDownSession(sid) })
}

func TestSessionPolicy_PendingAskDiesWithItsSession(t *testing.T) {
	// The spec's claim, asserted rather than assumed: a question still on
	// screen when the session ends does not leave an overlay behind that a
	// later session of the same id could inherit.
	s, sid := serverWithLiveSession(t)
	s.sessionPolicy.Set(sid, content.EffectObserve, content.DecisionPermit)
	s.closeSession(sid)
	if got := s.sessionPolicy.For(sid); len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}
```

> `serverWithLiveSession`, `dropSessionOnConnectionLoss`, `closeSession` and `tearDownSession` are placeholders for whatever the three sites and the existing harness are actually called — **read them first**:
>
> ```bash
> sed -n '1210,1230p;2370,2390p;2495,2510p' internal/transport/ws.go
> grep -n "func.*testing.T.*WSServer\|newTestServer\|startTestSession" internal/transport/ws_test.go | head
> ```
>
> Name each test function after the real path. What must not change is the shape: one helper, three paths, one assertion each.

- [ ] **Step 8: Run**

```bash
go test ./internal/transport/ -run 'TestSessionPolicy' -race -count=1 -v
go build ./...
```

Expected: PASS on all three paths.

- [ ] **Step 9: Commit**

```bash
git add internal/transport/ws_sessionpolicy.go internal/transport/ws_sessionpolicy_test.go internal/transport/ws.go internal/transport/ws_readscreen.go
git commit -m "feat(transport): an allow lives in one session, and dies on every path that ends one (<bead-id>)"
```

---

### Task 3: The prompt is told the effect and the resource

**Files:**

- Modify: `contracts/agent.approvalRequested.schema.json`
- Modify: `internal/assistant/policy.go:82-98` (`ApprovalRequest`), `:599-607` (`request`)
- Modify: `internal/transport/ws_agent.go:217-227` (`agentApprovalRequested`), `:808-813` (the fill)
- Regenerate: `frontend/src/generated/agent.approvalRequested.ts`
- Test: `internal/transport/ws_agent_test.go` (over-the-wire conformance)

**Interfaces:**

- Consumes: nothing from earlier tasks.
- Produces: `AgentApprovalRequested.effect: Effect` and `.resource: {kind, id} | null` on the wire; `assistant.ApprovalRequest.Effect content.Effect` and `.Resource *content.GrantScope`.

**Acceptance Criteria:**

- A policy ask carries the effect class the gate decided on, and the resource it matched against (null when the call named none).
- An egress ask carries the same fields, filled from the proposal's declaration — the surface ignores them there, but the wire is not two shapes.
- The renderer never needs the tool name to know the effect. Asserted by the schema requiring `effect`.
- `_OverTheWireConformsToContract` validates the real notification off the real socket, not a payload the test built.

**Why this is not optional:** without it the renderer maps `readScreen → observe` to decide which row "always" writes, which is a rule keyed by a tool name in everything but storage — the thing ADR-0028 decision 4 forbids.

- [ ] **Step 1: Extend the schema**

In `contracts/agent.approvalRequested.schema.json`, add to `required`: `"effect"`. Add to `properties`:

```json
    "effect": {
      "description": "The effect class the policy gate decided on — the row a standing answer writes. Sent by the backend because the renderer must never derive an effect from a tool name (ADR-0028 decision 4).",
      "type": "string",
      "enum": [
        "observe",
        "mutate-reversible",
        "mutate-destructive",
        "privilege-change",
        "disclose",
        "cross-boundary",
        "delegate"
      ]
    },
    "resource": {
      "description": "The resource the gate matched the call against, or null when the call named none. A fact for the person reading the question; a standing answer is over the effect, never over this.",
      "type": ["object", "null"],
      "additionalProperties": false,
      "required": ["kind", "id"],
      "properties": {
        "kind": {
          "description": "The resource kind, from the ledger's closed set.",
          "type": "string",
          "enum": ["path", "session", "environment", "credential", "destination"]
        },
        "id": { "description": "The resource's id.", "type": "string", "minLength": 1 }
      }
    }
```

- [ ] **Step 2: Regenerate the TS type and watch the check fail first**

```bash
cd frontend && npm run contracts:check
```

Expected: FAIL — the committed generated file no longer matches the schema. Then:

```bash
cd frontend && npm run contracts && cd .. && git diff --stat frontend/src/generated/
```

Expected: `agent.approvalRequested.ts` changed.

- [ ] **Step 3: Carry the fields on the Go side**

In `internal/assistant/policy.go`, add to `ApprovalRequest` (after `ArgHash`):

```go
	// Effect is the effect class the gate decided on — what a standing
	// answer writes a row for. Sent rather than derived, because deriving
	// it in the renderer would be a rule keyed by a tool name (ADR-0028
	// decision 4).
	Effect content.Effect `json:"effect"`
	// Resource is what the gate matched the call against, or nil when the
	// call named none. Shown to the person; never what an answer is over.
	Resource *content.GrantScope `json:"resource,omitempty"`
```

`request()` gains the declaration it already has at every call site. Change its signature and both callers:

```go
func (m *policyMiddleware) request(decl agenttools.Tool, callID, rawArgs string, args map[string]any) *ApprovalRequest {
	return &ApprovalRequest{
		RunID:     m.runID,
		Attempt:   m.attempt,
		Tool:      decl.Name,
		CallID:    callID,
		Arguments: rawArgs,
		Effect:    decl.Effect,
		Resource:  m.matchedResource(decl, args),
	}
}

// matchedResource is the resource the call named, in the declaration's own
// terms — the same argument `inScope` checks. Nil when the call named none.
func (m *policyMiddleware) matchedResource(decl agenttools.Tool, args map[string]any) *content.GrantScope {
	if decl.ResourceArg == "" || len(decl.Resources) == 0 {
		return nil
	}
	v, ok := args[decl.ResourceArg].(string)
	if !ok || v == "" {
		return nil
	}
	return &content.GrantScope{Kind: decl.Resources[0], ID: v}
}
```

> Read `inScope` (`policy.go:493`) before writing `matchedResource` — it already answers "which argument names the resource". If the two derivations can be one, make them one: two answers to that question is the defect `AGENTS.md` names. If they genuinely cannot, say in a comment why.

**There are TWO escalation sites, not one.** `args` is in scope at both — `m.validate` produces it at `policy.go:250` and `m.decide` consumes it at `:256`:

- `policy.go:268`, the policy arm: `m.request(decl.Name, tCtx.CallID, rawArgs)` becomes `m.request(decl, tCtx.CallID, rawArgs, args)`.
- `policy.go:295`, the CLASSIFIER arm: the `*ApprovalRequest` comes from the classifier gate, not from `request()`. Find where it is built (`grep -n "classifierAsk\|func.*ApprovalRequest" internal/assistant/classifier.go`) and fill `Effect` and `Resource` there the same way. A classifier escalation that reaches the surface without an effect is a prompt that cannot offer "always" — and the schema now requires the field, so it is a hard failure rather than a quiet one.

- [ ] **Step 4: Carry them on the wire**

In `internal/transport/ws_agent.go`, add to `agentApprovalRequested`:

```go
	Effect   string              `json:"effect"`
	Resource *content.GrantScope `json:"resource,omitempty"`
```

and in `suspendForApproval`, fill them in both arms (the egress arm reads them off the proposal's declaration the same way — a single shape on the wire).

- [ ] **Step 5: Write the over-the-wire conformance test**

Find the existing pattern:

```bash
grep -rn "OverTheWireConformsToContract" internal/transport/*_test.go | head -3
```

Add `TestAgentApprovalRequested_OverTheWireConformsToContract`: drive a real ask that escalates, capture the real notification off the socket, validate it against `contracts/agent.approvalRequested.schema.json`, and assert `effect` is `"observe"` for a `readScreen` escalation.

- [ ] **Step 6: Run**

```bash
go test ./internal/assistant/ ./internal/transport/ -count=1
cd frontend && npm run contracts:check && npx tsc --noEmit -p tsconfig.json
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add contracts/agent.approvalRequested.schema.json internal/assistant/policy.go internal/transport/ws_agent.go frontend/src/generated/agent.approvalRequested.ts internal/transport/ws_agent_test.go internal/assistant/policy_test.go
git commit -m "feat(transport): the approval question names its effect, so the answer need not guess it (<bead-id>)"
```

---

### Task 4: `agent.approve` grows a scope, and the backend applies it

**Files:**

- Modify: `contracts/agent.approve.schema.json`
- Modify: `internal/transport/ws_agent.go:229-239` (`approveParams`), `handleApprove`, `suspendForApproval`
- Modify: `internal/assistant/approvals.go` — `Approval` gains `Effect content.Effect`, so the answer knows which row it is about
- Regenerate: `frontend/src/generated/agent.approve.ts`
- Test: `internal/transport/ws_agent_test.go`

**Interfaces:**

- Consumes: `sessionPolicyStore` (Task 2), `content.EffectPolicy.SetRowDecision` (Task 1), `ApprovalRequest.Effect` (Task 3).
- Produces: `AgentApprove.scope: 'once' | 'session' | 'always'`; `assistant.Approval.Effect`; `agentApproveResponse.Warning string` (empty when the standing part was recorded).

**Acceptance Criteria:**

- `scope: "once"` behaves exactly as today — nothing is written anywhere.
- `scope: "session"` with `approved: true` sets the run's session's overlay for that effect to `permit`; with `approved: false`, to `refuse`. The next run of that session is not asked.
- `scope: "always"` with `approved: true` writes `permit` into the global policy's row for that effect, through `GlobalPolicy.SetPolicy` — inside the store, never by the renderer.
- `scope: "always"` with `approved: false` writes `refuse`.
- **A failed policy write does not lose the call.** The approval still resumes the run; the standing part is reported as not saved in the result. Asserted.
- An **egress** ask accepts only `scope: "once"`; anything else is `-32602`. Asserted by trying.
- A missing `scope` is rejected: the schema requires it, and the wire is the contract.

- [ ] **Step 1: Extend the schema**

In `contracts/agent.approve.schema.json`, add `"scope"` to `required` and:

```json
    "scope": {
      "description": "How far the answer reaches: this proposal only, every call of the same effect in this terminal session, or the standing policy. 'session' and 'always' are refused for an egress question — 'always send secrets to the provider' is not a standing decision.",
      "type": "string",
      "enum": ["once", "session", "always"]
    }
```

- [ ] **Step 2: Write the failing tests**

In `internal/transport/ws_agent_test.go`, on the existing approve-flow harness — find it first:

```bash
grep -n "handleApprove\|\"agent.approve\"" internal/transport/ws_agent_test.go | head
```

`suspendedRun(t)` below stands for whatever that harness is called: it must leave a run suspended on a `readScreen` (effect `observe`) policy ask in a known session, and hand back the binding to answer with.

```go
func TestApprove_ScopeOnce_WritesNothing(t *testing.T) {
	h, b := suspendedRun(t)
	approve(t, h, b, true, "once")

	if got := h.sessionPolicy.For(b.SessionID); len(got) != 0 {
		t.Fatalf("session overlay: got %+v, want empty", got)
	}
	if d := h.globalPolicy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("global policy: got %q, want the untouched ask", d)
	}
}

func TestApprove_ScopeSession_PermitsTheEffectForThatSessionOnly(t *testing.T) {
	h, b := suspendedRun(t)
	approve(t, h, b, true, "session")

	if got := h.sessionPolicy.For(b.SessionID)[content.EffectObserve]; got != content.DecisionPermit {
		t.Fatalf("this session: got %q, want permit", got)
	}
	if _, ok := h.sessionPolicy.For(session.ID("some-other"))[content.EffectObserve]; ok {
		t.Fatal("another session inherited the answer")
	}
	// A session answer is not a standing one.
	if d := h.globalPolicy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionAsk {
		t.Fatalf("global policy: got %q, want the untouched ask", d)
	}
}

func TestApprove_ScopeAlways_WritesTheRowIntoTheGlobalPolicy(t *testing.T) {
	h, b := suspendedRun(t)
	approve(t, h, b, true, "always")

	if d := h.globalPolicy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionPermit {
		t.Fatalf("observe: got %q, want permit", d)
	}
	// One row, not the matrix.
	if d := h.globalPolicy.Policy().DecisionFor(content.EffectMutateDestructive); d != content.DecisionAsk {
		t.Fatalf("mutate-destructive: got %q, want the untouched ask", d)
	}
}

func TestApprove_ScopeAlways_DenyWritesRefuse(t *testing.T) {
	h, b := suspendedRun(t)
	approve(t, h, b, false, "always")

	if d := h.globalPolicy.Policy().DecisionFor(content.EffectObserve); d != content.DecisionRefuse {
		t.Fatalf("observe: got %q, want refuse", d)
	}
}

func TestApprove_ScopeAlways_PolicyWriteFailureStillResumesTheRun(t *testing.T) {
	// The person said yes. A store that cannot record the STANDING part must
	// not cost them the call they answered.
	h, b := suspendedRun(t)
	h.globalPolicy = failingPolicyStore{err: errors.New("disk is full")}

	res := approve(t, h, b, true, "always")

	if res.State == "declined" {
		t.Fatal("the run was declined because a write failed; it must resume")
	}
	if res.Warning == "" || !strings.Contains(res.Warning, "disk is full") {
		t.Fatalf("warning: got %q, want it to name the failure", res.Warning)
	}
}

func TestApprove_EgressRefusesAnythingButOnce(t *testing.T) {
	// "Always send secrets to the provider" is not a standing decision.
	h, b := suspendedEgressRun(t)
	for _, scope := range []string{"session", "always"} {
		err := approveExpectingError(t, h, b, true, scope)
		if err == nil || err.Code != -32602 {
			t.Fatalf("scope %q: got %v, want -32602", scope, err)
		}
	}
	if res := approve(t, h, b, true, "once"); res.State == "" {
		t.Fatal("once must still be accepted for an egress question")
	}
}
```

`failingPolicyStore` is a two-method stub over `assistant.GlobalPolicy` whose `SetPolicy` always errors — the failure-path rule in `AGENTS.md` ("for every external call your code makes, there is a test where that call fails").

- [ ] **Step 3: Run and watch them fail**

```bash
go test ./internal/transport/ -run TestApprove_Scope -v
```

Expected: `unknown field "scope"` / compile failure.

- [ ] **Step 4: Implement**

`approveParams` gains:

```go
	Scope string `json:"scope"` // "once" | "session" | "always"
```

In `handleApprove`, after the binding is validated and BEFORE the resume, apply the standing part:

```go
// applyStandingAnswer records the part of the decision that outlives this
// proposal. It returns the sentence to report when it could not be recorded,
// and empty when there was nothing to record or it was recorded.
//
// A failure here never refuses the call: the person said yes, and punishing
// them for a store problem is the wrong end to fail toward. The run resumes
// and the result says the standing part did not stick.
func (h agentHandlers) applyStandingAnswer(p approveParams, pending assistant.Approval, sid session.ID) string {
	if p.Scope == "once" {
		return ""
	}
	d := content.DecisionRefuse
	if p.Approved {
		d = content.DecisionPermit
	}
	if p.Scope == "session" {
		h.sessionPolicy.Set(sid, pending.Effect, d)
		return ""
	}
	next := h.globalPolicy.Policy().SetRowDecision(pending.Effect, d)
	if err := h.globalPolicy.SetPolicy(next); err != nil {
		return "the decision was applied to this call, but could not be saved as a standing answer: " + err.Error()
	}
	return ""
}
```

> `SetRowDecision` comes from Task 1 and keeps the row's scopes — a standing answer changes the decision, never the bound. The pending `Approval` must carry the `Effect`: add `Effect content.Effect` to `assistant.Approval` and fill it in `suspendForApproval`, where the `ApprovalRequest` already has it from Task 3. Do NOT add it to `approvalKey` — the key identifies a proposal, and two proposals differing only by effect cannot exist.

The egress refusal, before anything else in `handleApprove`:

```go
	if reason == "egress" && p.Scope != "once" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "an egress decision covers this result only"})
		return
	}
```

Add the sentence to `agentApproveResponse` as an optional `warning` field, and extend `contracts/agent.approve` result schema accordingly if one exists.

- [ ] **Step 5: Run**

```bash
go test ./internal/transport/ -run TestApprove -race -count=1 -v
cd frontend && npm run contracts && npm run contracts:check
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add contracts/agent.approve.schema.json internal/transport/ws_agent.go internal/transport/ws_agent_test.go internal/content/effectpolicy.go internal/assistant/approvals.go frontend/src/generated/agent.approve.ts
git commit -m "feat(transport): an answer says how far it reaches, and the backend is what applies it (<bead-id>)"
```

---

### Task 5: `policy.get` reports which effects are live

**Files:**

- Modify: `contracts/policy.get.schema.json`
- Modify: `internal/transport/ws_policy.go`
- Modify: `internal/agenttools/registry.go` (a `LiveEffects()` accessor)
- Regenerate: `frontend/src/generated/policy.get.ts`
- Modify: `frontend/src/policy-client.ts`

**Interfaces:**

- Consumes: nothing from earlier tasks.
- Produces: `PolicyGet.live: Effect[]`; `PolicyMatrix` gains nothing — `PolicyClient.get()` returns `{matrix, live}`.

**Acceptance Criteria:**

- `live` lists exactly the effects at least one DECLARED tool carries — today `observe` and `mutate-destructive`, in the lattice's canonical order, deduplicated.
- `policy.get`'s top-level object gains `additionalProperties: false` (it lacks it today, which `AGENTS.md` rule 5 calls theatre) and `live` is required.
- Adding a tool with a new effect changes `live` without any other edit. Asserted by a test that registers a declaration carrying `disclose` and sees it appear.
- The over-the-wire test validates the real result.

- [ ] **Step 1: Write the failing Go test**

```go
func TestRegistry_LiveEffects_IsWhatTheDeclarationsCarry(t *testing.T) {
	r := /* the assembled production registry */
	got := r.LiveEffects()
	want := []content.Effect{content.EffectObserve, content.EffectMutateDestructive}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("live effects: got %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run, watch it fail**

```bash
go test ./internal/agenttools/ -run TestRegistry_LiveEffects -v
```

Expected: `r.LiveEffects undefined`.

- [ ] **Step 3: Implement**

In `internal/agenttools/registry.go`:

```go
// LiveEffects is the set of effect classes at least one declared tool
// carries, in the lattice's canonical order. The registry is the only place
// that knows, and the settings surface needs to know: five controls that
// govern nothing must not look like the two that do.
func (r Registry) LiveEffects() []content.Effect {
	carried := make(map[content.Effect]bool, len(allEffects))
	for _, t := range r.All() {
		carried[t.Effect] = true
	}
	out := make([]content.Effect, 0, len(allEffects))
	for _, e := range allEffects {
		if carried[e] {
			out = append(out, e)
		}
	}
	return out
}
```

- [ ] **Step 4: Put it on the wire**

Schema: add `additionalProperties: false` at the top level, add `"live"` to `required`, and:

```json
    "live": {
      "description": "The effect classes at least one declared tool carries. A row outside this list governs nothing yet — the surface says so rather than offering it as an equal.",
      "type": "array",
      "items": {
        "type": "string",
        "enum": ["observe", "mutate-reversible", "mutate-destructive", "privilege-change", "disclose", "cross-boundary", "delegate"]
      }
    }
```

`ws_policy.go`'s get handler fills it from the registry the server already holds.

- [ ] **Step 5: Adapt the client**

In `frontend/src/policy-client.ts`, `get()` returns the matrix AND the live list:

```ts
export interface PolicyView {
  matrix: PolicyMatrix
  /** The effect classes a declared tool carries. Anything else governs
   *  nothing yet, and the page says so rather than offering it as an equal. */
  live: EffectKey[]
}

get(): Promise<PolicyView> {
  return this.dispatcher
    .call<PolicyGet>('policy.get', {})
    .then((r) => ({ matrix: toMatrix(r.policy), live: r.live as EffectKey[] }))
}
```

- [ ] **Step 6: Run**

```bash
go test ./internal/agenttools/ ./internal/transport/ -run 'LiveEffects|Policy' -count=1
cd frontend && npm run contracts && npm run contracts:check && npx tsc --noEmit -p tsconfig.json
```

- [ ] **Step 7: Commit**

```bash
git add contracts/policy.get.schema.json internal/agenttools/registry.go internal/agenttools/registry_test.go internal/transport/ws_policy.go frontend/src/generated/policy.get.ts frontend/src/policy-client.ts
git commit -m "feat(transport): policy.get says which effect classes a tool actually carries (<bead-id>)"
```

---

### Task 6: The prompt offers six answers

**Files:**

- Modify: `frontend/src/agent-approval-prompt.tsx`
- Modify: `frontend/src/main.tsx:234-260` (`decideApproval`)
- Test: `frontend/src/agent-approval-prompt.test.tsx`

**Interfaces:**

- Consumes: `AgentApprovalRequested.effect` (Task 3), `AgentApprove.scope` (Task 4).
- Produces: `AgentApprovalPromptProps.onDecide(approved: boolean, scope: 'once' | 'session' | 'always')`, replacing `onAllow`/`onDeny`.

**Acceptance Criteria:**

- A `policy` question renders six actions: Allow once / Allow in this session / Allow always, and the three denials.
- Each reaches `agent.approve` with the right `approved` and `scope` pair. Asserted through the client seam, not by reading the handler.
- An `egress` question renders exactly two actions, Allow and Deny, and both send `scope: "once"`.
- The question names the effect in product words — "The assistant wants to **read and inspect**" — from `effect`, never from `tool`.
- The tool name and arguments stay on the surface: they are what the person is deciding about.
- The sentence about what approving covers stays, verbatim.

**Before writing any control:** read `frontend/src/ui/README.md` and list `frontend/src/ui/`. Six actions is a layout the kit may not have. A surface may PLACE kit components; it may not repaint them. If the kit needs a variant, add it in `ui/` with its own test and README row — do not hand-roll a button group in this file.

- [ ] **Step 1: Write the failing tests**

Create/extend `frontend/src/agent-approval-prompt.test.tsx`:

```tsx
it('a policy question offers three allowances and three refusals, each with its scope', async () => {
  const decisions: Array<[boolean, string]> = []
  const { getByRole } = render(() => (
    <AgentApprovalPrompt
      open
      busy={false}
      ask={policyAsk({ effect: 'observe', tool: 'readScreen' })}
      onDecide={(approved, scope) => decisions.push([approved, scope])}
    />
  ))
  fireEvent.click(getByRole('button', { name: 'Allow once' }))
  fireEvent.click(getByRole('button', { name: 'Allow in this session' }))
  fireEvent.click(getByRole('button', { name: 'Allow always' }))
  fireEvent.click(getByRole('button', { name: 'Deny once' }))
  fireEvent.click(getByRole('button', { name: 'Deny in this session' }))
  fireEvent.click(getByRole('button', { name: 'Deny always' }))
  expect(decisions).toEqual([
    [true, 'once'],
    [true, 'session'],
    [true, 'always'],
    [false, 'once'],
    [false, 'session'],
    [false, 'always'],
  ])
})

it("names the effect in the product's words, not the tool's", () => {
  const { container } = render(() => (
    <AgentApprovalPrompt
      open
      busy={false}
      ask={policyAsk({ effect: 'observe', tool: 'readScreen' })}
      onDecide={() => {}}
    />
  ))
  expect(container.textContent).toContain('read and inspect')
})

it('an egress question offers two answers, and both are once', async () => {
  const decisions: Array<[boolean, string]> = []
  const { getByRole, queryByRole } = render(() => (
    <AgentApprovalPrompt
      open
      busy={false}
      ask={egressAsk()}
      onDecide={(a, s) => decisions.push([a, s])}
    />
  ))
  expect(queryByRole('button', { name: 'Allow always' })).toBeNull()
  fireEvent.click(getByRole('button', { name: 'Allow' }))
  fireEvent.click(getByRole('button', { name: 'Deny' }))
  expect(decisions).toEqual([
    [true, 'once'],
    [false, 'once'],
  ])
})
```

- [ ] **Step 2: Run, watch it fail**

```bash
cd frontend && npx vitest run src/agent-approval-prompt.test.tsx
```

- [ ] **Step 3: Implement**

Replace the props and the actions block. The effect labels are the SAME map the settings page uses — put it in one module both import (`frontend/src/effect-labels.ts`), because two surfaces inventing their own wording for one state is the defect `AGENTS.md` names:

```ts
// frontend/src/effect-labels.ts
/** The product's words for the effect lattice (ADR-0020 decision 6). ONE
 *  owner: the approval prompt and the Agent policy page both read this, so a
 *  person meets the same words in the question and in the setting. */
export const EFFECT_LABEL: Record<EffectKey, string> = {
  observe: 'read and inspect',
  'mutate-reversible': 'make changes that can be undone',
  'mutate-destructive': 'make changes that cannot be undone',
  'privilege-change': 'gain more privilege',
  disclose: 'send information out',
  'cross-boundary': 'reach another host',
  delegate: 'hand work to another agent',
}
```

The prompt reads `EFFECT_LABEL[ask().effect]` and renders the six actions for `reason === 'policy'`, two for `'egress'`.

- [ ] **Step 4: Carry the scope through `main.tsx`**

```ts
const decideApproval = async (approved: boolean, scope: 'once' | 'session' | 'always') => {
  const ask = activeApproval()
  if (!ask || approvalBusy()) return
  setApprovalBusy(true)
  try {
    await dispatcher.call('agent.approve', {
      runId: ask.runId,
      attempt: ask.attempt,
      tool: ask.tool,
      callId: ask.callId,
      argHash: ask.argHash,
      approved,
      scope,
    } satisfies AgentApprove)
    pendingApprovals.delete(ask.runId)
    nextApproval()
  } catch (err) {
    /* unchanged */
  } finally {
    setApprovalBusy(false)
  }
}
```

and the JSX passes `onDecide={decideApproval}`.

- [ ] **Step 5: Run**

```bash
cd frontend && npx vitest run src/agent-approval-prompt.test.tsx src/main.test.ts && npx tsc --noEmit -p tsconfig.json
```

- [ ] **Step 6: Commit**

```bash
git add frontend/src/agent-approval-prompt.tsx frontend/src/agent-approval-prompt.test.tsx frontend/src/effect-labels.ts frontend/src/main.tsx
git commit -m "feat(frontend): the question a person is asked is where they can stop being asked (<bead-id>)"
```

---

### Task 7: The Agent policy page

**Files:**

- Modify: `frontend/src/agent-policy-section.tsx`
- Modify: `frontend/src/agent-policy-section.test.tsx`
- Modify: `frontend/src/styles/surfaces/agent-policy.css` (only if a disclosure needs placement)

**Interfaces:**

- Consumes: `PolicyView` (Task 5), `EFFECT_LABEL` (Task 6).
- Produces: nothing later tasks depend on.

**Acceptance Criteria:**

- **The Save button is gone.** Changing a decision select writes immediately and the page adopts the policy the store returned — it can never show a policy the store did not take.
- A scope's text field writes on **blur or Enter**, never per keystroke: `ParseEffectPolicy` rejects a non-absolute path, so a half-typed `/w` would be a refused write on every character.
- A refused write raises the kit's danger toast and the page re-reads.
- Rows whose effect is in `live` render first. The rest sit behind ONE disclosure that says what they are: capabilities the assistant does not have yet.
- The page description is the product's sentence, and the words "effect class", "resource scope" and "refused" do not appear on the surface.
- Row labels come from `EFFECT_LABEL` — the same words the prompt used.
- A row that is not on the default carries one sentence saying what it means in the person's terms ("Read and inspect — Allowed"). Changing the row's select IS the revoke; there is no second control.

- [ ] **Step 1: Write the failing tests**

```tsx
it('has no Save button: changing a decision writes it and adopts what the store returned', async () => {
  const { container, sent } = mount({
    'policy.get': () => ({ policy: allAsk(), live: ['observe', 'mutate-destructive'] }),
    'policy.set': () => ({}),
  })
  await waitForRows(container)
  expect(queryByRole(container, 'button', { name: /save/i })).toBeNull()

  fireEvent.change(decisionSelect(container, 'observe'), { target: { value: 'permit' } })
  await vi.waitFor(() => {
    expect(sent.filter((s) => s.method === 'policy.set')).toHaveLength(1)
  })
})

it('a refused write toasts and re-reads: the page never shows a policy the store did not take', async () => {
  const { container, sent } = mount({
    'policy.get': () => ({ policy: allAsk(), live: ['observe', 'mutate-destructive'] }),
    'policy.set': () => {
      throw new Error('config domain busy')
    },
  })
  await waitForRows(container)
  fireEvent.change(decisionSelect(container, 'observe'), { target: { value: 'permit' } })

  await vi.waitFor(() => {
    expect(
      toasts().some((t) => t.level === 'danger' && t.message.includes('config domain busy')),
    ).toBe(true)
  })
  await vi.waitFor(() => {
    expect(sent.filter((m) => m.method === 'policy.get')).toHaveLength(2)
  })
  expect(decisionSelect(container, 'observe').value).toBe('ask')
})

it('a scope field writes on blur, not on every keystroke', async () => {
  // ParseEffectPolicy rejects a non-absolute path, so a per-keystroke write
  // would be a refused write and a toast on every character of "/workspace".
  const { container, sent } = mount({
    'policy.get': () => ({ policy: allAsk(), live: ['observe', 'mutate-destructive'] }),
    'policy.set': () => ({}),
  })
  await waitForRows(container)
  fireEvent.click(addScopeButton(container, 'observe'))
  const field = scopeField(container, 'observe', 0)

  fireEvent.input(field, { target: { value: '/w' } })
  fireEvent.input(field, { target: { value: '/workspace' } })
  expect(sent.filter((m) => m.method === 'policy.set')).toHaveLength(0)

  fireEvent.blur(field)
  await vi.waitFor(() => {
    expect(sent.filter((m) => m.method === 'policy.set')).toHaveLength(1)
  })
})

it('lists the live effect classes first and puts the rest behind one disclosure', async () => {
  const { container } = mount({
    'policy.get': () => ({ policy: allAsk(), live: ['observe', 'mutate-destructive'] }),
    'policy.set': () => ({}),
  })
  await waitForRows(container)

  const visible = [...container.querySelectorAll('.st-policy__row:not([hidden])')].map((r) =>
    r.getAttribute('data-effect'),
  )
  expect(visible).toEqual(['observe', 'mutate-destructive'])
  const disclosure = container.querySelector('.st-policy__dormant')!
  expect(disclosure).toBeTruthy()
  expect(disclosure.textContent).toMatch(/does not have yet/i)
})

it('a row off the default says what it means, in the same words the prompt used', async () => {
  const { container } = mount({
    'policy.get': () => ({
      policy: { ...allAsk(), observe: { decision: 'permit', scopes: [] } },
      live: ['observe', 'mutate-destructive'],
    }),
    'policy.set': () => ({}),
  })
  await waitForRows(container)
  const row = container.querySelector('.st-policy__row[data-effect="observe"]')!
  expect(row.textContent).toContain('Read and inspect')
  expect(row.textContent).toContain('Allowed')
})

it("says nothing in the ADR's words", async () => {
  const { container } = mount({
    'policy.get': () => ({ policy: allAsk(), live: ['observe', 'mutate-destructive'] }),
    'policy.set': () => ({}),
  })
  await waitForRows(container)
  const text = container.textContent!.toLowerCase()
  for (const word of ['effect class', 'resource scope', 'is refused', 'rows nobody set']) {
    expect(text).not.toContain(word)
  }
})
```

- [ ] **Step 2: Run, watch them fail**

```bash
cd frontend && npx vitest run src/agent-policy-section.test.tsx
```

- [ ] **Step 3: Implement**

Delete the `save`/`saving` machinery and the Save `Button`. Add an `adopt` that takes the whole `PolicyView` from every read and every write, the way `roles-section.tsx` does — read that file first; it is the pattern this must match, not a new one.

The description becomes:

> What the assistant may do on its own, and what it must ask you about first. Anything not set here is asked.

Labels come from `EFFECT_LABEL`, sentence-cased for a heading.

- [ ] **Step 4: Run**

```bash
cd frontend && npx vitest run src/agent-policy-section.test.tsx src/settings.test.ts && npx tsc --noEmit -p tsconfig.json && npm run lint
```

- [ ] **Step 5: Commit**

```bash
git add frontend/src/agent-policy-section.tsx frontend/src/agent-policy-section.test.tsx frontend/src/styles/surfaces/agent-policy.css
git commit -m "fix(frontend): the policy page writes on change and says what it means (<bead-id>)"
```

---

### Task 8: The happy path, watched end to end

**Files:**

- Create: `e2e/agent-policy.spec.ts`
- Modify: `internal/content/effectpolicy.go:3`, `internal/assistant/globalpolicy.go:2`, `frontend/src/agent-policy-section.tsx:3` (the stale comments)

**Interfaces:**

- Consumes: everything above.
- Produces: the check that closes the epic.

**Acceptance Criteria:**

- One automated check watches a person: ask the assistant, be asked, answer **Allow always**, ask again in the same pane, and **not** be asked; then open Settings → Agent policy, see the standing decision, set it back to Ask, and be asked again on the next question.
- A second check watches **Allow in this session**: not asked again in that pane, and asked again after the session is restarted.
- The stale "amendment PROPOSED, awaiting owner approval" comments are corrected to record the acceptance ADR-0020's header already carries.

- [ ] **Step 1: Write the spec**

Model it on `e2e/agent-ask.spec.ts`, which already stands up the fake endpoint, the vault and the ask. Reuse its helpers rather than adding new ones.

```ts
import { test, expect, saveEndpoint, assignAnsweringRole, promptReady } from './harness'

/** Ask, and settle the answer. Returns nothing — the ANSWER's arrival is the
 *  synchronisation point every step below waits on, because no test here may
 *  wait on a duration (AGENTS.md). */
async function ask(page: Page, question: string, nth: number) {
  await page.locator('.cm-content').fill(question)
  await page.keyboard.press('Enter')
  await expect(page.locator('.agent-answer')).toHaveCount(nth, { timeout: 30_000 })
}

test('a person stops being asked, and can start being asked again', async ({ page }) => {
  const nonce = Date.now().toString(36)
  const fake = await startFakeEndpoint({ proposeTool: 'readScreen' })
  await saveEndpoint(page, { name: `E2E Fake ${nonce}`, baseUrl: fake.url, key: 'k' })
  await assignAnsweringRole(page, `E2E Fake ${nonce}`, 'e2e-model')
  await promptReady(page)

  // 1. The first question escalates: the policy default is ask.
  await page.locator('.cm-content').fill('what went wrong here?')
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible({ timeout: 30_000 })

  // 2. It names the effect in the product's words, not the tool's.
  await expect(dialog).toContainText('read and inspect')

  // 3. Allow always. The run resumes and the answer arrives.
  await dialog.getByRole('button', { name: 'Allow always' }).click()
  await expect(page.locator('.agent-answer')).toHaveCount(1, { timeout: 30_000 })

  // 4. The second question is NOT asked about. The answer's arrival is what
  //    proves the run completed; only then is the dialog's absence meaningful.
  await ask(page, 'and if I fix the type?', 2)
  await expect(page.getByRole('dialog')).toHaveCount(0)

  // 5. The page shows the standing decision, and setting it back brings the
  //    question back — the revoke is the same control, not a second one.
  await openSettings(page, 'Agent policy')
  const observeRow = page.locator('.st-policy__row[data-effect="observe"]')
  await expect(observeRow).toContainText('Read and inspect')
  await expect(observeRow).toContainText('Allowed')
  await observeRow.locator('select').first().selectOption({ label: 'Ask' })
  await expect(observeRow).not.toContainText('Allowed')

  await closeSettings(page)
  await page.locator('.cm-content').fill('and now?')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('dialog')).toBeVisible({ timeout: 30_000 })
})

test('an allow given in one session does not outlive it', async ({ page }) => {
  const nonce = Date.now().toString(36)
  const fake = await startFakeEndpoint({ proposeTool: 'readScreen' })
  await saveEndpoint(page, { name: `E2E Fake ${nonce}`, baseUrl: fake.url, key: 'k' })
  await assignAnsweringRole(page, `E2E Fake ${nonce}`, 'e2e-model')
  await promptReady(page)

  await page.locator('.cm-content').fill('what went wrong here?')
  await page.keyboard.press('Enter')
  await page.getByRole('dialog').getByRole('button', { name: 'Allow in this session' }).click()
  await expect(page.locator('.agent-answer')).toHaveCount(1, { timeout: 30_000 })

  // In force for the rest of this session...
  await ask(page, 'and if I fix the type?', 2)
  await expect(page.getByRole('dialog')).toHaveCount(0)

  // ...and gone with it. A new session of the same pane is a new session.
  await restartSessionInPane(page)
  await promptReady(page)
  await page.locator('.cm-content').fill('what went wrong here?')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('dialog')).toBeVisible({ timeout: 30_000 })
})
```

> `startFakeEndpoint`, `saveEndpoint`, `assignAnsweringRole`, `openSettings`, `closeSettings` and `restartSessionInPane` are the harness's existing helpers under whatever names it gives them — **read `e2e/harness.ts` and `e2e/agent-ask.spec.ts` first** and reuse them; `agent-ask.spec.ts` already stands up a fake endpoint that proposes a tool. Add a helper only if none exists, and add it to `harness.ts` rather than to this spec.
>
> The fake must propose `readScreen` so the escalation is a POLICY ask over `observe`. If the existing fake cannot be told which tool to propose, that parameter is the one new thing this task adds to the harness.

> **No test may depend on timing** (`AGENTS.md`). Assert the absence of the prompt by waiting for the second answer to arrive and THEN asserting the dialog has count 0 — never by sleeping.

- [ ] **Step 2: Run it in the container**

```bash
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/agent-policy.spec.ts
```

Expected: PASS.

- [ ] **Step 3: Correct the stale comments**

Each of the three reads "amendment PROPOSED, awaiting owner approval". ADR-0020's header records `§7 amended 2026-08-16, accepted`. Replace with "as amended 2026-08-16, accepted".

- [ ] **Step 4: Commit**

```bash
git add e2e/agent-policy.spec.ts internal/content/effectpolicy.go internal/assistant/globalpolicy.go frontend/src/agent-policy-section.tsx
git commit -m "test(e2e): a person stops being asked, and can start again (<bead-id>)"
```

---

## Task dependency order

```
1 ──> 2 ──┐
          ├──> 4 ──> 6 ──┐
3 ────────┘         ▲    ├──> 8
3 ──────────────────┘    │
5 ──────────────> 7 ─────┘
```

- Task 2 needs Task 1's `SessionOverrides`.
- Task 4 needs Task 2's store and Task 3's `Effect` on the request.
- Task 6 needs Task 3 (`effect` on the wire) and Task 4 (`scope` on the wire).
- Task 7 needs Task 5 (`live`).
- Task 8 needs 6 and 7.
- Tasks 1, 3 and 5 have no blockers and can start together.
