---
title: MCP tools on demand for the assistant
status: accepted
created: 2026-09-04
binding-design: docs/decisions/0076-mcp-tools-on-demand.md
related: AD-1, AD-2, AD-6, AD-7, AD-8, ADR-0011, ADR-0028, ADR-0030, ADR-0031, ADR-0045, ADR-0053
beads: nocx-ga29v, nocx-ga29v.1
review: BMAD Mary/John/Sally/Winston/Amelia/Paige roles applied; owner-approved plan
---

# MCP tools on demand for the assistant

## 1. User contract

Settings contains an `MCP Servers` page. A person can create, edit, enable, disable, delete, refresh, and inspect MCP servers. The page supports stdio and Streamable HTTP. Stdio edits an executable, argv, optional absolute cwd, explicit environment bindings, and lifecycle limits. HTTP edits an absolute endpoint, custom headers, auth mode, bearer binding or OAuth configuration, scopes, and lifecycle limits.

Secrets can be entered once or selected from Vault. Persisted profile JSON, catalogs, JSON-RPC payloads, notifications, logs, stderr errors, ledger records, and provider messages carry only opaque references or sanitized status. Plaintext is never returned by a read API.

Only `tools/list` and `tools/call` are supported. Resources, prompts, roots, elicitation, sampling, logging, provider-native MCP, external config import/sync, presets, remote helper deployment, and stdio execution over SSH are not part of this feature.

## 2. Dormant-by-default lifecycle

The following operations do not start a process or open an HTTP session:

- application startup;
- Settings mount, list, or get;
- `agent.ask` setup;
- local `tools.search`;
- waiting for nocx approval;
- refused or cancelled calls.

`Refresh tools` is the only discovery operation. It activates only the selected server, performs MCP `initialize`, then a complete paginated `tools/list`, validates and canonicalizes the result, merges it into the persisted catalog with CAS, and always closes the discovery session.

Sessions close on run terminalization/cancel/discard, approval suspension, server mutation, tool-list-changed notification, transport/protocol/schema error, idle timeout, and application shutdown. Discovery sessions participate in the same per-server lifecycle gate: a mutation cancels/drains Refresh before it returns, without holding the configuration queue during network I/O. A successful configuration mutation closes the server's pooled sessions before its response is followed by `mcpServers.changed`. Deleting a Vault secret first drains every affected server's active discovery and calls, then clears those exact bindings and deletes the stored value; a deleted reference advances the server revision so stale activations fail closed. The default idle timeout is 30 seconds; Settings may choose 0–120 seconds, where 0 closes after each call.

Before `tools/call`, the runtime performs a live paginated `tools/list` and compares the selected descriptor digest with the persisted digest. A mismatch closes the session and returns a visible stale-catalog failure. There is no auto-refresh or auto-execution.

## 3. Catalog and authority

The profile catalog is declarative and local. A fresh catalog stores bounded server/protocol metadata, refresh time, digest, and tools. New or changed tools are disabled. An unchanged tool keeps its enabled state. Removed tools disappear. Any invalid, oversized, duplicate, or CAS-conflicted refresh commits nothing. Changing command/argv/cwd/env, endpoint/auth/headers, or protocol-affecting limits increments the server revision, marks the catalog stale, and closes sessions. Rename and enable toggles also increment revision; rename does not alter the catalog digest. Disabled, stale, and missing-auth tools are absent from new run snapshots.

Each enabled tool is composed into a new immutable registry for the run:

- model name: `mcp_` + first 32 base32 characters of SHA-256(`serverID + NUL + remoteToolName`); collisions reject composition;
- fixed nocx-owned model description; remote descriptions and annotations are not instructions;
- sanitized input schema with persuasion/annotation keywords removed, external `$ref` rejected, and local `$ref` allowed only after bounded compile validation;
- singleton effect set `{delegate}`;
- resource kind `destination`, with canonical HTTP endpoint or `mcp+stdio:<serverID>`;
- bounded untrusted result envelope and checked deadline/cancellation;
- `Narrow` result `*agenttools.MCPScope{RunID, ServerID, ServerRevision, CatalogDigest, RemoteTool, Destination}`.

