# Experimental Pydantic Deep Agents replacement for Eino

## Context

Replace nocx's Eino-based assistant with the framework in `vstorm-co/pydantic-deepagents`, preserving current user-visible behavior and the existing Go-owned authority boundaries. The owner explicitly requires an experimental feature flag for the transition and selected **engine replacement plus current capabilities**, not the additional product features in `DEEP_AGENTS_PLAN.md`. Success means Ask, endpoint Test, skill auditing and drafting all use the new model implementation when selected, while existing tools, history, approvals, cancellation and Settings continue to work; turning the flag off routes new operations to the retained Eino implementation.

The initial proposal came from read-only investigation of nocx at `c1eb69b999fffad094323be3555e0bc44b045689` and upstream sources inspected on 2026-10-29. That investigation did not install packages, resolve dependencies or execute compatibility experiments. The implementation evidence below is separate: the difficult runtime seams were exercised before exposing the switch.

## Execution admission and current-main reconciliation

The owner authorized implementation on 2026-10-29 in
`feat/pydantic-agent-experimental`, in the isolated checkout
`/home/andrey/projects/nocx-pydantic-agent`, based on freshly fetched
`origin/main` at `6062102f0`. Feature `nocx-g9giy` owns this work;
`nocx-g9giy.1` owns admission and reconciliation.

Before final integrated verification, the feature checkout advanced first to
`origin/main` at `ceb6bcc87`, then integrated `3a67b18ec`. The newer
busy-terminal lifecycle, keyed session control, ledger-row changes and
matched FreeType AppImage packaging remain in the integration; they are not
replaced with the original investigation's versions.

The owner initially selected an independent workstream outside the
single-current-milestone rule. On 2026-10-30 the owner authorized `v0-9`
instead, with this integration as its new goal. The charter carries forward
the three active `busy-terminal` outcomes without changing their holders,
priorities or acceptance criteria. The original main-checkout proposal remains
untouched; no release date or promotion is implied.

At initial admission, compared with the source investigation revision, the
assistant implementation, transport handlers and host composition had not changed. Relevant changes
elsewhere must be carried into implementation: the coordinator protocol is now
7, so the combined wire cutover starts at 8 unless integration advances it
again; the release workflow has been split into reusable pipeline components;
and ledger clear boundaries now preserve the latest observed entry identity.
Runtime packaging must extend the current pipeline rather than reproduce the
old release workflow. Replay/hydration must preserve current clear-boundary
semantics.

The installed backlog checks are all protocol 0.56.0. The initial
independent-workstream admission was rejected by `off-milestone-open`;
`nocx-g9giy.5` now resolves that through the owner-authorized v0-9 charter
and ordinary milestone labels, not by changing or bypassing any gate rule.

The runtime proof and independent design review are prerequisites to retained
product implementation. Source inspection remains distinct from executable
evidence; record their findings before declaring the implementation ready.

## Analysis and scope decisions

### What the candidate actually supplies

Pin Pydantic Deep Agents to `650b592b6accd7dc994226abbaa0cf717527e457`, release `pydantic-deep` 0.3.50. Use its Python library, not its terminal CLI, ACP server, Docker workspace or installer. The factory delegates its loop to Pydantic AI and accepts a custom model and custom toolsets. It can disable its built-in capabilities individually. There is **no committed upstream dependency lock**: its version floors do not prove a compatible installation.

The source-investigated dependency candidate is Pydantic AI slim 2.52.0, harness 0.52.0, OpenAI SDK 3.19.0 and httpx2 2.12.0. Harness 0.52.0 pins Pydantic AI slim 2.52.0. The native Chat adapter recognizes streamed `reasoning_content` and `reasoning`. `httpx2`, not the older `httpx`, is the transport interface in this candidate. Resolve and hash the entire graph before adoption; no claim about security, binary footprint or platform support follows from these four versions.

| Concern              | Verified source behavior                                                                                                      | Decision for nocx                                                                                                  |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| Agent loop           | `create_deep_agent` returns a Pydantic AI `Agent`; supports custom model/toolsets                                             | Keep the SDK loop, not a hand-written ReAct loop                                                                   |
| Default tool surface | Filesystem, todos, subagents, skills, memory, monitoring, web and history archive are enabled by default                      | Explicitly disable every competing tool/state owner; register only nocx's toolset                                  |
| Tool parameters      | JSON Schema advertisement alone does not validate arguments; `Tool.from_schema` can use an `any_schema` validator             | Go remains the schema validator before every effect; never count provider strict mode as authorization             |
| Execution order      | All tools marked sequential, or a run-scoped sequential mode, execute in model order; a pending tool future holds the barrier | Use a live pending future for approvals and egress; keep the whole run sequential                                  |
| SDK deferred tools   | Deferrals are collected after a batch; sequential execution does not itself stop at a returned deferral                       | Do not implement nocx approval using `DeferredToolRequests` or checkpoint replay                                   |
| Unknown tool calls   | Stock manager resolves names before validation hooks and has no equivalent of Eino's unknown-tool callback                    | Carry one small, explicit upstream extension, specified below; no hidden monkeypatch or weakened refusal semantics |
| Providers            | Native OpenAI Chat adapter and explicit provider/client injection exist                                                       | Python formats requests and parses SSE; Go performs guarded HTTP and holds secrets                                 |
| Checkpoints/archives | Upstream can store SDK messages in its own files; rewind does not undo effects                                                | Disable them. Keep nocx ledger history and process-local continuation semantics                                    |
| Compaction           | Additional summarization dependency/capabilities, with its own budgets and persistence behavior                               | Disable for this parity migration; preserve current history assembly and omission notice                           |
| Skills/workers/MCP   | SDK offers its own skills scripts, subagents and MCP clients                                                                  | Preserve nocx skills and supervised external workers. Do not enable independent SDK executors                      |
| Packaging            | Python package advertises Python >=3.10; inspected CI definition covers Ubuntu 3.10–3.13                                      | Bundle and prove a pinned CPython 3.13 runtime on nocx's actual three shipped OS/architecture slices               |

For the chosen scope, most of Deep Agents' extra harness features are intentionally off. The immediate benefit is changing the orchestration/provider foundation, not acquiring those features for free. The costs are a Python runtime in distribution, a private streaming IPC adapter, dual-engine regression coverage during rollout, and maintenance of the narrow unknown-call extension. A direct Pydantic AI integration would have fewer unused dependencies, but it is not silently substituted for the owner's selected framework.

### Current nocx behavior that remains authoritative

- `internal/assistant/assistant.go` defines `Client.Probe`, `Ask`, `Discard`, and `AuditSkill`; `AskEvent` has ordered `answer`, `reasoning`, and `toolCall` variants. Preserve the interface and event semantics.
- `internal/transport/ws_agent.go` owns admission, role/material resolution, pane history assembly, live run controls, approval handling, prose boundaries, command leases and terminalization. An engine change does not require moving those domain/application responsibilities to Python.
- `effectKernel.invokeClassified` owns validation, grant/policy/floor checks, fresh capability narrowing, durable-before-effect attempts, execution, result-contract checks and egress screening. `NewToolDispatcher` is worker-only and is not the experimental assistant's substitute for this kernel.
- An approval proposal binds run, attempt, tool, call ID and argument hash. An egress suspension holds bytes from an effect that already ran. Resume must retrieve those bytes from the shared approval store, never rerun the effect to reconstruct them.
- Current conversation context is ledger-derived, pane-scoped, trimmed by `conversationBudget = 12000`, and includes an explicit older-turn omission notice. SDK messages are live execution state, not a second durable transcript. Reasoning is live-only.
- Current continuation/checkpoints are process-lifetime. Store startup interrupts unfinished assistant runs. New runtime process loss must not invent restart replay.
- Endpoints currently use the `openai-compatible` wire schema. Preserve manual model names, `noKey`, custom base paths, literal/secret headers, Vault resolution and existing role precedence/fallback rules. Do not add provider presets or native protocols.
- Current strict `files.*` capabilities are local; commands and session reads use their existing local/SSH routes and limitations. Do not advertise newly remote-aware strict editing. Python never executes a shell or connects to SSH; the existing host tools decide what is reachable.
- The policy classifier is implemented but normal production `AskParams` does not wire its resolver. Preserve that activation state; replace its Eino-backed model port without silently enabling a new gate.
- MCP on main includes nocx's stdio bridge for external agents, not the unmerged configured-server assistant client in PR #474. Preserve existing external-agent integration; neither merge nor close that PR as part of this migration.

