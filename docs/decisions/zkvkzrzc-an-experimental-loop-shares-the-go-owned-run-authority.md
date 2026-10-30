# ADR-zkvkzrzc — An experimental loop shares the Go-owned run authority

- **Status:** Accepted for the owner-authorized experimental integration; partially supersedes ADR-0028 decision 1's exclusive Eino choice. Its grant, narrowing, refusal and approval decisions remain binding.
- **Date:** 2026-10-29
- **Work:** `nocx-g9giy`, design reconciliation `nocx-g9giy.1`.
- **Design:** [`PYDANTIC_DEEP_AGENTS_PLAN.md`](../../PYDANTIC_DEEP_AGENTS_PLAN.md), including acceptance cases V1–V15.

## Context

The owner wants to develop a Pydantic Deep Agents implementation alongside Eino, under an experimental feature flag, on a separate branch based on current main. This is an engine experiment, not a second assistant product. Ask, endpoint Test, skill auditing and skill drafting must continue through the same existing surfaces. New goals, Plan/Edit, compaction and broader provider or remote-edit capabilities are not part of this change.

ADR-0028 correctly assigns the loop mechanism to a framework and authority to nocx. Making Eino the only possible framework would conflate those two responsibilities. Conversely, delegating tools, credentials or approval policy to the Python framework would discard the existing authority boundary rather than replace the loop.

## Decision

Eino remains the default. The typed setting `assistant.experimentalAgent`, default false, selects Pydantic Deep Agents only for newly admitted operations. An operation pins its engine and runtime identity before admission. Approval, cancellation, replay, status and terminalization use that identity, not the setting's later value. A selected but unavailable runtime produces a visible failure; there is no fallback to another engine.

The existing Go tool kernel remains the single owner of catalog narrowing, argument validation, capabilities, effect classification, attempts, approvals, result disclosure and framing. Both engines construct it through the same run-host factory. Model HTTP likewise crosses the existing Go guard and recorder. The Python child receives neither provider credentials nor direct network authority, and exposes no shell, filesystem, workspace, MCP or framework-owned tool surface. AD-1, AD-2, AD-4, AD-6, AD-7 and AD-8 are unchanged.

One trusted Python process owns one live Ask. Private framed pipes carry bounded messages with operation, epoch, sequence and request identities. SDK model requests use its native OpenAI adapter and SSE parser over an IPC HTTP transport; Go binds the endpoint, model, headers and credential. SDK retries are disabled. Nested draft or classifier generation uses a separate request on the same operation without blocking the IPC reader. Probe and audit own short-lived operations.

A pending tool invocation remains a suspended SDK future. Go returns the existing typed approval or disclosure question to the existing UI, then rebuilds the host with the resumed grant when the person answers. Already-produced protected results remain in Go's existing disclosure store; approval does not repeat their effects. Cancellation and child failure terminalize the run exactly once and reap only its own process. A process lost while approval is pending is still a failed operation, not an indefinitely pending question.

The dependency pin includes a small Pydantic AI extension for unknown-tool calls. It retains unresolved calls during upfront validation and handles them only in the normal sequential executor. The callback can produce a Go-owned refusal, never discover additional execution authority. With no callback, upstream behavior remains. A callback failure aborts rather than becoming a model retry. Known-tool arguments also reach Go before SDK JSON/schema validation so a malformed later call cannot suppress an earlier ordered effect. We do not write another tool-calling loop or patch the installed package at runtime.

Provider-safe tool names have one translation owner. Invocation routing uses only the admitted catalog; a separate recognition view of the trusted registry classifies withheld names for refusal without admitting them. Trusted tool references in prompts, catalog descriptions and host-owned refusals use the same translation. User text, tool arguments, tool bytes and stored history are never rewritten. Canonical names remain the ledger and approval vocabulary. ADR-0045's declared-call carrier remains binding.

Run facts persist engine and runtime identity with the original model. A matching ask retry returns the original facts and state before resolving today's settings, role or runtime. Transaction-race replay never creates another worker or overwrites a live run. Restored turns use the same metadata and a consistent artifact-body/last-sequence snapshot. Missing historical engine metadata means Eino; unknown nonempty identities are preserved rather than mislabeled. The UI remains one Ask surface, with explicit experimental metadata on experimental turns.

The application ships the pinned interpreter and locked dependency graph for each supported target, with a manifest and licenses. There is no first-Ask installer or reliance on a user's Python. A coordinator that survives the desktop owns a content-addressed runtime outside transient package mounts. Flag-off operations neither start nor validate Python. Packaging and full behavior parity are acceptance requirements, not assumptions established by the dependency smoke.

## Evidence and consequences

The isolated Linux candidate resolved and ran `pydantic-deep` 0.3.50, `pydantic-ai-harness` 0.52.0, `pydantic-ai-slim[openai]` 2.52.0, `openai` 3.19.0 and `httpx2` 2.12.0 on CPython 3.13.16. A real factory/model/native-parser smoke proved ordered suspension and cancellation with scripted HTTP, not the production IPC bridge. It also disproved the original factory's `instrument=False` argument and its omission of `DeepAgentDeps()`: instrumentation is set on the resulting agent, and empty dependencies satisfy the always-present pending-upload capability. Unknown later calls abort before earlier calls in the unpatched SDK; that observed behavior is why the extension is required.

This evidence admits implementation; it does not certify Go integration, disclosure parity, UI restore or packaged platforms. The tracked stages retain those acceptance obligations. Eino removal requires a separate owner decision after that evidence exists. The cost of the experiment is a packaged Python runtime and a maintained source patch; that cost is preferable to forking domain authority or silently changing current behavior.
