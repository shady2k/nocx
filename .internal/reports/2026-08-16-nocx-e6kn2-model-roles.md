# nocx-e6kn2 — model roles: a feature asks for a role, never for a model id

## What was built

A named role resolves to one (endpoint, model) pair, and every feature asks
for a role. The layer above `Endpoint.Models`:

- **`internal/profile/role.go`** — the closed role enum (`answering`,
  `classifier`), the stored `RoleAssignment` (one pair per role; a role is
  assignable, clearable, and never dangled), the store-level shape
  validation, and **`ResolveRole` — THE ONE resolver** with the three
  visible refusals: `ErrRoleUnassigned`, `ErrRoleEndpointGone`,
  `ErrRoleModelGone`, each wrapped with the role name and what disappeared.
- **`internal/profile/store.go`** — roles ride the same JSON document as
  endpoints (a role is a reference to an endpoint, matching the ADR-0030
  reasoning); `LoadRoleAssignments` + `AssignRole` (upsert; the empty pair
  is the CLEAR write) — one atomic document write.
- **`internal/capability/config.go`** — `ConfigService` gains
  `ListRoleAssignments`, `AssignRole`, `ResolveRole`; the operation is
  threaded a `profile.RoleRepository` (derived from the same store as the
  endpoints repo in `buildConfigOp`, so end-to-end wiring is one line).
- **`internal/transport/ws_roles.go`** — `roles.list` + `roles.assign`
  over the real socket. `roles.list` completes the stored assignments to
  the CLOSED SET: an unassigned role is a null row, never an absent one.
  `roles.assign` upserts or clears (both-null), returns the full table.
- **`internal/transport/ws_agent.go`** — `agent.ask` now resolves through
  `svc.ResolveRole(RoleAnswering)` instead of picking `eps[0].Models[0]`.
  Role refusals are renderable conditions (-32603) with a repair sentence;
  the endpoint/model pair is still pinned at ask time (run facts), so
  approval/resume are untouched.
- **`contracts/roles.list.schema.json` + `roles.assign.schema.json`** —
  `additionalProperties: false`, explicit `required`, closed `enum` for the
  role; assign references list's `$defs` cross-file (the git.status
  pattern). Generated renderer types committed.
- **`frontend/src/roles-section.tsx`** — the new "Roles" settings page
  (assistant group): one row per role, endpoint + model selects (kit
  `Select`, native), state sentence rendered with the kit `StatusDot`
  vocabulary — assigned ("Answers with OpenAI · gpt-4o"), unassigned
  (warning), endpoint-gone / model-gone (error, naming what is missing).
  A draft (endpoint picked, model not yet) is never written; "— None —"
  clears.
- **`frontend/src/endpoints.ts`** — `EndpointClient.listRoles()` /
  `assignRole()` (+ the `RoleAssignInput` contract type and the
  `WireRole` union that consumes BOTH generated `Role` declarations, so the
  dead-exports ratchet stays green).
- **`frontend/src/settings.tsx`** — exactly my registration lines (import +
  `rolesPage` entry + one array element); no restructure.

## Gates (exact)

| Gate                                                                     | Result                                                                                                                                                    |
| ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `go vet ./internal/profile/... ./internal/transport/...`                 | 0 issues                                                                                                                                                  |
| `golangci-lint run ./...`                                                | 0 issues                                                                                                                                                  |
| `go test ./internal/profile/... ./internal/transport/... -race -count=1` | `internal/profile` ok · `internal/transport` ok · `control` ok · `outbound` ok                                                                            |
| `node .githooks/check-deadcode.mjs`                                      | exit 0 — **84 unreachable, all baselined** (baseline unchanged)                                                                                           |
| `cd frontend && tsc --noEmit -p tsconfig.json`                           | 0                                                                                                                                                         |
| `cd frontend && tsc --noEmit -p tsconfig.test.json`                      | 0                                                                                                                                                         |
| `cd frontend && vitest run` (whole suite)                                | **163 files, 2949 tests passed** (roles-section: 10/10)                                                                                                   |
| `cd frontend && npm run contracts:check`                                 | 0                                                                                                                                                         |
| `cd frontend && npm run lint` (full chain, incl. dead-exports)           | 0 — dead-exports ratchet: **0 NEW** (1 NEW found during the pass — `roles.assign.ts: Role` — fixed at the cause with the `WireRole` union, not baselined) |
| `git diff --check`                                                       | clean                                                                                                                                                     |

## Acceptance criteria → asserting tests