`DEEP_AGENTS_PLAN.md` is untracked user material in the original checkout and targets LangChain Deep Agents/QuickJS. Do not overwrite it or import its package pins, runtime design, proposed PR closure, goal scheduler, Plan/Edit, remote strict editor or expanded MCP/provider scope. The owner chose to evaluate those separately. v0.8 has shipped; this integration is now O4 of the owner-authorized `docs/milestones/v0-9.md`, alongside unchanged busy-terminal carryover. The charter is not a release-date promise.

### Binding architecture

AD-1 keeps the existing terminal/control planes. AD-2 keeps PTY/session/config in Go. AD-4 keeps SSH in the existing pool. AD-6/7 preserve backend identity and authenticated lifecycle authority. AD-8 requires separate registered engine implementations and one owner per domain behavior. [ADR-zkvkzrzc](docs/decisions/zkvkzrzc-an-experimental-loop-shares-the-go-owned-run-authority.md) supersedes ADR-0028's exclusive Eino choice for this experiment; its grant/narrowing requirements remain. Keep the ordered turn-tree authority of ADR-0040 and effect/resource and descendant-pane constraints of ADR-0053/0067. Do not introduce the interpreter carrier governed by ADR-0045.

## Approach

### 1. Establish a reproducible runtime and prove the two difficult SDK seams

Create the new `agent-runtime/` Python project, with package `nocx_agent_runtime`, because no Python assistant runtime exists in the inspected tree. Create `third_party/pydantic-ai/` for the source pin, patch and license/provenance of the single required dependency extension. It is not a second agent implementation.

Use CPython 3.13. At the initial lock step resolve the newest non-prerelease 3.13 patch available for **all** darwin/arm64, darwin/amd64 and Linux/amd64 release artifacts; record the exact interpreter version, artifact URLs and SHA-256 values in `agent-runtime/runtime-manifest.json`. Resolve build tooling once and record its exact version/hash too. Later builds consume those values, never `latest`. This is a deterministic selection rule, not permission to vary Python per platform. No system Python or runtime package installation is allowed in the shipped path.

Pin the framework source SHA and the investigated dependency candidate, with these direct constraints: `pydantic-deep==0.3.50`, `pydantic-ai-harness==0.52.0`, `pydantic-ai-slim[openai]==2.52.0`, `openai==3.19.0`, `httpx2==2.12.0`. Resolve the remaining declared requirements into `agent-runtime/uv.lock`; record upstream source/wheel correspondence and native artifact hashes. Build the patched Pydantic AI distribution from the pinned 2.52.0 source with a local `+nocx.1` version and lock that artifact; ensure the harness's dependency is satisfied by that local build. Do not delete upstream's required dependencies merely because their features are off. Do not install CLI, sandbox, browser, MCP, Google, Logfire or `all` extras.

If the exact candidate does not resolve or build on a supported target, keep the feature unavailable and report the concrete dependency/platform conflict. Do not silently select a different major/minor SDK, drop a platform, install a global interpreter, or fall back to a hand-written loop. This is a failed adoption gate, not a partially delivered replacement.

Implement and exercise a **small pinned Pydantic AI extension**:

- Add optional `unknown_tool_handler: Callable[[RunContext[Any], ToolCallPart], Awaitable[Any]] | None` to `Agent` construction and pass it to the run's `ToolManager`; preserve it in `ToolManager.for_run_step`.
- For a name absent from the prepared tool map, `validate_tool_call` creates an unresolved `ValidatedToolCall` only when that handler exists. Do not invoke the handler in upfront validation and do not charge a model retry. Preserve the original `ToolCallPart`, including ID and raw arguments.
- At `_execute_tool_call_impl`, before the assertion that a tool definition exists, route this unresolved record to the handler. The normal SDK executor still owns ordering, result correlation and the next model step. Use run-scoped sequential mode so even unknown calls wait behind a parked known call. No such definition appears in `tool_defs` or the provider's offered catalog.
- Missing handler preserves upstream behavior. A configured handler failure aborts the run; it must not become an automatic retry. Return values pass through the normal `ToolReturnPart` construction with the original call ID/name.
- nocx's handler only requests the host's existing unknown/withheld-tool response. It cannot invoke an undeclared effect. Extract that pure policy-aware response from `callsCarrier.UnknownTool` and reuse it for both engines.
- Keep upstream loop, SSE and ordinary tool executor intact. Package the source patch with its focused tests; never mutate installed site-packages at runtime. Seek upstream adoption independently, without making a remote merge prerequisite.

For **known** tools, install a side-effect-free validation capability which captures `ToolCallPart.args`, checks identity and raises the supported `SkipToolValidation(validated_args={})` to defer validation to Go. Tool execution uses the captured original representation, not the empty dictionary. This preserves Go's serial validation/attempt ordering even when later arguments are malformed; `any_schema` alone still parses JSON upfront and is insufficient. A response-level capture hook rejects duplicate IDs before effects; no argument-mutating SDK capability is enabled. The current Chat adapter supplies string arguments; if a required fixture supplies a decoded object, serialize it once without lossy numeric conversion and bind host approval to those received bytes. Unsupported representations fail before any effect, never receive a guessed repair.

Before proceeding, run the real factory against a local scripted OpenAI-compatible HTTP server and the real Go kernel bridge. It must show: unknown name → existing nocx controlled result → final answer; malformed later call does not erase an earlier permitted effect; first call parked for approval → second effect still absent; approval resumes exactly once; egress approval releases retained bytes with no second effect. Do not approve an SDK version from import success alone.

### 2. Expose the existing host services without duplicating them

Keep Eino-specific orchestration in its current implementation. In `internal/assistant`, extract construction shared by `client.Ask`/`newPolicyMiddleware` into a host factory; both engines use the same implementation of authority and framing. Proposed new file: `internal/assistant/host.go`; it is a wrapper around existing owners, not a new dispatcher.

New host-facing contracts:

```go
type InvocationKind string
const (
    InvocationToolOutput InvocationKind = "tool_output"
    InvocationNocxMessage InvocationKind = "nocx_message"
)
type InvocationResult struct {
    Text string
    Kind InvocationKind
}
type RunHost interface {
    Tools() []agenttools.Tool
    ModelTools() []agenttools.Tool
    InvokeClassified(ctx context.Context, name, callID, rawArgs string) (InvocationResult, error)
    UnknownTool(name, rawArgs string) (string, error)
    FrameForModel(tool, result string) string
}
type RunHostFactory interface {
    NewRunHost(ctx context.Context, p AskParams, onCall func(ToolCall) error) (RunHost, error)
}
```

`NewRunHost` applies the existing floor, grant, registry projection, approvals, known-material scanner, attachments, run/attempt identity, pane access binder, snapshots and domain seams currently wired in `client.Ask`. It must not expose those objects over IPC. No grant means an empty offered tool set and no effect-capable dispatch. Missing known-material protection continues to fail closed. `InvokeClassified` wraps the existing `modelResult` classification and preserves typed approval/egress errors. Framing uses the current framing owner; do not XML-wrap a refusal as untrusted tool output or frame a tool result twice.