Streamable HTTP destination grants use canonical URL containment. A stdio grant matches the exact opaque `mcp+stdio:<serverID>` identity; network `*` and subdomain scopes do not authorize local processes.

`tools.search` searches only local catalog rows by server/tool name and bounded untrusted description, returns an explicit untrusted frame, and loads the opaque model name. MCP rows remain lazy even when their schema is small enough to avoid putting server-authored text in the initial provider request.

All MCP effects are `delegate`. MCP annotations, user-configured auth, and catalog text cannot lower the effect, select a scope, or alter policy. Availability (`configured`/`enabled`) is not authority.

## 4. Runtime interfaces

`internal/mcp` owns these narrow seams:

```go
type Runtime interface {
    Refresh(context.Context, Activation) (Catalog, error)
    Invoke(context.Context, Invocation) (Result, error)
    CloseRun(runID string)
    CloseServer(serverID string)
    Close() error
}

type OAuthService interface {
    Authorize(context.Context, Activation, URLPresenter) (OAuthStatus, error)
    Forget(context.Context, serverID string) error
}
```

The runtime receives immutable activation snapshots containing exact server/tool IDs, record revision, catalog digest, validated limits, and only required secret references. It does not read Settings and does not receive an unrestricted config or vault service. Each activation rechecks current record revision, enabled state, and catalog digest before opening a transport.

## 5. Transport requirements

### 5.1 stdio

Use `exec.Cmd` with separate argv and no shell. Validate executable, argv, cwd, environment names, and bounds. Inherit only a backend-owned allowlist (`PATH`, `HOME`, temp, locale/platform essentials); add literal or Vault-bound values explicitly. Never inherit WS auth, provider keys, or arbitrary `os.Environ`.

Own the process group/job. Closing stdin is followed by bounded grace, TERM to the whole group, and KILL to the whole group. Context cancellation uses the same ladder. Bound MCP framing before JSON decode, total stdout, and stderr ring size. Stderr is never logged raw; retain a bounded diagnostic tail and sanitize it against known secret material before a bounded, actionable Settings-facing error.

### 5.2 Streamable HTTP

Use the official MCP Streamable HTTP client with an injected `http.Client`. Endpoints are absolute HTTPS, or HTTP only for an explicitly configured loopback/private destination. Reject userinfo and fragments; canonicalize URLs; bound redirects; GET/HEAD redirects may continue only after stripping all credentials and custom headers, while redirects are refused based on the original request method/body (before the Go HTTP client can rewrite POST to GET); never forward credentials to a different origin. Resolve and validate DNS destinations, rejecting unspecified, link-local, multicast, and cloud metadata addresses. Bound response bodies and bind every request to context. Do not leak proxy credentials implicitly.

Static bearer auth is assembled by the backend from Vault. Arbitrary custom headers reject `Authorization`, `Cookie`, `Host`, hop-by-hop names, and headers owned by MCP protocol negotiation or session/event metadata (case-insensitively). Bearer/header values never enter model-visible or persisted plaintext paths.

## 6. OAuth 2.1

Only Settings `Connect OAuth` may open a browser. The handler snapshots server config/revision under the config operation, releases the queue during network/browser work, binds a loopback callback on `127.0.0.1:0`, requires authorization-server metadata advertising PKCE `S256` before presenting the browser URL, creates PKCE with `S256` and state, and presents the authorization URL through the existing `URLPresenter`. Use the SDK authorization-code handler with protected-resource and authorization-server metadata plus a resource indicator. Support dynamic registration and preregistered client IDs, optional Vault-backed client secret, and scopes. Do not support client-ID metadata documents in this delivery.

