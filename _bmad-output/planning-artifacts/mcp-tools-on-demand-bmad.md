---
title: MCP tools on demand for the assistant — BMAD planning artifact
status: approved
created: 2026-09-04
roles: Mary (analyst), John (PM), Sally (UX), Winston (architect), Amelia (dev), Paige (tech writer)
source: local://mcp-tools-on-demand-plan.md
---

# MCP tools on demand — BMAD planning artifact

## Mary — requirements and boundaries

The user-visible capability is explicit MCP server management in Settings and safe use of discovered tools from an assistant run. The implementation MUST support only `tools/list` and `tools/call`.

In scope:

- stdio and Streamable HTTP transports;
- persisted, sanitized catalogs with explicit Refresh tools;
- secret bindings through the existing Vault boundary;
- static bearer/custom headers and OAuth 2.1 authorization-code flow;
- immutable per-run tool composition through the existing assistant effect kernel;
- cancellation, timeout, stale revision/schema refusal, and bounded results;
- Settings CRUD, tool enablement, refresh, and OAuth connect/forget;
- real WebSocket contract coverage and an end-to-end fake MCP server.

Out of scope: resources, prompts, roots, elicitation, sampling, logging, provider-native MCP, external config import/sync, presets, remote-helper deployment, and stdio over SSH.

Acceptance is behavioral: no process or HTTP session exists before an approved call; Refresh is the only discovery trigger; approval and ledger admission precede activation; all MCP effects are `delegate` and destination-scoped; secrets remain in the credential boundary; cancellation closes process trees/connections.

## John — product/JTBD

When a developer has a trusted local or remote MCP server, they can configure it once, inspect its tools, choose which tools the assistant may offer, and approve each actual effect without granting the server implicit authority. The product must make dormant, stale, disconnected, and unavailable states honest. A person can refresh or connect OAuth from Settings; an assistant cannot silently open a browser or activate a server while searching or waiting for approval.

The primary success path is: create server → Refresh tools → enable one tool → ask the assistant → see no activation before approval → approve → observe exactly one activation and call → receive a bounded result → observe cleanup.

## Sally — UX contract

Settings adds an `MCP Servers` page in the `assistant` group using the existing SolidJS/UI-kit patterns. The page has:

- list summaries and a selected editable detail;
- General, stdio/HTTP, Advanced, and Tools sections;
- Save, Delete, Refresh tools, Connect OAuth, and Forget OAuth actions;
- badges only for `Disabled`, `Needs refresh`, `Needs sign-in`, and `Ready`;
- no claim that a server is running or healthy after a closed probe;
- reference chips/status for secrets; freshly entered values clear after save;
- revision-conflict reread instead of overwriting concurrent edits;
- explicit warnings for new/changed tools, which start disabled.

Mounting Settings, rendering a list/detail, local `tools.search`, and an assistant ask never call Refresh or OAuth. The UI never receives plaintext secrets, secret material, or server-authored instructions as provider context.

## Winston — architecture

`internal/mcp` owns transport/runtime lifecycle behind narrow interfaces. The runtime receives immutable activation snapshots; it does not read Settings or receive an unrestricted config/vault service. A `(runID, serverID)` session is created only inside `Invoke`, after `effectKernel` validation, policy/floor, resource scope, approval, ledger start, and narrowing. Calls on one session serialize. Refresh uses an isolated discovery session and always closes it.

Each enabled fresh tool becomes a normal immutable `agenttools.Tool` for the current run. Its model name is `mcp_` plus the first 32 base32 characters of SHA-256(`serverID + NUL + remoteToolName`). Its description is nocx-owned. Its effect is always `delegate`; its resource is a canonical destination; its scope contains run/server/revision/catalog digest/remote name/destination. MCP descriptions, annotations, and results are untrusted bounded data and cannot change authority.

Stale or disabled catalogs are absent from offers. A live `tools/list` is rechecked before `tools/call`; an exact descriptor mismatch fails closed and requires Refresh. Process and HTTP bounds are backend-owned and validated. stdio uses direct `exec.Cmd` argv without a shell and owns process-group cleanup. HTTP uses guarded HTTPS/loopback, bounded redirects/body, and no credential forwarding across origins. OAuth browser work exists only behind the explicit Settings action; runtime refresh is non-interactive.

Persistence extends the existing profile aggregate with `mcpServers`, preserving atomic writes and reference sweeps. Records contain opaque secret references and ownership metadata, never secret material. Existing backup format does not gain endpoint-like MCP records without a separate decision.

## Amelia — implementation/test handoff

Implement in dependency order:

1. profile domain, validation, repository/CAS, catalog merge, secret ownership/reference sweep;
2. schemas/OpenRPC/TS generation and WebSocket handlers;
3. runtime transports and OAuth;
4. immutable external registry composition, `MCPScope`, executor and lifecycle wiring;
5. Settings UI and generated types;
6. deterministic fake servers, real socket tests, browser smoke, and E2E.

Every external call has a failure test. Every durable operation names both ends of its invariants. The implementation must add no second policy path and no provider-native MCP path.

## Paige — documentation handoff

The durable spec is `docs/superpowers/specs/2026-09-04-mcp-tools-on-demand-design.md`. ADR-0056 records the persisted-catalog-before-activation decision, delegate-only authority, destination scoping, explicit refresh/OAuth, runtime lifetime, untrusted metadata/results, and provider boundary. Architecture/module map and vision roadmap must mention the feature and its non-goals. Final docs must state that the current backup format excludes MCP configs and that rollback does not guess-delete unknown secret references.