`Tools()` is the complete grant-admitted executable catalog; `ModelTools()` is its current lazy visible projection. Register the former with the SDK and filter only the provider request definitions to the latter before each model request. Eino already executes guessed names that are grant-admitted but not yet visible; treating those as unknown would change current behavior. The unknown-tool callback remains refusal-only.

Expose the existing guarded model HTTP client through `ModelHTTPFactory`, rather than copying `newGuardedHTTPClient`. Both Eino and the experimental transport use the same factory with the existing recorder and custom-header context. Keep actual secret/header material in Go's operation lifetime.

Separate auxiliary **text generation** from its Go business logic:

```go
type ModelTarget struct {
    Key credential.Secret
    BaseURL string
    Model string
    Headers []Header
}
type TextGenerator interface {
    Generate(ctx context.Context, target ModelTarget, messages []Message) (string, error)
}
```

Provide an Eino implementation using existing `buildModel` and a Pydantic implementation using the private HTTP bridge. Change `DraftSkill`, `draftSkillGenerate`, `draftSkillWithHeaders`, `SkillDraftRequest.arguments`, `auditBundle`, `readOneFile`, `readEachFile`, `concludeFromNotes`, and `classifierEngine` to consume this port and plain `Message` values instead of Eino model/options. Resolve target material in the same existing owner; keep strict JSON parsers, quotation checks, memoization, reading budgets, file fan-out and partial failures unchanged. Eliminate the now-obsolete `skillDraftHTTP` seam; pass the selected generator through the run-host factory. No new model prompt, fallback or automatic classifier activation.

Find all affected call sites with these exact repository searches before editing (Go LSP was not available during analysis): `DraftSkill\(|draftSkillGenerate\(|draftSkillWithHeaders\(|NewSkillDraftRequest\(|\.arguments\(|auditBundle\(|readOneFile\(|readEachFile\(|concludeFromNotes\(|newClassifierEngine\(|skillDraftHTTP` under `internal/assistant` and `internal/transport`. Update the concrete tests to the neutral port, keeping behavior assertions. Keep the Eino adapter because it remains a live rollback implementation, not a compatibility alias.

### 3. Run the new SDK behind a private, bounded transport

Create `internal/assistant/pydantic/` implementing `assistant.Client` and `assistant.TextGenerator`. No equivalent subprocess-backed assistant implementation exists. It receives `RunHostFactory`, `ModelHTTPFactory`, the shared registry and runtime locator by constructor injection from `internal/app/app.go`. It does not import an Eino loop or call Eino's `Ask`/`buildModel`.

Run one child process for each active experimental Ask. Its live task survives host approval pauses, but not termination. Probe and a complete skill audit each own a short-lived operation process; the audit can make its existing bounded concurrent model calls within that process. A draft/classifier call nested inside Ask uses the same run's process as an independent model RPC, not a second agent run. IPC reading must continue while the main tool callback is parked, so a Go-side draft waiting for a Python generation cannot deadlock the worker's reader.

Launch an absolute packaged interpreter with isolated mode (`-I -B`), a controlled environment, no inherited provider/OTEL/Logfire variables, no project working directory or project import path, and inherited private pipes. Stdin/stdout carry the bridge only; stderr is consumed as bounded structured diagnostics through nocx logging. No TCP listener, renderer credential, cloud account, SSH connection, package installation or model-authored Python execution is introduced. This is trusted SDK isolation and cancellation containment, **not an advertised OS sandbox**.

Use a 4-byte big-endian length followed by UTF-8 JSON frames. Protocol version is `1`; every envelope has `version`, `kind`, `operationId`, `epoch`, `seq`, `requestId`, and `payload`. Operation/epoch IDs originate in Go. Requests use separate `g:<counter>` and `p:<counter>` namespaces for Go and Python respectively, unique within the operation/epoch; register valid incoming requests before dispatch and require replies to match an outstanding request. Notifications have a null request ID and a declared event kind. The bridge verifies the process/operation binding and a monotonic per-direction sequence. Handshake checks package manifest digest and bridge version. Unknown kinds/fields/versions, duplicate/reused requests or replies, cross-operation IDs, out-of-order chunks or oversized lengths fail the operation closed.

Use explicit frame kinds: `hello`, `start`, `catalog.request`, `catalog.result`, `tool.invoke`, `tool.result`, `model.open`, `model.headers`, `model.chunk`, `model.end`, `model.close`, `generate`, `generated`, `event`, `complete`, `cancel`, `error`, `object.chunk`, `object.commit`. Object frames identify their logical message kind and request ID, total byte length, ordinal and final digest; only a valid commit dispatches the reconstructed message. They are not HTTP-stream chunks. An approval park is a pending `tool.invoke`, not a `tool.result` or SDK exception. Go returns its existing typed suspension to transport and leaves that request unresolved. Resume attaches a new drive and re-invokes only the retained proposal through a fresh `RunHost` before resolving it.

Transport bounds: 1 MiB per JSON envelope, 64 KiB raw tool arguments (existing bound), 32 KiB HTTP byte chunks, 4 MiB maximum reassembled logical object, and 256 KiB queued bytes per stream. `model.open` alone permits 5,657,944 encoded bytes: base64 for a 4 MiB decoded HTTP body plus 64 KiB request metadata. Go independently enforces the decoded body limit; other kinds retain the 4 MiB bound. At most one partial object per direction is retained. Objects larger than one frame use identified chunks with total byte length, ordinal, final SHA-256 and final commit; do not publish a partial object, and finish an already-started logical send before propagating cancellation. HTTP response streams are not reassembled. Pause reads/writes for backpressure and cancel a stalled operation through its owner; never silently truncate a strict-edit snapshot or model history. These are IPC representation bounds, not claims about the SDK's token window or total process heap. A legal existing payload larger than the bound is an explicit compatibility failure to resolve before rollout, not permission to narrow a tool contract.

Construct the factory with explicit nocx instructions, the one nocx toolset, `workspace=False`, `output_type=str`, `end_strategy="graceful"`, `retries=0`, `context_manager=False`, `eviction_token_limit=None`, `patch_tool_calls=False`, `stuck_loop_detection=False`, `cost_tracking=False`, `thinking=False`, `forking=False`, `tool_search=False`, `web_search=False`, `web_fetch=False`; set every `include_*` feature false, including filesystem/execute/todo/current_todos/subagents/builtin_subagents/plan/skills/memory/teams/monitoring/checkpoints/improve/liteparse/history_archive. Pass no MCP servers, extra hooks, context files/discovery, message queue, reminders or provider-native tools. Register the nocx capture/validation capability and patched unknown-tool handler described in §1. Mandatory factory/SDK infrastructure remains, including `_PendingUploads`, ToolSearch and PendingMessageDrain; prove it exposes no additional model tools. Do not pass `fallback_model` or `instrument` to the factory: they are forwarded to an Agent constructor that does not accept them. Do not construct `FallbackModel`. Set the returned agent's public `instrument` property to `False`, keep a sanitized environment and supply empty `DeepAgentDeps()` on each run. Supply no uploads or workspace.

`thinking=False` here disables the factory's automatic thinking-effort capability, **not the reception/display of reasoning returned by a model**. Use the existing system prompt and user messages once, without duplicating the current question in history and `agent.run` input. Preserve actual SDK output part identities, but emit only nocx's approved events. Go is the sole producer of visible `toolCall` events; SDK validation notifications are not evidence a call ran.

### 4. Preserve provider behavior through the native Python adapter and Go HTTP

Use `OpenAIChatModel(configured_model, provider=OpenAIProvider(openai_client=sdk), profile=...)`, where `sdk` is explicit `AsyncOpenAI(api_key="nocx-placeholder", base_url="https://nocx.invalid/v1/", http_client=httpx2.AsyncClient(transport=GoIPCTransport(...), trust_env=False, follow_redirects=False), max_retries=0)`.