OAuth metadata, registration, and token requests require HTTPS, except loopback HTTP for local OAuth. A private HTTPS destination is allowed only when it is the explicitly configured MCP origin or loopback; cross-origin private destinations are rejected. The authorization URL is validated before browser presentation and must use HTTPS or loopback HTTP. An MCP endpoint's private-HTTP allowance never carries over to OAuth requests.

Access token, refresh token, and dynamic client secret are one system-owned OAuth session secret in the OS keychain/Vault. Profile data stores only opaque `SecretID` and sanitized issuer/scopes/expiry. Wire status is only `connected`, `expired`, or `missing`. Commit the binding with CAS against the original server revision. On conflict, delete only the newly-created orphan secret. Close callback listener on success, error, cancel, and timeout; validate state and issuer; never display code or tokens.

Runtime may silently renew using the stored refresh token and replace material behind the same secret ID. A browser is never opened during an assistant call. If reauthorization is required, return `ErrOAuthReconnectRequired` with a bounded instruction to reconnect in Settings. `oauthForget` removes binding metadata first, best-effort deletes the owned secret, closes live sessions, and leaves the server unavailable until a new authorization.

## 7. Persistence and secret lifecycle

Extend the existing profile aggregate with `mcpServers`; do not create a second configuration store. The stored shape is:

```go
type MCPServer struct {
    ID        string
    Revision  uint64
    Name      string
    Enabled   bool
    Transport MCPTransportKind
    Stdio     *MCPStdioConfig
    HTTP      *MCPHTTPConfig
    Limits    MCPLimits
    Catalog   MCPCatalog
}
```

Bindings are discriminated literal or opaque secret reference. Fresh values and existing `secrow:` selections are mutually exclusive on input. Fresh values create owned secrets; selected Vault rows are shared. Stored ownership bits determine cleanup. OAuth sessions are owned/system-only. Old profile documents without `mcpServers` load as an empty list.

Suggested backend limits: 64 servers; 256 tools/server; 2 MiB catalog/server; 32 KiB schema/tool; 2 KiB description; 128 argv/env/header rows; 64 KiB call args; 256 KiB model-facing result. UI may only choose lower validated limits. `SecretReferenceImpact` adds `MCPServerCount`; reset preview and execution count and clear all MCP bindings atomically, leave server records, set OAuth status to missing, and make catalogs non-executable. The current backup snapshot does not include endpoint-like MCP records; a secret-safe export is a separate decision.
MCP secret lifecycle operations coordinate with active server sessions. Deleting a Vault secret drains and closes only servers referencing it before removing the material, clears those bindings, advances their revisions, and emits `mcpServers.changed`. Replacing material drains and closes only referencing sessions before the swap but leaves profile bindings and revisions unchanged. Vault reset drains and closes every server whose references it clears before removing material; unaffected MCP records receive no notification.

## 8. Control-plane contracts

Add exact schemas, OpenRPC entries, validators, and generated TS types for:

- `mcpServers.list`, `get`, `create`, `update`, `delete`;
- `mcpServers.refresh`, `setToolsEnabled`;
- `mcpServers.oauthAuthorize`, `oauthForget`;
- `mcpServers.changed` notification with `{id, revision, change}` only.

CRUD/toggle operations use canonical `ConfigOperation`. Refresh and OAuth snapshot config under that operation, release it for external work, then commit by CAS. Stale revisions return a distinct conflict reason and the UI re-fetches. Every schema uses `additionalProperties:false`, explicit non-null required arrays, and bounded sizes. Real WebSocket tests validate the result actually emitted by the server and verify no secret or `SecretID` leaks where the consumer must not see one.

## 9. Result envelope and egress

