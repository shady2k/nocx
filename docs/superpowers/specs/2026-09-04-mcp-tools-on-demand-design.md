---
title: MCP tools on demand for the assistant
status: accepted
created: 2026-09-04
binding-design: docs/decisions/0056-mcp-tools-on-demand.md
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

The session closes on run terminalization/cancel/discard, approval suspension, server mutation, tool-list-changed notification, transport/protocol/schema error, idle timeout, and application shutdown. Successful configuration mutations take the same per-server gate as invocation and close the server's live sessions before the mutation response is followed by `mcpServers.changed`. The default idle timeout is 30 seconds; Settings may choose 0–120 seconds, where 0 closes after each call.

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

Own the process group/job. Closing stdin is followed by bounded grace, TERM to the whole group, and KILL to the whole group. Context cancellation uses the same ladder. Bound MCP framing before JSON decode, total stdout, and stderr ring size. Stderr is never logged raw and is sanitized against known secret material before a bounded user-visible error.

### 5.2 Streamable HTTP

Use the official MCP Streamable HTTP client with an injected `http.Client`. Endpoints are absolute HTTPS, or HTTP only for an explicitly configured loopback/private destination. Reject userinfo and fragments; canonicalize URLs; bound redirects; GET/HEAD redirects may continue only after stripping all credentials and custom headers, while redirects for requests with a body are refused; never forward credentials to a different origin. Resolve and validate DNS destinations, rejecting unspecified, link-local, multicast, and cloud metadata addresses. Bound response bodies and bind every request to context. Do not leak proxy credentials implicitly.

Static bearer auth is assembled by the backend from Vault. Arbitrary custom headers reject `Authorization`, `Cookie`, `Host`, and hop-by-hop names. Bearer/header values never enter model-visible or persisted plaintext paths.

## 6. OAuth 2.1

Only Settings `Connect OAuth` may open a browser. The handler snapshots server config/revision under the config operation, releases the queue during network/browser work, binds a loopback callback on `127.0.0.1:0`, creates PKCE/state, and presents the authorization URL through the existing `URLPresenter`. Use the SDK authorization-code handler with protected-resource and authorization-server metadata plus a resource indicator. Support dynamic registration and preregistered client IDs, optional Vault-backed client secret, and scopes. Do not support client-ID metadata documents in this delivery.

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
6. cancel, timeout, suspension, mutation/delete, and shutdown leave no child process/connection;
7. browser opens only from explicit OAuth Settings action, refresh renewal is silent, and reauth-required calls do not open a browser;
8. the final model answer depends on the fake MCP result.

The E2E fixture is a deterministic fake stdio MCP server with start marker, initialize/list/call, and cleanup marker. The browser journey creates the server in Settings, refreshes, enables a tool, searches/asks without activation, observes approval with no marker, approves, observes one start and exact call, checks the final model answer, and verifies cleanup. Negative paths cover decline, schema mismatch, server error with secret-shaped text, unsupported media, HTTP redirect credential forwarding, and OAuth reconnect. An over-real-socket HTTP/OAuth integration covers callback, token persistence, discovery cleanup, and subsequent non-browser execution.

## 11. Rollback and documentation

Runtime rollback is disabling MCP records; disabled/stale records leave new snapshots and close active sessions. Code rollback is a normal revert. The additive profile field is ignored by an older build. Secret material remains in Vault until an explicit delete/forget/reset by the new build; an older build must never guess-delete unknown references. Architecture, vision, ADR-0056, and this spec are updated with the final implemented contract.