The synthetic URL is never dialed. The custom transport permits only the Chat Completions operation and sends bytes over IPC. Go binds each model request to its already selected `ModelTarget`; Python cannot select an endpoint URL, key, proxy, role or credentialed arbitrary route. Validate method, synthetic path, configured model, declared tools and absence of provider-native tools before adding secrets. Go derives the real request URL from the configured API root using the same base-path semantics as the old adapter. Python-supplied authorization, cookies, organization/project/admin and hop-by-hop headers are discarded. Preserve content type/accept and the minimal SDK protocol headers; inject the existing nocx-configured custom headers and key only in Go, with the current custom-header precedence and origin-change stripping rules.

The bridge returns response status, relevant non-secret headers and a live `httpx2.AsyncByteStream`, not a buffered completion. Its `aclose()` sends `model.close`; Go cancels the HTTP context and closes the body. Worker death closes all outstanding model requests. Preserve bounded error bodies for SDK classification, then map framework errors into nocx's existing safe failure categories; raw SDK exceptions/provider bodies never become UI messages.

Supply a deliberate generic Chat profile rather than model-name-based OpenAI capability guesses for a custom endpoint: no native tools, no forced structured-output tool, no schema-rewriting transformer, no automatic thinking effort, no context-window inference, and ordinary system-role prompts. Preserve canonical registry schemas. Keep `reasoning`/`reasoning_content` reception and current live reasoning/answer ordering. Override the native model's documented response-text mapping only to keep literal `<think>...</think>` text as text if the old adapter does so; do not add a generic reasoning parser. Fixtures decide byte-compatible request settings (including whether `stream_options`, strict/tool-choice fields or reasoning send-back are emitted) against the existing adapter; changes that a current endpoint rejects block parity rather than causing dialect guessing or fallback to Eino.

Connection-only Probe and `/models` discovery reuse the existing Go implementation because they do not run an agent or require an SDK. Model Probe uses the actual selected experimental model/stream path, declares no tools, retains the 30-second bound and existing distinction between outcome failure and invalid parameters. Audit and draft use the selected `TextGenerator` through their existing Go workflow; the audit's fan-out remains four and its budget remains whole-operation. Missing auxiliary roles retain only their existing, explicitly implemented fallback.

### 5. Preserve live suspension, result disclosure and cancellation

Use `ToolManager.parallel_execution_mode("sequential")` around the **actual Python run task**, and set `ToolDefinition.sequential=True` for every offered host tool. `get_tools` refreshes its projection from the current host drive before each model request. Catalog presence is not authorization: every invocation reaches Go again.

Retain each outstanding proposal by operation epoch, response ordinal, original provider call ID, canonical tool name and original argument representation. The shared `assistant.ValidateToolCallID` boundary rejects empty/whitespace-only identities and identities longer than 128 Unicode characters before any sibling effect; never synthesize or truncate a missing provider identity. A later explicit streamed ID is valid when the completed response has an unchanged original identity. Reject duplicate IDs in one response before effects and reject conflicting reuse across responses; an unchanged ID from a second SDK execution is not permission to replay a completed effect. Release completed batch payloads while retaining compact consumed-ID fencing. Only the explicit host resume of the outstanding pending proposal may reinvoke the kernel. All results are framed/screened on the Go side before entering the Python task.

When the kernel raises `ApprovalRequestedError` or `EgressRequestedError`:

1. Drain preceding prose/reasoning events in order, detach the current drive's event sink and return the same typed Go suspension to `ws_agent.go`.
2. Keep the Python run/event-stream task alive and its tool future unresolved. Do not close the SDK stream context, raise an SDK deferral, serialize its heap or hold a Go DB transaction/mutex over the wait.
3. Existing transport approval logic applies the user's answer and remints the grant. A subsequent `Ask` drive for that run attaches a fresh event sink and host kernel using the same shared approval store and original proposal binding.
4. Revalidate policy, file/expansion freshness and capability scope through that kernel. Policy decline returns the existing nocx refusal; egress decline terminates. Approved egress retrieves retained bytes without another effect. Only then resolve the waiting Python future and allow later siblings.
5. Missing worker/checkpoint, changed epoch, argument conflict or terminalized run cannot be resumed; reject it and preserve the recorded outcome. Both engines require explicit `AskParams.Resume` for continuation, derived from transport's existing resume state. A missing continuation cannot start cold, and a fresh admission cannot take over a parked operation. Completed-request replay belongs to the durable ledger lookup, not an unbounded client-lifetime tombstone map.

The operation context is owned by the client/run, not by the temporary Go drive that returns on suspension. Keep it attached to the existing connection/run cancellation owner; a normal suspension return is not cancellation. `Discard(runID)` fences further model/tool/event traffic, cancels the Python task and Go requests, closes pending futures and drops snapshots/approval-held material through their existing owners. Allow up to two seconds for cooperative child shutdown, then kill and reap that process. Never kill a different run's process, a user command or the pooled SSH connection.

Add `AskParams.OnRuntimeLost func(error)` for the one lifetime event the synchronous `Client` interface cannot carry: a worker dying after `Ask` has returned a suspension. Transport binds it to the original run before the first drive. The supervisor dispatches every unexpected process/protocol failure independently through that callback, whether the drive is active or parked; `agentRunControl.beginTerminal` arbitrates it against normal return, approval and Stop. Never decide ownership from an unsynchronized “Ask is active” flag, and never call the callback synchronously from a supervisor `Discard` waits to reap. Intentional shutdown suppresses unexpected-loss notification. Late suspension/approval transitions cannot reopen a terminalized run. The Eino implementation does not emit this event because it has no separate runtime to lose.

Publish a typed `RuntimeLostError` before cancelling the operation. A failing active `Ask` preserves that published cause under the operation mutex, so both its ordinary return and `OnRuntimeLost` carry run-owned lease cleanup into terminal arbitration. Otherwise cancellation can win first, record a generic failure and leave a quiet-parked command alive. A successful completed run does not retroactively acquire the loss cause; a late callback cannot kill its continuing command. Pending Stop retains the existing `cancelDone` arbitration.

Runtime-loss settlement registers with the existing control-plane inflight owner and runs inline on the client's callback, not in another transport goroutine. Admitted settlement participates in the bounded shutdown join. The shutdown fence refuses later callbacks before they can write the ledger; the existing interrupted-run sweep remains the recovery owner.

Worker crash, malformed bridge traffic, event-callback error, socket disconnect or explicit Stop enter the existing terminalization funnel exactly once. Do not retry the Ask automatically and do not report a dispatched effect as 'not run' merely because its acknowledgment was lost. Existing lease owners settle committed/partial/unknown command outcomes. On coordinator restart, use the existing interrupted-run sweep; later explicit questions rebuild context from the ledger, not pending SDK work.

### 6. Add experimental selection and durable engine ownership

Use the typed Settings registry, not an environment-only toggle or a second preferences store. Register `AssistantExperimentalAgent` with key `assistant.experimentalAgent`, `Section: "Answers"`, `DataClass: PublicConfig`, default `false`, label **"Pydantic agent (Experimental)"**, description **"Use the experimental Pydantic engine for new assistant operations. Active operations keep their engine; use Stop to end them. Turn this off to use Eino again."** The existing generated Settings control and UI kit render it.

Add engine metadata and a selector outside the implementations:

```go
type Engine struct {
    ID string // "eino" or "pydantic-deepagents"
    RuntimeVersion string // Eino version, or complete packaged runtime manifest digest
    Client Client
}
type EngineSelector interface {
    Selected(ctx context.Context) (Engine, error)
}
```

The application composition root registers both clients; the selector reads the setting and chooses one. No switch inside either engine and no automatic runtime fallback. A flag read error is a visible unavailable result, not silent selection. The experimental client/locator is lazy: constructing the app or serving flag-off Ask/Probe/Audit never starts or validates Python.