1. **Assign in the product, feature picks it up, no model id named outside
   the assignment.** `TestAgentAsk_UsesTheAnsweringRoleAssignment` and
   `TestAgentAsk_ReassignmentIsPickedUp` (transport): two endpoints, the
   role assigned to the SECOND; the stub engine receives exactly the
   assigned pair, and a reassignment is picked up by the next ask.
   Frontend half: `roles-section.test.tsx` "picking an endpoint then a
   model reaches roles.assign with EXACTLY that pair".
2. **Unassigned role = visible failure, never a silent fallback.**
   `TestAgentAsk_NoEndpointIsARefusal` (message names the answering role
   and the repair); `profile/role_test.go`
   `TestResolveRole_UnassignedIsARefusalNeverAFallback` (an unassigned
   role refuses even with an unassigned-but-present endpoint on the list);
   `roles-section.test.tsx` "an unassigned role next to working endpoints
   shows the no-model warning".
3. **Disappeared model/endpoint → unresolvable and says so.**
   `TestAgentAsk_DeletedEndpointLeavesTheRoleARefusal` and
   `TestAgentAsk_RemovedModelLeavesTheRoleARefusal` — the ask refuses, the
   message names the deleted endpoint / removed model, and the model is
   NEVER called (`askCount() === 0`); `profile` ResolveRole tests for both;
   `capability/role_test.go TestRoles_EndpointDeleteLeavesTheDangleVisible`;
   the roles page renders the error sentences
   (`roleStateLine` unit tests).
4. **Both ends: the ordinary ask keeps working, through the role.** All
   pre-existing ask tests run unchanged modulo harness seeding
   (`askHarness.createEndpoint` now also assigns the answering role — the
   exact product migration): `TestAgentAsk_StreamsTheAnswerAndTerminalizes`,
   `_ModelFailureTerminalizesFailed`, `_ConnectionLostMidStreamTerminalizes`,
   `_GeneralQuestionWithNoReferencesStreams`, `_RegionSelectsRowsForTheModel`,
   `_EndpointHeadersResolveAtStreamTime`, and the three readScreen
   e2e-style tests. Nothing about the streaming path changed for a user.
5. **Wire in contracts, generated types committed, asserted over the
   socket.** `TestRoles_OverTheWireConformsToContract` (roles.list,
   roles.assign, and the clear write, validated against both schemas off
   the real socket) + `TestRolesList_DTOConformsToContract`; generated
   types committed and `contracts:check` green.
6. **Deadcode baseline unchanged.** See gate table.

## Kit component use

No new kit component and no variant were needed — the existing kit already
had every vocabulary: **`Select`** (the roles' two pickers; native select
with an accessible `<label>` wrapper), **`StatusDot`** + tone text for the
state sentence (the same tones/words the endpoints rows and
`agent-status-line` use), **`Stack`** for vertical rhythm
(`surface-spacing-kit`), **`EmptyState`** for the no-endpoints / failure
states, **`Spinner`** and **`Button`** (Retry). The only new stylesheet
(`styles/components/roles.css`) is placement + the surface's own sentence
text — no kit component is repainted.

## Blast-radius grep (whole set?)

- `eps[0]`, `Models[0]`, `[0].Models` in prod Go: **zero**. The `eps[0]`/
  `Models[0]` selector is gone from `ws_agent.go` (the only such site).
- The one resolver: `profile.ResolveRole` ← `configService.ResolveRole` ←
  `ws_agent.go handleAsk` only. `ListEndpoints` remains only where it is
  legitimately about the endpoint LIST (endpoints.list handler) or the
  endpoint-presence fact (agent.status, unchanged semantics — documented
  below), never role selection.
- Frontend: the only place a role's model is NAMED outside the assignment
  write is the roles page row's state sentence (which reads the wire) and
  the ask refusal message — display, not resolution.
- A `settings.test.ts` rail assertion pinned the page list and broke:
  updated to include the new "Roles" page (one reviewed change to that
  test); the rest of the suite green.

## What I could not verify, and why

- The **classifier** role has no consumer yet (its bead `nocx-kpy23` is
  blocked ON this one): assignment is tested, the closed set is tested,
  but "a classifier ask resolves" cannot be asserted until the consumer
  exists.
- **end-to-end in the running product** (real browser/socket journey:
  assign in Settings → ask at the prompt → answer streams): the brief's
  verification is explicitly scoped to unit/integration gates; the over
  -the-EE socket tests (real `WSServer` + scripted engine) are the
  closest permitted substitute.
- `internal/app` and `internal/shellintegration` fail on this host —
  **pre-existing and environmental**: `/bin/bash` and the bash-3.2
  fixture are absent on this bare NixOS box (`fork/exec /bin/bash: no
such file or directory`), per the known local-vs-CI shell discrepancy in
  AGENTS.md. Neither package imports anything I changed; the failing
  tests are pty/shell fixtures. My scoped packages are green with
  `-race`.