The nocx-owned result envelope contains server/tool identity, `isError`, bounded text segments, optional output-schema-validated structured content, resource-link/text-resource metadata, and omitted entries with type/mime/byte count/reason. Binary image/audio/blob content is omitted from the model text channel and described as a visible limitation. Text, structured JSON, MCP errors, and sanitized stderr pass existing result bounds, `FrameUntrusted`, known-material checks, and heuristic egress checks. `isError:true` is a normal tool result; recoverable remote/runtime failures become bounded tool-result text while the run remains alive, whereas protocol/cancel/timeout are ledger failures with a bounded result when the run still exists.

## 10. Test and E2E proof

The implementation must prove:

1. start, Settings list/get, assistant setup, search, refusal, and suspended approval leave process/connection counters at zero;
2. `StartExecution` and approved Narrow precede the only activation edge;
3. live descriptor mismatch sends no `tools/call`, closes the session, and requires Refresh;
4. every call records `delegate` and canonical destination;
5. profile JSON, wire, notifications, logs, stderr errors, ledger/provider payloads contain no secret material;
6. cancel, timeout, suspension, configuration mutation, Vault secret delete/replace/reset, and shutdown leave no child process/connection;
7. browser opens only from explicit OAuth Settings action, refresh renewal is silent, and reauth-required calls do not open a browser;
8. the final model answer depends on the fake MCP result.

The E2E fixture is a deterministic fake stdio MCP server with start marker, initialize/list/call, and cleanup marker. The browser journey creates the server in Settings, refreshes, enables a tool, searches/asks without activation, observes approval with no marker, approves, observes one start and exact call, checks the final model answer, and verifies cleanup. Negative paths cover decline, schema mismatch, server error with secret-shaped text, unsupported media, HTTP redirect credential forwarding, OAuth reconnect, and secret deletion during an active call. An over-real-socket HTTP/OAuth integration covers callback, token persistence, discovery cleanup, and subsequent non-browser execution.

The executable browser proof is `e2e/mcp-tools-on-demand.spec.ts`, backed by `e2e/fixtures/fake-mcp-stdio.mjs`. It drives the actual Settings, `tools.search`, approval, runtime, result-redaction, provider, answer, and run-cleanup seams. The fake MCP process receives a dedicated Vault secret distinct from the model endpoint credential, so the provider authorization header and body assertions prove that MCP-only material did not cross the provider boundary. The persisted-profile round trip also guards descriptor identity against JSON formatting changes: schema whitespace must not turn a freshly refreshed catalog stale. `go test ./internal/mcp ./internal/assistant` covers the runtime/error-envelope regressions below that journey.

## 11. Settings interaction

Server configuration fields are a draft until `Save`; `Cancel` discards only that draft. Refresh, per-tool enablement, OAuth connect/forget, and their responses are immediate persisted actions shown separately from the configuration form and labelled as such. They do not replace, save, or discard an in-progress draft.

A revision conflict preserves the local draft and shows the current persisted revision with explicit choices to discard/reload or keep the draft for review and deliberate resubmission. No automatic retry may overwrite a concurrent edit or claim a mutation succeeded.

The tool catalog has local search across bounded tool names/descriptions, a filter for enabled tools, and visible enabled/total counts. New and changed tools remain disabled until explicitly enabled. Catalog readiness describes the persisted discovery snapshot, not a live health check; display the last refresh time/result and an actionable failure/retry without activating a server on Settings mount. Refresh explains that it explicitly starts only the selected stdio program or contacts its configured HTTP endpoint.

Controls remain labelled, keyboard-operable, and usable at narrow widths. Search/filter state is local and does not mutate the persisted catalog. Tool toggles and OAuth actions preserve focus and announce saved/error state.

## 12. Rollback and documentation

Runtime rollback is disabling MCP records; disabled/stale records leave new snapshots and close active sessions. Code rollback is a normal revert. The additive profile field is ignored by an older build. Secret material remains in Vault until an explicit delete/forget/reset by the new build; an older build must never guess-delete unknown references. Architecture, vision, ADR-0076, and this spec are updated with the final implemented contract.