Replace transport injection `WithAssistantClient` with `WithAssistantEngines(EngineSelector)` and migrate every caller, including test harnesses, using the exact search `WithAssistantClient\(|assistantClient|assistant\.Client` under `internal/transport`, `internal/app`, `cmd`, and `main.go`. Tests that need one engine use a fixed test selector. Preserve `assistant.Client` itself; a selected `Engine` is an operation binding, not another model API.

For a **new** Ask, resolve and pin `Engine` in `askRunContext` before enqueueing, after the replay lookup below. Call that client's `Ask` and `Discard` on every drive and terminal path. Probe and audit select once at their handler's admission and retain that binding for their entire operation. Draft/classifier generation inherits the enclosing run's selected implementation. Turning the flag off changes **new** operations only; an already parked Pydantic run can be approved or stopped through its pinned client. This makes rollback precise without aborting work the person did not ask to stop.

Add `Engine string json:"engine,omitempty"` and `RuntimeVersion string json:"runtimeVersion,omitempty"` to durable `content.RunFacts`. New runs always write both. Missing historical engine means Eino; unknown nonempty engine is not normalized to Eino. Keep `Mode == "explain"` unchanged. Extend `AgentAskResult` with persisted `Facts RunFacts` and `State RunState` so both initial admission and idempotent replay return original metadata/state. Read these in the existing SQL transaction; no second database or table is needed.

Fix the directly affected replay path before adding selection. Add `LookupAgentAsk(ctx, in AgentAsk) (AgentAskResult, bool, error)` to the existing ledger repository and expose it as `AgentService.LookupAsk`. Share the existing idempotency lookup/digest helper with `SubmitAgentAsk`; do not implement another digest in transport. After validating the request and binding its existing client/session/environment identity, perform this read **before** resolving a current role, engine, runtime or grant. A matching row returns persisted run/entry/state/model/engine/version even if the current role was removed or the experimental runtime is unavailable. A conflicting key returns `ErrIDConflict`. A missing row takes the normal admission path; `SubmitAgentAsk` retains its transactional recheck so concurrent admissions still create exactly one run. Migrate `LedgerRepository`, its existing stub/test doubles, `capability.AgentService` and the guarded service implementation together.

Whether found by the early lookup or the transactional race check, a replay never replaces `pendingRuns`, constructs a new draft, starts a worker, reruns the model or enqueues a second stream. Keep ask idempotency's digest independent of current flag/endpoint selections. The early read does not weaken authentication or the existing session/client binding; it removes only configuration dependencies irrelevant to an already admitted ask.

Add required `engine` (nonempty string) and `runtimeVersion` (string) to successful `agent.ask` and `endpoints.probe` results. Add both required fields to `agent.status` too, but allow null there when engine selection itself failed; report the existing settings-unavailable reason rather than guessing an engine. New selection emits only `eino` or `pydantic-deepagents`; a nonempty unknown stored engine remains representable and displays as an unknown engine, never Eino. Historical missing engine normalizes to Eino, and an unknown historical runtime version is the empty string, not a guessed package version. `ledger.get` currently exposes no run-facts projection: add required `agentRuns: []` for non-agent entries, and one `{runId, attempt, state, model, engine, runtimeVersion}` object per agent-lane execution for turns, derived from existing `LedgerEntry.Executions`/`RunFacts` without exposing endpoint material. Each object has explicit required properties and `additionalProperties: false`. For Probe, retain engine provenance in `ProbeStore`; `agent.status` only shows a last Probe for the selected engine as well as the currently resolved endpoint/model. A slow Probe finishing after a flag change cannot certify the new engine.

Readiness checks file/manifest availability without starting Python. Add answering reason `runtime-unavailable`; existing credential/role reasons keep their meanings and take precedence. The UI message is **"The experimental agent runtime is unavailable. Turn off Pydantic agent (Experimental) in Settings → Assistant → Answers."** No background download, install attempt or silent downgrade. A later spawn/import failure still fails its real Ask visibly; a file check is not a successful runtime probe.

Update the touched contracts, `contracts/openrpc.json` and generated renderer types together using the existing generator. Current coordinator `ProtocolVersion` is 7: the first combined wire cutover uses 8, or the then-current committed value plus one if integration has advanced. Generate its new protocol surface golden; do not reuse the source proposal's protocol 7. No helper/lifecycle protocol change is needed because this plan adds no remote execution protocol.

### 7. Keep one UI, one history and one tool vocabulary

Continue using the current Ask composer, answer blocks, approval UI, tool-action disclosure and Settings pages. `frontend/src/agent-ask.ts` receives the engine metadata alongside model identity. Use the existing answer notice/metadata facilities to show **"Pydantic agent · Experimental"** on experimental turns and their restored projections; do not create another chat surface or custom-repaint a kit control. Keep model identity separate from engine identity, so a model alias is not modified to smuggle in the label.

Handle replay responses explicitly in `AgentInputTarget.submit`: the current implementation ignores returned state/replayed and waits for notifications. Reuse the existing `restore-client.ts` turn/caused-child/artifact readers to populate the returned `entryId`, reuse an existing block for that identity and settle a terminal run immediately. For an in-flight replay, register/buffer live events before hydration and merge by run/block/action identity, not by text comparison. Extend `ledger.artifact` with required nullable `lastSeq`, the maximum actual chunk sequence included in **that same body snapshot**. Extend `sqliteContent.Artifact` to read `seq` with `body` and obtain metadata/chunks in one read transaction; it currently discards sequence and reads metadata separately. Do not derive `lastSeq` from chunk count because prose sequences are run-global and noncontiguous per block. Apply only buffered deltas above the hydrated block's `lastSeq`; deduplicate action IDs and retain later terminal notifications. Fetch bodies per artifact as today, never inside `ledger.get`. After hydration re-read metadata if a terminal notification arrived during the read, so final children and terminal state cannot be missed. Restore and replay use this one reconciliation path; a replay cannot reopen a completed turn or append its prose twice.

Keep `agent.runDelta`, `agent.runReasoning`, `agent.runToolCall` and their ordering intact. Reasoning stays live-only. Only kernel-authorized execution emits a toolCall; a parked, malformed or refused proposal is not drawn as executed. Commands continue to occupy their own existing block instead of a duplicate action row. Lazy result expansion continues through the ledger's existing screened artifacts.

Keep the existing 12,000-byte prior-history rule, attachments and omission notice in both engine paths. Do not add SDK messages.json, MEMORY.md, file checkpoint stores, compaction, conversation forks, automatic skill discovery or context-file scanning. Do not make concurrent asks overwrite a shared Python conversation: every run starts from its existing pinned host context, and live Python state is private to that run.

Expose every currently executable `agenttools.Registry` tool under the same grant, resource checks and exact schema. The model-facing name translator owns both directions. Where provider-compatible encoding is required, encode names as `nocx_` plus lowercase hex of their UTF-8 bytes; enforce the provider's 64-character limit at catalog construction and treat any current registry name that exceeds it as a compatibility blocker, not a dropped tool. Canonical names stay in Go approvals, ledger and UI.

Keep two distinct lookup purposes: the current admitted catalog permits routing a known invocation to the Go kernel; the complete trusted registry permits recognition only for unknown/withheld-call refusal classification. A registered but withheld encoded name reaches the existing canonical refusal policy, never an executable tool. A name absent from the trusted registry stays unknown; decoding a plausible spelling never creates authority.

Render model-facing nocx instructions, cross-tool descriptions and host-owned refusal responses through that same translator, so an attached-screen instruction and the permitted-tool list name tools the model can actually call. Translate structured tool references or trusted template slots, not arbitrary text: user messages, attachments, argument strings, tool-result bytes and retained history remain unchanged. Do not globally replace tool-looking strings inside those values. Add acceptance cases for an unknown name, an encoded withheld name, an attached-screen instruction and a cross-tool description; all preserve canonical ledger/approval identity.

Preserve current tools for session reads/commands/leases, strict local files, guarded fetch, notes/snippets, skills and supervised workers. Keep declaration-only capabilities unadvertised and existing SSH refusals honest. Do not reinterpret the runtime child process as a `workers.*` participant or deploy Python to remote hosts.

