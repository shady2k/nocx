# ADR-0103 — MCP tools are activated only after admission

- **Status:** Accepted design; implementation acceptance is separate.
- **Date:** 2026-10-23
- **Beads:** nocx-ga29v, nocx-ga29v.8.1
- **Related:** AD-1/2/6/7/8; ADR-0011, ADR-0028, ADR-0030, ADR-0031,
  ADR-0045 and ADR-0053.
- **Contract:** [MCP tools on demand](../../.internal/specs/2026-10-23-mcp-tools-on-demand-design.md).
- **Source:** PR #181 at `e4ea37c88fd506c61b49a03035b8ec3eb6bb6615`.

## Context

A person wants the assistant to use tools from explicitly configured MCP servers,
without launching every configured program, contacting every endpoint or granting
remote metadata authority during startup or discovery in the assistant UI.
The old implementation branch predates the current capability and assistant
owners. Its behavior is the source contract, not its integration or acceptance
claims. Its ADR-0076 number is occupied on current main and is not reused.

The owner admitted the full stdio, Streamable HTTP, OAuth, Settings and assistant
vertical to 0.8. This is not permission for a second policy engine, configuration
store, secret store or external-agent tool surface.

## Decision

### One catalog owner and one authority path

The existing profile aggregate owns server records, revisions and sanitized local
catalogs. It also owns canonical tool descriptors: persistence and live
verification use the same schema canonicalization and digest. Network schema
loading and dynamic references are refused. Explicit Refresh is the only catalog
discovery operation. A refresh validates the complete paginated catalog before a
single CAS commit, disables new or changed descriptors, and closes its session.

The assistant composes an immutable registry before its existing grant filter and
schema compilation. MCP tools are catalog-only declarations with nocx-authored
model descriptions, singleton `delegate` effects and exact canonical destinations.
Local search, setup, waiting, refusal and cancellation activate nothing.
`StartExecution`, authorized narrowing and runtime invocation occur in that order
through the existing effect kernel. Private snapshots bind record, catalog and
selected descriptor identity; loaded presentation is not authority. Live mismatch
sends no `tools/call` and requires explicit Refresh.

### One cancellation boundary

The dormant runtime registers cancellable work before credential resolution or
connection setup. A mandatory `MutationRunner` bars activation for a sorted,
deduplicated set of server identities, cancels affected work before draining,
closes its sessions, executes the mutation callback, and releases every barrier.
Failure to cancel/drain is a refusal, never permission to commit concurrently.

Capability services remain the owners of repositories, minting, row resolution
and cleanup. Transport snapshots affected IDs, revisions and exact secret
identities under the existing capability operation, releases that operation
before draining, then recomputes the full affected set inside the commit callback.
Any difference is a visible conflict with no automatic retry. This covers shared
endpoint and Vault mutation entrypoints as well as direct MCP management. No
configuration admission is held while credential unlock needs it.

### One secret owner

Vault owns all credential material. Owned-versus-shared bindings determine cleanup,
and surviving references are counted across the whole profile aggregate. Metadata
is removed before material; failed cleanup is durable residue, not a restored
reference to deleted material. Reset preserves server/literal configuration and
counts affected MCP servers separately.

OAuth session material uses backend-only system-secret creation and replacement
in the existing journal. `SystemOnly` survives from prepared journal entries to
durable records; Vault schema version 3 prevents old binaries silently dropping
that security metadata. Both inventory paths and forged row handles refuse these
records. Backend reads, owned deletion, reset/purge and known-material screening
retain access. Renewal serializes by persisted secret ID and does not change it.
Forget and CAS-orphan cleanup are correct after restart.

### Owned, bounded transports

Stdio is direct executable/argv with restricted environment inheritance, bounded
newline framing, output and diagnostics, and existing process-group escalation.
Idle callbacks are generation-bound and cannot close an actively reused session.

HTTP uses the official MCP SDK with an injected MCP-specific guarded client. DNS
validation and dialing select the same allowed addresses. Redirects of original
body-bearing requests are refused. Every redirected GET/HEAD loses credentials,
custom headers and MCP session/event metadata, even on same-origin hops, without
reinjection. There are no implicit environment proxy credentials.

Only explicit Settings OAuth Connect presents a validated URL through the existing
client-host URL opener. PKCE S256, state, issuer, resource and endpoint rules are
validated within a bounded operation. Silent renewal never presents a browser.
Listeners and owned HTTP idle connections close on all terminal paths.

### One Settings vocabulary

The existing UI kit and form owners implement configuration drafts separately from
immediate catalog/OAuth actions. CAS conflicts retain drafts. Own notifications
cannot regress revision or falsely conflict with a completed action. Reads expose
sanitized status, not material or secret IDs. Fresh entry uses the existing Vault
setup/unlock flow. The reset dialog includes the additional MCP impact count.

## Alternatives rejected

- Replaying the stale source branch would replace newer integration owners.
- Eager connection/discovery would turn reading Settings or searching tools into
  an external effect before authority exists.
- A separate MCP policy path would disagree with existing grants and approvals.
- Another secret/config store would split atomic reference sweeps and recovery.
- Draining under a configuration lock would deadlock work waiting for unlock.
- Hiding OAuth secrets only in the UI would leave forged handles and restart
  paths able to access them.
- A stdio-only cut would remove accepted HTTP and OAuth behavior without consent.

## Consequences and verification

The implementation must wire the dormant runtime at the composition root before
landing it. Shutdown stops admission, drains MCP, then closes Vault, without
changing helper-owned terminal survival. Suspension closes sessions but preserves
resumable checkpoints and exactly-once retained-result replay.

The contract requires real-wire schemas for every result, deterministic socket
and process fixtures, the actual Settings/approval journey, secret-leak checks,
mutation/unlock and idle/frame regressions, independent security review, and all
applicable CI jobs on the replacement revision. Browser OAuth unavailability is
reported honestly; socket proof is not presented as a native visual walk.

No resources, prompts, sampling, elicitation, external config import, remote
helper deployment or SSH stdio execution are admitted. Accepted ADR-0076 and the
opposite-direction `internal/mcpstdio` bridge remain unchanged.