- The Roles page's actual rendering inside the running settings rail is
  covered by the settings.test.ts rail criterion (the page is registered
  and visible there), but I could not LOOK at it: the brief forbids
  container runs and e2e, and jsdom cannot paint. The rendering contract
  is pinned by the roles-section vitest instead.

## What I deliberately left alone

- **`agent.status`** (`endpointConfigured`/`credential` semantics): it
  keeps meaning "an endpoint exists / this endpoint's credential", not
  "the answering role resolves". The readiness fact for roles is the
  roles wire + the ask refusal; changing the existing boolean's meaning
  would have silently re-contracted a field the ask surface keys on
  (`onNoEndpoint`), outside this bead's scope.
- **`internal/assistant` / `internal/content`** (wv7-policy's packages):
  the engine interface (`Client`) and content ledger are untouched; the
  transport passes the role-resolved pair into the existing
  `content.RunFacts`, so the run record still pins endpoint+model at ask
  time.
- **`delete` cascade**: `DeleteEndpoint` does NOT chase role
  assignments — a role names an endpoint, not a secret, so the dangle is
  kept deliberately so the refusal (and the role row) can NAME the
  deleted endpoint (the bead's "says so", which a cascade would erase).

## Open position

The end-of-beed migration behavior: an install with an endpoint but
**no role assignment** now gets a visible refusal at ask time
("the answering role has no model assigned — assign one in
Settings → Roles") instead of silently using the first endpoint's first
model. That is acceptance criterion 2's contract, not a regression: the
old behavior is exactly the silent fallback the bead forbids.

## Attribution follow-up (criterion 2's "a person must be able to tell which model answered")

Reviewed post-delivery: the answer block previously showed NO model, so the
criterion's second clause was unverifiable in the product. Added end to end:

- `contracts/agent.ask.schema.json` gains **required `model`** — the
  answering role's pair, pinned into `RunFacts.Model` BEFORE the
  transaction. Always present on a result (an unresolvable role is a
  refusal, never a result), so the wire cannot omit it (`additionalProperties:
false` + `required` keeps it exact; `contracts:check` green).
- `internal/transport/ws_agent.go` — `agentAskResponse.Model` = `facts.Model`.
- `frontend/src/agent-ask.ts` — the pinned model rides the block
  (`handle.el.dataset.answeredBy = ask.model`) and the terminal
  `close('success', undefined, model)` passes it.
- `frontend/src/scrollback/blocks.ts` — `AnswerBlockHandle.close` gains an
  optional `model`; a successful close paints `answered by {model}` under
  the answer (`.cmd-answer-provenance`, muted caption style). No model =
  no attribution line (nobody is named who did not answer); failure keeps
  only the renderable reason.

Assertions: `TestAgentAsk_StreamsTheAnswerAndTerminalizes` asserts
`res.Model == "qwen3"`; `TestAgentAsk_UsesTheAnsweringRoleAssignment`
asserts `askRes.Model == "gpt-4o"` (the ASSIGNED pair, off the real
socket); `TestAgentAsk_DTOConformsToContract` + the over-the-wire case
require the field against the schema; `blocks.test.ts` (two new tests) and
`agent-ask.test.ts` (the close assertion now `('success', undefined,
'qwen3')`) pin the renderer half; full vitest **2951 passed**.

## Final verification + the race-gate evidence

All gates on the final tree: `go vet` 0; `golangci-lint run ./...` 0;
`node .githooks/check-deadcode.mjs` exit 0 (84 unreachable, all baselined);
`tsc` ×2 0; `npm run contracts:check` 0; `npm run lint` 0; full frontend
vitest **163 files / 2951 tests**; full-package `go test ./internal/transport/
-race -count=1` passed on the final clean run.

The full-package -race gate did NOT pass on every attempt: five different
tests failed across three runs, each in an unrelated domain (git.changed
×2 names, lifecycle.changed, tabby vault retry, agent.approve contract),
each **passing alone** (agent.approve 3/3 and 3/3, tabby 0/20 with -race,
git.changed 3/3), and a clean HEAD baseline worktree (no changes) passed the whole gate —
a pattern matching the documented host flake class (AGENTS.md nocx-2h08:
"a 30-second timeout under a different test name in every environment").
My new ask/resolve tests add full-harness weight (vault + content DB +
websocket each) which can only ratchet this load sensitivity; the failing
behavior was never a stable one, and no single assertion failed twice. The
final full run was green; the coordinator's merged-tree gate on CI is the
source of truth.