### 8. Deliver the runtime with the app and the surviving coordinator

Extend the existing release/package paths, not an on-first-Ask downloader. The manifest records the framework SHA, resolved dependency versions, patched source hash, interpreter/build-tool hashes, target triple, bridge version and each shipped file digest. Runtime artifacts include licenses for the actual locked graph. Missing platform wheels/native dependencies fail packaging rather than obtaining them on the user's machine.

Package architecture-specific runtime directories for darwin/arm64 and darwin/amd64 under the universal app's Resources; select using the coordinator's actual architecture. Package Linux/amd64 at the existing glibc 2.35 floor. Sign Python executables and native libraries inside-out before signing the macOS bundle, preserving the release workflow's existing coordinator/app order.

The Linux coordinator outlives the AppImage mount, so an in-image runtime path is invalid after the launcher exits. Extend `internal/update/serverbin`'s versioned, content-addressed installation discipline to the runtime tree. The **launcher stages the runtime while the AppImage is still mounted, before coordinator launch or launcher exit**, even when the experimental flag is off; this is package installation work, not an Eino Ask dependency. Bind the installed manifest identity into the coordinator's locator. A staging failure is nonfatal to ordinary Eino operation and exposes experimental unavailability rather than preventing app start. Add an explicit tree installation operation rather than repurposing the remote-helper deployer: copy into a private staging directory, reject escaping symlinks, verify every manifest digest, fsync files/directories, and atomically rename to a directory keyed by the manifest digest. Do the same content-addressed runtime staging for macOS so an in-place app replacement cannot remove a live run's runtime. Never prune a runtime referenced by a live coordinator/worker; on startup prune only owned, manifest-valid unused runtime versions. A failed copy leaves the previous valid runtime intact but does not pass it off as the expected new manifest.

The runtime is installed from trusted packaged bytes, never fetched at runtime. Installation may stage Python bytes with the flag off; no Eino Ask/Probe/Audit triggers extraction, Python start or import. Standalone server distribution carries the matching staged tree, and `make dev-web` resolves the same build artifact through the injected locator. Do not bundle Python into `nocx-helper` or enlarge remote deployment/consent scope.

### 9. Roll out without claiming premature Eino removal

The implementation dependency order is: runtime/seam proof → shared host/model ports → bounded bridge plus provider adapter → live continuation/tool parity → selector/provenance/UI → packaged-platform acceptance. Track three acceptance stages: `nocx-g9giy.2` proves adoption; `nocx-g9giy.3` delivers the complete existing UI path on the selected engine; `nocx-g9giy.4` proves packaged delivery. Shared ports are a prerequisite inside the end-to-end stage, not a separately accepted invisible feature. Packaging preparation can proceed independently once the runtime layout is fixed; shared composition/wire integration has one owner.

Enable the switch only after the complete experimental path is wired, never for a compiling scaffold. Ship it default-off with truthful experimental labeling. Flag-on model work must not call Eino even for audit/draft; flag-off stays independently usable. No automatic population of different roles/endpoints, migration of endpoint records or automatic promotion to default-on.

Retain Eino and its imports while the switch is offered. Removing that working implementation now contradicts the requested trial period. Final removal requires a later explicit decision to end rollback, after the same acceptance matrix passes on the new engine; this plan neither schedules that decision nor leaves an Eino dependency on the selected Pydantic execution path.

Before retained implementation, use `to-backlog`/`to-spec`/`to-design`/`to-stages` and the repository integration to admit the independent requested feature with these acceptance scenarios. Do not claim or relabel unrelated deferred work or alter the approved release-pipeline charter. Draw a new ADR identifier for the experimental loop and its narrow dependency patch; accepted ADR text is immutable.

Keep proof-only launchers disposable until the real composition is reachable; a retained Go package lands with its wiring, not as a dead scaffold. After behavioral smoke succeeds, update the existing architecture/assistant/developer setup documentation and changelog with the flag, bundled runtime, provenance, no-fallback rule and accepted platform evidence. Publish only this feature's tracker/code delta through the integration and preserve implemented/submitted stages until their acceptance is actually witnessed.

## Unreleased change record

- Added a default-off **Pydantic agent (Experimental)** switch beside the existing Eino path. Both engines use the same endpoint/role settings and Go-owned tool, policy, approval, credential and HTTP boundaries. Selection is pinned per operation; there is no fallback.
- Added original engine/runtime provenance to live and restored answers. Replay reads the original ledger facts without restarting a model request, and Stop addresses the owning run rather than the currently selected engine.
- Added the pinned native SDK worker, narrow unknown-call dependency patch, frozen bundled CPython runtime, artifact inventory and licenses. Runtime staging and inherited leases outlive launcher exit and prevent pruning a live version.
- Added native runtime verification targets, cross-platform packaging wiring and real UI coverage. The control-plane cutover uses protocol version 8; no old-runtime compatibility shim is retained.

## Critical files and anchors

1. `internal/assistant/host.go`, `NewRunHostFactory`/`InvokeClassified` — one shared owner of host assembly, validation, approval, egress and effect ordering.
2. `internal/assistant/pydantic/{client.go,process.go,protocol.go}` and `agent-runtime/nocx_agent_runtime/` — the native SDK loop, bounded private protocol and run-owned worker lifecycle.
3. `internal/transport/ws_agent.go`, `handleAsk`/`runAskStream` — pin engine once, preserve context and continuation ownership, and never drive replay.
4. `internal/content/ledger_agent_sqlite.go`, `LookupAgentAsk` and the replay transaction — original engine/state/facts and the artifact watermark win over current configuration.
5. `internal/update/serverbin/{tree.go,tree_prune.go}` and `internal/assistant/runtimebundle/` — verified staging and inherited leases keep the runtime independent of an AppImage mount or replaced application.

## Verification

### Behavior acceptance matrix

Run the common current-behavior cases against both registered engines. New tests must exercise behavior through `assistant.Client`, the real socket or product UI, not test source text or mock forwarding. The provider can be the existing scripted HTTP/SSE fixture; the Pydantic SDK process, bridge, Go kernel and ledger must be real.

