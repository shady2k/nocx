# ADR-0056 — MCP tools are persisted declarations activated on demand

- **Status:** Accepted.
- **Date:** 2026-09-04
- **Related:** AD-1, AD-2, AD-6, AD-7, AD-8, ADR-0011 (opaque secret references), ADR-0028 (effect kernel), ADR-0030 (owned endpoint secrets), ADR-0031 (vault reset references), ADR-0045 (declared calls), ADR-0053 (tool effect sets).
- **Design:** [MCP tools on demand](../superpowers/specs/2026-09-04-mcp-tools-on-demand-design.md)
- **Beads:** `nocx-ga29v`, `nocx-ga29v.1`

## Context

MCP lets a model reach a user-configured local process or remote service. A naïve integration would discover servers at application start, pass remote tool descriptions directly to the provider, or let an MCP annotation determine whether a call is safe. Each choice creates authority outside the existing effect kernel, makes server-authored text part of the first provider request, or activates a process while the user is only inspecting Settings.

The existing architecture already has the correct enforcement path: immutable declared tools, per-run grants, schema validation, policy/floor, resource scope, approval, ledger admission, narrowing, bounded result egress, and lifecycle ownership. MCP must fit that path rather than introduce a second policy system.

## Decision

### 1. One local catalog before activation

MCP server records and sanitized tool catalogs live in the existing profile aggregate. The catalog is created only by explicit Settings `Refresh tools`, which activates only the selected server, performs `initialize` and complete paginated `tools/list`, validates the result, CAS-merges it, and closes the discovery session. Startup, Settings reads, `agent.ask` setup, `tools.search`, approval waiting, refusal, and cancellation never activate a server.

New and changed tools are disabled; unchanged enabled tools retain state; removed tools disappear. A revision or transport/auth/runtime identity change marks the catalog stale and excludes its tools from new runs. Live descriptor equality is required immediately before `tools/call`; mismatch fails closed and requires Refresh.

### 2. All MCP calls are `delegate` and destination-scoped

Every enabled MCP tool is composed into a fresh immutable per-run registry as a normal `agenttools.Tool`. Its effect is exactly `delegate`, because nocx cannot prove the internal effects of a stdio process or remote service. User configuration and MCP annotations cannot lower that effect or select a policy row. The tool also declares a canonical destination (`mcp+stdio:<server-id>` or normalized HTTP endpoint), which remains a separate resource boundary.

The model receives a stable opaque tool name and a fixed nocx-owned description. Remote descriptions and annotations remain bounded untrusted catalog metadata. `tools.search` returns untrusted local catalog frames and opaque names; it does not activate a server or inject remote instructions into the initial provider request.

### 3. Runtime owns sessions, not authority

`internal/mcp.Runtime` receives immutable activation snapshots and only the required secret references. It does not read Settings or receive unrestricted config/vault capabilities. A session keyed by `(runID, serverID)` is created inside `Invoke` only after kernel admission and is closed on run terminalization, cancellation, suspension, mutation, tool-list change, protocol/schema error, idle timeout, and shutdown. Successful mutations and invocation share a per-server gate, so no call races a committed configuration change. Calls on a session serialize. Refresh uses no run session pool.
stdio uses direct argv execution without a shell and owns process-group cleanup. Streamable HTTP uses guarded endpoints, bounded redirects/bodies, context cancellation, and origin-safe credentials; GET/HEAD cross-origin redirects strip credentials, while body-bearing redirects fail closed. OAuth browser authorization exists only through explicit Settings `Connect OAuth`; runtime token refresh is non-interactive and returns a reconnect-required result instead of opening a browser.

### 4. Secret and result boundaries remain type boundaries

Environment, headers, bearer tokens, OAuth tokens, and client secrets are either literals allowed by the stored contract or opaque Vault references. Persisted records, JSON-RPC, notifications, logs, stderr errors, ledger records, and provider messages never carry secret material. OAuth session material is one owned system secret. Reset clears MCP references and status without deleting server records; owned cleanup is metadata-first.

MCP results are a bounded nocx-owned envelope. Unsupported binary media is explicitly omitted; text, structured output, errors, and sanitized stderr pass existing untrusted-frame, known-material, and egress checks. Recoverable runtime failures are returned as bounded tool-result text while the run remains alive; cancellation and terminal failures remain errors.

### 5. No provider-native MCP in this delivery

The assistant provider continues to see only the existing declared-tool interface. MCP is translated into that interface per run. Provider-native MCP configuration, external config import/sync, presets, and additional MCP capabilities are excluded so there is one source of authority and one approval path.

## Why this rather than the obvious alternatives

**Discover everything at startup** was rejected because it executes configured programs or opens network sessions during an unrelated app operation, increases startup failure surface, and makes a Settings read an effect.

**Pass remote tool declarations directly to the provider** was rejected because server-authored descriptions are untrusted instructions and because provider-native tools would bypass the effect kernel's approval, ledger, destination, and egress guarantees.

**Trust MCP annotations to choose `observe` or another effect** was rejected because annotations describe a remote server's claim, not an authority nocx can verify. `delegate` is the honest class for an external executor.

**Create a mutable global MCP registry** was rejected because concurrent assistant runs would share mutable rows and could observe each other's configuration or revisions. Immutable per-run composition preserves isolation.

**Run OAuth from an assistant call** was rejected because a model-triggered browser is an implicit external effect and can turn a normal call into an unbounded interactive flow. Settings owns explicit human authorization.

## Consequences

- MCP configuration is local and persisted, but discovery and execution are dormant until explicit user actions.
- New/changed catalog tools require an explicit enable action after refresh.
- Every MCP call incurs a live descriptor check and participates in the existing approval/ledger/effect pipeline.
- The runtime owns process/network cleanup and must maintain bounded buffers and deadlines.
- The current backup format excludes endpoint-like MCP records; a secret-safe export needs a separate ADR.
- An older build may ignore the additive `mcpServers` field, but it must not delete unfamiliar secret references.

## Inherited test obligations

The feature is incomplete unless tests demonstrate zero activation before approval, exact post-admission activation, stale descriptor refusal, delegate/destination facts, secret non-disclosure, lifecycle cleanup, explicit-only OAuth browser use, and a final answer that depends on the fake MCP result. Real WebSocket contract tests must validate the server's emitted payloads, and the browser E2E must use the actual Settings and assistant seams.