| Check                       | Input/action                                                                                                                                                                                | Observable result                                                                                                                                                                                                                                                                 |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| V1 — Complete replacement   | Select Pydantic; Ask, model Test, audit a multi-file skill, then explicitly save a drafted skill                                                                                            | Every model request uses the Python native adapter and Go HTTP bridge; no Eino model construction; Go validators/approval still decide writes                                                                                                                                     |
| V2 — Rollback isolation     | Runtime missing/corrupt; flag off; Ask/Test/audit                                                                                                                                           | Eino works; no Python start, extraction or import; flag on reports runtime unavailable without fallback                                                                                                                                                                           |
| V3 — In-flight selection    | Pause a Pydantic run for approval, turn flag off, start a new Ask, then approve/Stop the old run                                                                                            | New Ask uses Eino; old continuation uses Pydantic or is stopped; no engine transfer or duplicate effect                                                                                                                                                                           |
| V4 — Ordered suspension     | Model proposes A requiring approval then B creating a sentinel file                                                                                                                         | Before A's answer B has no effect; allow resumes A once then B; decline returns existing refusal semantics; Stop prevents both pending effects                                                                                                                                    |
| V5 — Egress                 | Effect increments a counter and returns known-secret bytes                                                                                                                                  | Counter increments once; neither Python history nor provider sees bytes before release; allow releases retained result; decline stops without rerun                                                                                                                               |
| V6 — Freshness              | Read file, propose hashline edit, change file/symlink/scope before approving                                                                                                                | Same existing stale/scope refusal, no overwrite; unchanged file successfully edits exact intended bytes                                                                                                                                                                           |
| V7 — Unknown/invalid batch  | Unknown or withheld tool, malformed/extra args, duplicate IDs, wrong process/epoch                                                                                                          | Controlled host refusal for unknown/withheld; real invalid known call fails as existing contract requires; no bypass/retry/hidden tool exposure; duplicate IDs fail before effects                                                                                                |
| V8 — Streaming              | Split SSE, reasoning and text in one chunk, multiple tool calls, reasoning-only final stream, truncated/error stream                                                                        | Existing answer/reasoning/action order, empty-answer failure, safe truthful errors; no tool result painted as answer                                                                                                                                                              |
| V9 — Provider/secret parity | Custom API prefix, arbitrary model alias, noKey, literal+secret headers, redirect, proxy, DNS change                                                                                        | Correct path/model; secrets only injected in Go; cross-origin stripping and existing HTTP policy enforced; no ambient Python credential or direct network path                                                                                                                    |
| V10 — History and replay    | Same askId after flag change, role deletion/unassignment or selected-runtime loss; terminal and in-flight renderer replay; restore unknown engine metadata; ask beyond prior-history budget | One run/stream/effect; no dependency on current model configuration; original metadata preserved; one hydrated block with no duplicate or missing deltas/actions; correct terminal state; unknown engine shown honestly; no restored reasoning; existing omission notice retained |
| V11 — Loss/cancellation     | Kill worker or cancel while streaming, waiting on approval, draft generation or command result; sever socket; restart coordinator                                                           | Exactly one terminal state; requests/futures/process reaped; only run-owned leases stopped; committed/unknown effects truthful; no automatic resume/replay                                                                                                                        |
| V12 — Tool breadth          | Current session, local strict file, guarded fetch, notes/snippets, skills and worker workflows; existing SSH routes                                                                         | Same enabled/disabled/granted behavior and bounds; host tools own effects; current remote limitations remain explicit, not silently executed locally by Python                                                                                                                    |
| V13 — Auxiliary consumers   | Audit with one failed file then all failed files; draft pause/resume; injected classifier resolver success/failure                                                                          | Existing partial audit report and all-failed behavior, one memoized draft, fail-closed classifier; production classifier remains unwired                                                                                                                                          |
| V14 — Packaged delivery     | Clean supported OS target without Python; start flag-off then exit AppImage launcher and enable the flag in the surviving coordinator; offline import/start; app update while worker live   | Runtime was staged before unmount and starts from the verified artifact; survives replacement; no first-use downloads; signature/license checks pass; failed runtime staging does not break Eino                                                                                  |
| V15 — UI reachability       | Open Settings → Assistant → Answers, enable experimental switch, Test model, ask from a selected terminal block, approve tool, Stop another run, restart and restore                        | Explicit experimental identity; existing controls actually reach new client; answer/result visible and correctly ordered; unavailable-runtime repair points to that setting                                                                                                       |

No speed, memory or model-quality improvement is promised. At packaged acceptance record added compressed/install size, cold process start, time to first provider request, peak RSS for one and multiple concurrent runs, and cleanup time, using the same hardware/fixture for Eino and Pydantic. Record model quality observations separately; a scripted endpoint proves integration, not reasoning quality. Do not add arbitrary performance thresholds or paid model benchmarks without an owner requirement.

### Measured Linux development artifact

Two descriptive trials ran 16 real Ask operations against the same held scripted provider on an AMD Ryzen 7 5700X (8 cores/16 threads), 67,337,543,680 bytes RAM, Linux 7.3.0-4-cachyos. Each operation started a fresh engine process where applicable; the filesystem cache was warm. The composed no-login backend SHA-256 was `550e01d63e5d42f110d683456282dc6057ed98a4525415d3a1a453410c6c3c61`; the runtime manifest digest was `b57910d6bb462f8ac45bccded63e6afcbc57a748b8e122658ef2f60b05265183`.

| Engine   | Concurrent Ask operations | Submit to first provider request | Sampled process-tree peak RSS | Python-only sampled RSS |
| -------- | ------------------------: | -------------------------------: | ----------------------------: | ----------------------: |
| Eino     |                         1 |                     13.5–14.2 ms |                   294–311 MiB |          Not applicable |
| Eino     |                         3 |                     19.3–29.4 ms |                   283–301 MiB |          Not applicable |
| Pydantic |                         1 |                   2,198–2,213 ms |                   361–364 MiB |             155–157 MiB |
| Pydantic |                         3 |                   2,242–2,385 ms |                   619–692 MiB |             389–471 MiB |

The first Python child was observed 158–178 ms after submission; child observation to provider arrival took 2.04–2.22 s. After releasing the held provider response, Python processes were gone in 202–219 ms for one operation and 284–296 ms for three. Eino terminal settlement took 14.7–23.6 ms; it has no Python cleanup interval. Fresh coordinator startup, including verified staging, took 1,405–1,406 ms. Coordinator shutdown took 10.7–10.9 ms; the second trial explicitly verified helper/shell cleanup in 21.2 ms. All observed fixture processes were absent at completion.

The runtime-only gzip archive was 60,423,726 bytes (SHA-256 `0fbfb7fb34cab40142b0158566bbe3004e7033ca3acc84e057ad1250e60deaca`). Its apparent installed size was 188,000,689 bytes; allocated bytes were 212,815,872 packaged and 208,523,264 staged. The composed Go binary was 93,950,857 bytes; a marginal Eino-only binary size was not isolated.

These are not production latency, continuous peak memory, PSS, model-quality or final installer-compression measurements. Sampling intervals reached approximately 11 ms; aggregate RSS double-counts shared pages and includes the coordinator, helper and three shells. Go heap baselines varied, Eino preceded Pydantic, and the shared host had background load without CPU pinning. Reproduction and raw measurements are recorded with `nocx-g9giy.4.3`; no benchmark framework or performance threshold is added.

### Packaged Linux acceptance

An unsigned `VERSION=dev` AppImage built from `2dc52b04302924c33be9648cfdbcc9eadc2cfbfd` is 210,577,912 bytes, SHA-256 `c5d82d72fedfeebc52f282ccf5ab2f32835fa8c325e82ee7df6f32dd49502952`. The isolated builder used Ubuntu 22, Go 1.26.10, Node 24.22.1 and WebKitGTK 2.50.4. Dependency installation used the frozen lock and a private offline npm download cache; no host `node_modules` or mutable build outputs were mounted.

The repository's `verify-appimage.sh` passed its complete default Arch matrix: the happy path and the `no-freetype`, `no-network-process`, `no-web-process` and `unpatched` refusal mutations. Arch had neither GTK3 nor WebKitGTK installed. The happy path observed the renderer connection, a terminal, both bundled WebKit helpers, a shell in disposable HOME and the COLRv1 output/title acknowledgement.

A separate run extracted that same AppImage and exercised its no-login coordinator under Docker `--network none`, with disposable HOME and a PATH without Python. Against a local scripted provider, flag-off Eino completed without a Python child; flag-on Pydantic completed through the profile-staged interpreter with `-I -B` and manifest digest `b57910d6bb462f8ac45bccded63e6afcbc57a748b8e122658ef2f60b05265183`. Full offline manifest verification and imports of the SDK and native dependencies passed. The extracted coordinator matched the built binary, SHA-256 `73f8139f432ee18645850356fd592a408bd5c8222cf3fb0b67ccf77a7ca27b74`.

Reproduction drivers and receipts are recorded with `nocx-g9giy.4.2`. This proves the Linux experimental payload is present and executable in the packaged artifact; it is not release publication, signing, model-quality evidence or native macOS acceptance.

### Native runtime acceptance

[CI run 40994079153](https://github.com/shady2k/nocx/actions/runs/40994079153), at `2dc52b04302924c33be9648cfdbcc9eadc2cfbfd`, passed the packaged-runtime and host-authority checks natively on Linux/amd64, macOS/amd64 and macOS/arm64. The `ci-mac` job, both variants of the OS-specific `ci-linux` job, and all four Chromium/WebKit e2e shards also passed. These are component results, not a claim that the whole run was green. They prove native execution rather than only cross-built bundles; release signing and notarization remain separate release checks.

### Commands and execution ownership

These are verification entry points, not claims that every platform gate has passed. Stage acceptance records hold the executed commands and results. Work from the repository root unless a directory is named.

- Build the frozen target with `make agent-runtime`. On Linux/amd64, `python3 scripts/package-agent-runtime.py --target linux-amd64 --check-lock` uses the pinned uv and bundled CPython to refuse lock drift; select the matching target on macOS. It does not depend on an unpinned host Python resolver. No install occurs on the user's first Ask.
- `make ci-agent-runtime` runs the packaging regression tests, frozen-lock check, bundled-interpreter `unittest` suite and required native Go runtime/installer tests. The Python tests exercise the patched dependency hook and actual sequential pending-future behavior, not only bridge mocks.
- `internal/assistant/pydantic` behavioral tests launch the packaged runtime against `httptest` and real host/kernel fixtures. Worker verification uses affected packages within `go test -tags gtk3 -race ./internal/assistant/... ./internal/transport ./internal/content ./internal/settings ./internal/update/serverbin`. Existing approval, egress, freshness, answerstream, probe, audit and draft assertions remain meaningful. Use Linux's gtk3 tag only on Linux; macOS does not need it.
- Regenerate touched wire contracts from `frontend`: `npm run contracts`. On the combined wire cutover: `NOCX_PROTOCOL_SURFACE_UPDATE=1 go test ./internal/coordinator/ -run TestTheProtocolSurface` after incrementing the version; verify normal `go test ./internal/coordinator/ -run TestTheProtocolSurface` without update mode.
- Frontend worker checks from `frontend`: `npx vitest run src/agent-ask.test.ts src/agent-status-line.test.ts src/settings.test.ts src/restore-client.test.ts` with the repository-installed toolchain and updated actual affected-file list. Validate UI behavior in a real browser, not only these tests.
- `e2e/agent-engine.spec.ts` reuses `e2e/agent-ask.spec.ts`'s `VaultBackend`, disposable HOME and `FakeOpenAI` pattern. It drives the default engine, native endpoint Test, in-flight switching, approval, Stop and restored provenance through real controls. The coordinator, not branch workers, runs `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/agent-engine.spec.ts` and the existing assistant suite on the merged tree. Missing-runtime isolation has its own composed acceptance scenario; the switch spec alone does not prove all of V2.
- Manual smoke uses a composed `cmd/nocx-server` built **without** `nocx_login_session`, the existing isolated-home/stand discovery helpers, Vite and a scripted endpoint. Exercise V15 through actual browser controls, inspect streamed answers and restored turns, then close the browser and stop owned processes. `make dev-web` is an interactive login-session build, not an automated-isolation shortcut. Do not set `NOCX_WS_PORT`, touch the released profile or reach the OS keystore. For SSH parity use the existing authorized SSH fixture/session, not a shell-spawned substitute.
- The integration coordinator runs `make ci-full` once on the merged tree and release-artifact verification on Linux/amd64 plus both macOS architectures. Workers do not run heavy container/e2e suites independently. A Linux pass does not prove macOS signing or native module compatibility.

## Assumptions and contingencies

- The owner approved current-capability parity only. Compaction, goals, Plan/Edit, native-provider expansion, MCP client migration and remote strict-edit expansion remain separate proposals; their existing work is not cancelled.
- Feature flag changes affect new operations. Existing operations retain their engine until completion or Stop. Changing that to an emergency kill switch would be a separate user-visible lifecycle decision.
- Existing endpoints/roles are shared unchanged between engines. No separate experimental model preferences are introduced because there are no new provider protocols or model controls in this scope.
- The chosen framework requires the narrow unknown-call dependency extension to preserve current behavior. If that extension or the paused-future/stream lifecycle cannot pass V4–V7, experimental rollout is blocked; do not silently weaken approval semantics, raise retry budgets, adopt a second tool loop or replace the requested framework.
- The exact five dependency pins resolved as 48 locked packages under isolated CPython 3.13.16. The retained narrow SDK patch now routes unknown calls to Go refusal handling without adding another loop. Native Linux tests and composed UI/audit/draft smoke have exercised the packaged SDK, Go bridge, approval continuation and lifecycle; Linux runtime builds reproduced the same manifest digest. Cross-built macOS bundles are not evidence of native execution or signing: those checks remain the native CI/release targets' responsibility.
- The owner-authorized current milestone is v0-9. Its charter retains current busy-terminal work and adds this integration. It implies no release date or promotion. Tracked ownership is `nocx-g9giy`.

## Primary source index

### nocx sources inspected

`docs/architecture.md` AD-1/2/4/6/7/8; `docs/decisions/0028-eino-runs-the-loop-the-grant-is-ours.md`; `docs/milestones/v0-8.md`; `internal/assistant/{assistant.go,engine.go,carrier.go,dispatch.go,kernel.go,httpguard.go,probe.go,skilldraft.go,skillaudit.go,skillaudit_pass.go,classifier.go,answerstream_test.go}`; `internal/transport/{ws_agent.go,ws_assistant.go}`; `internal/content/{ledger.go,ledger_agent_sqlite.go}`; `internal/settings/settings.go`; `frontend/src/{agent-ask.ts,agent-status-line.ts}` and UI kit inventory; `contracts/{README.md,agent.ask.schema.json,agent.status.schema.json}`; `internal/coordinator/handshake.go`; `internal/update/serverbin/serverbin.go`; `.github/workflows/release.yml`; `e2e/agent-ask.spec.ts`. Additional authority, parity and runtime slices were inspected by read-only scouts; all runtime claims remain explicitly untested.

### Pinned upstream sources

- [Pydantic Deep Agents source pin](https://github.com/vstorm-co/pydantic-deepagents/commit/650b592b6accd7dc994226abbaa0cf717527e457)
- [Factory, feature defaults and assembly](https://github.com/vstorm-co/pydantic-deepagents/blob/650b592b6accd7dc994226abbaa0cf717527e457/pydantic_deep/agent.py#L661-L739)
- [Declared dependencies and optional extras](https://github.com/vstorm-co/pydantic-deepagents/blob/650b592b6accd7dc994226abbaa0cf717527e457/pyproject.toml)
- [Context/history ownership documentation](https://github.com/vstorm-co/pydantic-deepagents/blob/650b592b6accd7dc994226abbaa0cf717527e457/docs/architecture/memory-and-context.md) — documentation is not proof of installed behavior
- [Pydantic AI toolset API](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/toolsets/abstract.py)
- [Sequential barriers and upfront validation](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/_tool_execution.py#L630-L680)
- [Pending-tool execution order](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/_tool_execution.py#L833-L885)
- [Unknown-name resolution and execution](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/tool_manager.py#L548-L569)
- [Agent constructor and graph dependencies](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/agent/__init__.py)
- [Native Chat model and response mapping](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/models/openai.py#L958-L1004)
- [Reasoning reception](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/models/openai.py#L1322-L1356), [streamed reasoning](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/models/openai.py#L4102-L4147)
- [Explicit provider injection](https://github.com/pydantic/pydantic-ai/blob/v2.52.0/pydantic_ai_slim/pydantic_ai/providers/openai.py#L91-L145)
- [OpenAI SDK constructor and ambient configuration](https://github.com/openai/openai-python/blob/v3.19.0/src/openai/_client.py#L918-L1068)
- [httpx2 custom transport](https://github.com/pydantic/httpx2/blob/v2.12.0/src/httpx2/httpx2/_transports/base.py)
- [Harness candidate metadata](https://pypi.org/pypi/pydantic-ai-harness/0.52.0/json)
- [uv lock/sync semantics](https://docs.astral.sh/uv/concepts/projects/sync/)
