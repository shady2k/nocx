> Vocabulary note — the “wave” vocabulary was retired on 2026-09-06; live names below use workers/tools.

# DRAFT — NOT APPROVED

# The tool surface at launch: Claude Code without config contamination

**Date:** 2026-09-05

**Status:** DRAFT — NOT APPROVED. This is a mechanism design, not an implementation.
The vendor behavior below was measured on this machine with Claude Code 2.1.258. Any
fact not established by those measurements is called out as an open question.

## 1. In one sentence

A shell function that brackets `claude` creates a launch-owned MCP configuration in a
private temporary directory, points Claude Code at one local stdio bridge, and removes
the directory after the declaration and withdrawal; the bridge translates Claude's
MCP tool calls into the already-decided JSON-RPC 2.0 calls on
`coordinator.RuntimeDir(paths)/tool.sock`.

The user-visible result is that a coordinator typed in a nocx pane can see and invoke
`workers.spawn`, `workers.say`, `workers.wait`, `workers.holdings`, and `workers.close` without any
`.claude`, `.mcp.json`, or other user-owned configuration being edited. A coordinator
started outside a nocx pane remains an ordinary Claude Code invocation.

## 2. What this crosses, and what is already decided

This design crosses the shell integration bundle, a vendor tool-provider protocol, the
assistant tool declaration table, and the external tool endpoint. It does not redesign
the endpoint, enrollment protocol, tool dispatcher, or declaration record.

The **2026-09-05 amendment to §7.1 of
`.internal/specs/2026-08-24-orchestration-mechanism-design.md`** settles the delivery
shape before this document starts. The shipped mechanism is a shell function that
**brackets** the agent, not D5's former launcher that `exec`s it. The reason is the
ordering contract in `docs/lifecycle-protocol.md` §16: the shell must send the
participant's declaration after the agent returns and before `agent_withdraw`.
`exec` removes the shell process that must send that declaration. The consequence for
this design is direct: the shell owns launch staging, and the process reaching the
tool endpoint is the agent's child, not the shell itself.

The same amendment carries **D5 step 4**: the tool surface and any supported hooks are
staged in temporary launch-owned files. The owner's **§3.1 constraint** is sharper:
invasive work at launch is welcome, but editing anyone's configuration files is not.
The amendment also records **Assertion 15**: removing the wrapper must leave `claude`
ordinary and marked not orchestrated. The implementation therefore may add arguments
to one invocation, but may not call `claude mcp add`, write `.mcp.json`, or update
`~/.claude/settings.json`.

**D6** says that no vendor-specific route carries anything required. MCP is an
unavoidable vendor-shaped route for exposing a tool list to Claude Code, but the worker's
authority, supervision, state, declaration, and mail facts do not depend on MCP hooks,
MCP inboxes, prompt text, or a model following an instruction. If MCP disappears or
changes, the adapter fails closed and the worker remains governed by the endpoint and
lifecycle facts, not by a vendor callback.

**AD-8** in `docs/architecture.md` requires interface-first boundaries and dependency
injection at one composition root. This design therefore names one agent-adapter seam,
with one Claude implementation today. A second agent gets another implementation of
that seam; it does not get a second shell staging path or a second worker protocol.

The endpoint is already decided by **`.internal/specs/2026-09-05-the-second-caller-design.md`
D1–D5 and §4.1**. `nocx-server` owns a private listening Unix socket at
`coordinator.RuntimeDir(paths)/tool.sock`; it carries bounded newline-delimited
JSON-RPC 2.0, and the endpoint's authorizer binds the authenticated caller to a
server-authoritative session. The endpoint is not a second worker server. It is not
published as an admitting endpoint while the enrollment authorizer is absent. The
bridge must use that endpoint as-is and must not send a caller-supplied session id or
invent a bearer token.

Finally, **`docs/lifecycle-protocol.md` §§15–16** bind the interval around the launch.
`agent_enrol` opens the grid interval, `agent_withdraw` closes it, and the report drop
is the participant's separate declaration. The drop is opened before the agent starts;
`ok` or `fail` is read from its first line; the declaration is sent before withdrawal;
and the drop is removed afterwards. The shell wrapper already implements this shape
in `internal/shellintegration/scripts/nocx.bash:789-831`. A declaration is not an
exit status, and an exit is not a declaration.

## 3. Measured Claude Code facts

This section is intentionally separate from the design. Every claim here came from a
command run on this machine on 2026-09-05, or from a path read in this worktree. The
commands are included so the measurements can be repeated after a vendor update.

### 3.1 Version and invocation arguments

`which claude && claude --version` produced:

```
/run/current-system/sw/bin/claude
2.1.258 (Claude Code)
```

`claude --help` accepts all of the following relevant invocation arguments:

```
--mcp-config <configs...>       Load MCP servers from JSON files or strings
--strict-mcp-config             Only use MCP servers from --mcp-config, ignoring all
                                other MCP configurations
--settings <file-or-json>       Path to a settings JSON file or a JSON string to load
                                additional settings from
--setting-sources <sources>     Comma-separated list of setting sources (user,
                                project, or local)
--tools <tools...>              Specify the list of available tools from the built-in set
```

The same help output says that `--mcp-config` is repeatable and accepts a file or an
inline JSON string. `--strict-mcp-config` is the switch that makes an invocation-only
surface possible: without it, the invocation also considers the other MCP sources.
The help output does not state conflict precedence when the same setting is present in
several sources. That precedence is an open question below; this design avoids relying
on it for the tool surface by using `--strict-mcp-config`.

`claude mcp --help`, `claude mcp add --help`, and `claude mcp add-json --help` report
these provider forms:

```
stdio   command plus optional args and environment
sse     URL-based provider
http    URL-based provider
```

`add-json --help` also names WebSocket as an accepted JSON provider form, while the
`add --help` transport option lists `stdio`, `sse`, and `http`. This inconsistency is
not used by the design. The only provider form selected here is `stdio`, because it
launches a local process that nocx supplies. `sse` and `http` are network transports,
not Unix-domain-socket transports.

### 3.2 Configuration locations and source loading

The current project contains `.mcp.json` with a project-scoped `playwright-test`
stdio server. `claude mcp get playwright-test` reported `Scope: Project config (shared
via .mcp.json)`. `claude mcp get repowise` reported `Scope: User config (available in
all your projects)`, `Type: stdio`, and its command and arguments. The user settings
file read at `/home/dev/.claude/settings.json` contains the `hooks` object and an
`mcpServers` object; the user MCP registry in `/home/dev/.claude.json` contains the
user-scoped `codex` and `repowise` entries.

A debug run of:

```
claude --print --no-session-persistence \
  --debug-file=/tmp/nocx-claude-debug.log \
  --mcp-config='{"mcpServers":{"broken":{"type":"stdio","command":"/definitely/not-a-command"}}}' \
  --tools='' 'Reply with one word: ready'
```

reported that Claude watches these setting paths, in this order:

```
/home/dev/.claude/settings.json
/home/dev/.herdr/worktrees/nocx/feat-rowqt-2-tool-surface/.claude/settings.json
/home/dev/.herdr/worktrees/nocx/feat-rowqt-2-tool-surface/.claude/settings.local.json
/home/dev/repos/nocx/.claude/settings.local.json
```

The debug run also showed the invocation-supplied MCP server being loaded alongside
configured servers. A second run with `--strict-mcp-config` started only the supplied
`broken` server. The vendor therefore exposes the source names and reads user,
project, and local settings, but the help and debug output do not establish a stable
same-key merge precedence. The adapter must not depend on that precedence.

### 3.3 Provider launch and failure behavior

For the same debug run, Claude logged:

```
MCP server "broken": Starting connection with timeout of 30000ms
MCP server "broken": Connection failed after 2ms (ENOENT): ENOENT: no such file or directory, posix_spawn '/definitely/not-a-command'
MCP server "broken" Connection failed (ENOENT): ENOENT: no such file or directory, posix_spawn '/definitely/not-a-command'
```

Despite that failed provider, the print invocation returned `ready` when no MCP tool
was requested. The error was in the debug log, not in the print output. A provider
failure is therefore not a sufficient pane-visible refusal mechanism.

A temporary stdio provider that successfully answered `initialize` and then exited
produced this sequence:

```
MCP server "vanish": Successfully connected (transport: stdio) in 200ms
MCP server "vanish": Connection established with capabilities: ...
MCP server "vanish": STDIO connection closed after 0s (cleanly)
MCP server "vanish": tools/list failed (Connection closed); retrying in 250ms
MCP server "vanish": tools/list failed (Not connected); retrying in 500ms
MCP server "vanish": tools/list failed (Not connected); retrying in 1000ms
MCP server "vanish" Failed to fetch tools: Not connected
```

The vendor retries a missing stdio provider and eventually reports that it could not
fetch tools, but this experiment did not request a real tool call. The bridge must
not treat a successful `initialize` as proof that the tool endpoint remains available.

An SSE configuration with `url: "unix:///tmp/nocx-tool.sock"` was rejected by the
vendor with:

```
SSE error: protocol must be http:, https: or s3:
```

That is the direct measurement that Claude Code cannot speak the already-decided Unix
socket as an SSE URL. A local bridge is required; replacing the endpoint with HTTP
would violate the second-caller design.

### 3.4 Hooks

Hooks can be supplied per invocation through the same `--settings` file-or-JSON
argument. A temporary settings file containing `SessionStart`, `UserPromptSubmit`,
and `Stop` command hooks wrote this order for one non-interactive print turn:

```
session-start
user-prompt-submit
stop
```

A separate inline `--settings` probe also executed a `SessionStart` command. Hooks are
therefore configurable per launch, and these three events fire at the measured points.
This design does not make any hook load-bearing. In particular, the `Stop` hook fires
when a turn ends; it cannot wake a session that is already idle, and it cannot carry
worker authority or declaration state.

### 3.5 Ordinary-run side effects

A clean temporary `HOME` was used for:

```
HOME=/tmp/nocx-sidefx-home claude --bare --print --no-session-persistence \
  --strict-mcp-config --mcp-config='{"mcpServers":{}}' --tools='' \
  'Reply with one word: ready'
```

The invocation stopped at authentication (`Not logged in · Please run /login`), but it
still created:

```
/tmp/nocx-sidefx-home/.claude.json
/tmp/nocx-sidefx-home/.claude/sessions/
/tmp/nocx-sidefx-home/.claude/backups/
```

The file contained startup metadata such as `firstStartTime`, `firstStartVersion`, a
machine id, and a generated user id. A second clean-home run with the temporary broken
MCP provider created the same home entries and no `.mcp.json` or `settings.json`.
This establishes two boundaries: the wrapper must not edit user configuration, and
ordinary Claude Code startup may still write its own `.claude.json` metadata and cache
directories. The acceptance check must distinguish those vendor-owned ordinary side
effects from forbidden launch staging in a user-owned settings file.

> **Confirmed independently by the coordinator, 2026-09-05.** §3.3's measurement is the most
> consequential fact in this document, so it was re-run rather than taken on report. With a
> config naming a provider that does not exist —
> `{"mcpServers":{"nocxworker":{"type":"stdio","command":"/nonexistent/nocx-worker-bridge"}}}` —
> `claude --print --strict-mcp-config --mcp-config <file>` answered the prompt normally, exited
> **0**, and wrote **nothing to stderr**. The coordinator's entire tool surface can be absent
> and no exit status, no stream and no pane says so. That is why `D8` refuses to read a
> successful launch as a working surface, and why the pane-visible bridge status is a
> deliverable rather than a nicety.

## 4. Decisions

| #       | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                 |
| ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **D1**  | **The shell function remains the only launch and staging owner.** It performs enrollment, creates the private launch directory, invokes Claude, reads the declaration, sends it before `agent_withdraw`, removes the report and launch directory, and returns Claude's status.                                                                                                                                                                                                  | A new `nocx agent run` launcher would duplicate the shell's lifecycle capability and would be unable to send the §16 declaration after an `exec`. The 2026-09-05 §7.1 amendment explicitly settled this against the former D5 shape.                                                                                                          |
| **D2**  | **Use one local stdio MCP bridge.** The staged MCP server has `type: "stdio"`, an absolute bridge command, and arguments naming the worker socket and launch lease. The bridge speaks MCP on its stdin/stdout pipes and speaks bounded JSON-RPC 2.0 to `tool.sock`.                                                                                                                                                                                                               | Pointing Claude at `unix:///.../tool.sock` through SSE or HTTP was measured to fail because the SSE transport accepts only `http:`, `https:`, or `s3:`. Teaching Claude's process to speak the worker socket directly is impossible through the vendor's accepted provider interface and would couple the endpoint to Claude's protocol.        |
| **D3**  | **Use `--strict-mcp-config` with a launch-owned config file.** Claude receives `--mcp-config "$launchDir/mcp.json"` and `--strict-mcp-config`; the file contains exactly the nocx bridge. No user, project, or local MCP server is loaded into this invocation.                                                                                                                                                                                                                 | Calling `claude mcp add` or writing `.mcp.json` would contaminate a shared or user-owned config, and omitting strict mode would let unrelated configured providers enter an orchestrated launch. An inline JSON argument is supported, but a file avoids shell quoting and gives the launch a named lifetime that can be audited and removed. |
| **D4**  | **The bridge is a translation adapter, not a second authority.** Its `tools/list` response is the five existing declarations, and each `tools/call` validates and forwards the declared params to the tool endpoint. It does not mint grants, choose sessions, infer lifecycle state, parse PTY bytes, or implement worker storage.                                                                                                                                               | Adding five Claude-only endpoint handlers would create a second declaration table and a second validation path. Letting the bridge accept a `sessionId` or bearer token would violate the second-caller D6/D7 shape and turn a local child into an authority source.                                                                          |
| **D5**  | **The tool surface is the only vendor-shaped mechanism; hooks are optional and non-load-bearing.** The first Claude adapter stages MCP only. A future hook may be staged through `--settings` if it improves presentation, but no hook may be required for enrollment, worker mutation, delivery, declaration, or terminalization.                                                                                                                                                | Using `Stop`, an inbox socket, or prompt instructions as the coordination carrier would make a vendor callback decide a fact that D6 leaves to nocx. The measured `Stop` hook fires at turn end and cannot wake an already-idle coordinator.                                                                                                  |
| **D6**  | **One adapter interface owns vendor-specific launch details.** Conceptually, `AgentAdapter.Stage(LaunchContext) -> StagedInvocation` returns the vendor argv additions, bridge declaration, and cleanup handle. `ClaudeAdapter` is the one implementation today; the shell lifecycle and launch directory remain shared.                                                                                                                                                        | A Claude branch inside generic shell code would make a second agent a second staging path. A generic MCP abstraction in the endpoint would instead make the backend know a vendor protocol it does not own.                                                                                                                                   |
| **D7**  | **Enrollment refusal happens before staging and falls back to the ordinary command.** If `agent_enrol` is absent, malformed, times out, or refuses, the shell prints the backend's reason in the pane and runs `command claude "$@"` without MCP arguments. The person sees the refusal; the model sees no nocx tools.                                                                                                                                                          | Refusing to run Claude would turn an optional orchestration feature into a terminal outage. Staging anyway would expose a surface without an authenticated interval, the fail-open defect D4 exists to prevent.                                                                                                                               |
| **D8**  | **A successful lifecycle enrollment is not treated as a successful MCP connection.** The bridge must establish its endpoint connection and return a named refusal for endpoint loss or an unauthorized call. The model sees the MCP tool error or missing tool; the person must receive a pane-visible bridge-status sentence through a separately verified monitor, not through Claude's provider stderr.                                                                      | Relying on Claude's provider error is rejected by measurement: a failed stdio provider was logged in `--debug-file` while the print pane still returned `ready`, and a provider's stderr was not printed. Silently continuing as if the tools existed is the wrong side of D4.                                                                |
| **D9**  | **The launch directory has a normal cleanup path and a crash cleanup path.** The shell installs an `EXIT`/interrupt cleanup around its bracket. The private directory carries a launch lease keyed by the shell process identity; a bridge-side watcher removes it when the shell dies, and the next nocx launch sweeps stale lease directories before creating one. The directory is mode `0700`, files are mode `0600`, and stale contents are never loaded as a live launch. | Leaving files in the repository, `.claude`, or `.mcp.json` survives the launch and violates §3.1. Depending only on a shell `EXIT` trap fails for `SIGKILL` and abrupt parent death. A global temporary-file sweep without a nocx prefix and lease could delete another program's files.                                                      |
| **D10** | **Vendor upgrades are accepted only through an adapter conformance check.** The check runs the real installed Claude CLI with a staged config and a scripted bridge, proves the five tools are requested and invoked, proves strict mode excludes configured servers, proves failure is surfaced by the chosen pane monitor, and proves cleanup after normal and killed launches. A changed MCP contract blocks orchestration rather than silently exposing an empty list.      | Treating a successful Claude exit or a model's prose as proof would miss the measured failure where Claude returned `ready` with a dead provider. Pinning one vendor version forever is not a design; allowing any version without a wire check lets a provider rename or schema drift break the worker invisibly.                              |

## 5. Launch and data flow

The shell's existing `__nocx_agent_run` remains the outer bracket. After the lifecycle
`agent_enrol` answer is positively recognized, it creates a private launch directory
under the platform temporary directory and a launch lease. It creates `mcp.json` before
starting `claude`, with one server named by the adapter, for example:

```
{
  "mcpServers": {
    "nocx-worker": {
      "type": "stdio",
      "command": "/path/to/nocx-agenttools-bridge",
      "args": [
        "--socket", "/.../run/tool.sock",
        "--launch-lease", "/.../nocx-agent-.../lease"
      ]
    }
  }
}
```

The exact bridge installation path and the endpoint generation argument belong to the
implementation plan. The important boundary is that the config contains paths, not a
secret or a caller-chosen session. The endpoint authorizer receives the kernel-stamped
assertion for the bridge connection; the unresolved child `(pid, start-time)` pin from
the second-caller design remains a prerequisite and is not papered over here.

The shell then invokes the user's original Claude command with only these added
arguments:

```
claude --strict-mcp-config --mcp-config "$launchDir/mcp.json" <original arguments>
```

Claude launches the stdio bridge as a child and owns its MCP stdin/stdout pipes. The
bridge answers MCP `initialize` and `tools/list` from the same five declaration rows
used by the in-process assistant. When Claude issues `tools/call`, the bridge converts
the MCP call to one bounded newline-delimited JSON-RPC 2.0 request on `tool.sock`, keeps
the request id correlation local to the bridge, and converts the endpoint result or
named domain error back to an MCP tool result. The bridge never sees PTY bytes; the
shell and backend lifecycle channel remain the owners of those bytes and facts.

The adapter must make the bridge's environment explicit. It must not put a capability,
access token, or session authority into MCP `env` or a shell environment variable. The
report path in `NOCX_AGENT_REPORT` is intentionally inherited by the enrolled process
tree under §16 and is a rendezvous path, not bearer material. The bridge must not read,
forward, or log it. Other inherited environment values are vendor process state rather
than an authority channel; the implementation must either scrub them at the bridge
boundary or prove that the bridge neither forwards nor persists them.

When Claude returns, the shell reads the report drop exactly as it does today. It sends
the participant declaration first, removes the report and the private launch directory,
then sends `agent_withdraw`. Claude's exit code remains separate from the declaration.
If the shell is killed before this sequence, backend output closure still closes the
grid interval; the bridge lease watcher and the next-launch stale sweep reclaim the
launch files rather than treating them as a resumable tool surface.

## 6. Refusal and failure behavior

The two audiences are deliberately separate. The **person** needs a sentence in the
pane. The **model** needs a tool declaration or a structured tool result. A model that
can see a tool error does not prove that the person saw the refusal, and a pane sentence
does not authorize a tool call.

With no enrollment, the shell prints `nocx: not orchestrated — <backend reason>` and
runs ordinary Claude. No launch directory and no MCP config are created. Claude has no
nocx MCP server and therefore no five worker tools. This is the ordinary fallback and
satisfies D4 without refusing the user's requested agent.

If staging the directory or config fails, the shell prints a named staging refusal and
runs ordinary Claude without the staged arguments. A partial file is removed before
fallback; the wrapper does not invoke Claude with a path whose contents were not fully
written and permission-checked.

If the bridge executable cannot start, Claude's measured behavior is insufficient for
D4: it logs an error, may retry, and can still complete a turn that never requests a
tool. The implementation therefore needs a bridge-status monitor whose pane-visible
failure path is part of the adapter contract. The monitor must be event-driven, not a
sleep-based poll: the bridge writes `ready` only after endpoint authorization succeeds,
`failed` with a bounded reason on startup failure, and `closed` when the endpoint or
stdio session ends. The shell-owned bracket prints the first failure once and does not
claim orchestration after it. The exact monitor transport and the safe way to write a
sentence without stealing the agent's input are open questions, because the current
vendor measurement proves only that MCP stderr is not a pane guarantee.

If the endpoint is not published, the second-caller design says the server does not
admit the endpoint while its authorizer is absent. Enrollment of the grid therefore
cannot be used as a substitute for worker authorization. The bridge reports a named
endpoint refusal, the model receives no usable worker result, and the monitor supplies
the pane sentence. No worker mutation occurs.

If the vendor ignores the staged surface, the acceptance check fails before the feature
is considered wired. The adapter must not silently fall back to configured MCP servers,
prompt text, or an empty tool list. The model either receives all five declarations
from `nocx-worker` or receives none; the person sees the adapter refusal if the vendor
cannot provide that result. The actual Claude tool-name namespace and the exact
observable failure surface remain to be measured with a scripted five-tool bridge.

If an enrolled agent spawns a child, that child is within the process tree that the
approval explicitly covers. The child may inherit `NOCX_AGENT_REPORT` as the §16
rendezvous, but it must not gain a bearer capability, a caller-supplied session id, or
a copied credential from the tool surface. The endpoint's pending process pin must
validate the enrolled tree's child identity before treating the bridge as the admitted
caller. A same-UID connection without that pin is a refusal, not a convenient fallback.

## 7. Invariant intervals

The following intervals state both their opening and closing events. A statement that
names only a setup moment is not an invariant.

1. **Staging interval.** It opens after a positive `agent_enrol` answer and before the
   first Claude process starts. It contains the complete `mcp.json`, lease, and any
   adapter-owned status file. It closes only after Claude returns, the declaration has
   been sent, `agent_withdraw` has been sent, and cleanup has removed the directory.
   A staging error closes it before Claude starts and takes the ordinary fallback.

2. **Enrollment/grid interval.** It opens when the backend positively records
   `agent_enrol` and closes on `agent_withdraw` or backend-owned output/session closure.
   The bridge cannot open, complete, or alter this interval, and Claude's MCP state
   cannot keep it open after the shell and backend have closed it.

3. **Tool availability interval.** It opens only after the bridge has completed MCP
   initialization and endpoint authorization, not merely after Claude spawned the
   bridge. It closes on endpoint refusal, endpoint connection loss, bridge stdio EOF,
   failed tool declaration, or Claude return. No call after closure is reported as a
   success; an in-flight call is either answered by the endpoint or returns a named
   transport failure.

4. **Declaration interval.** The report drop opens before the agent starts and closes
   after the shell reads a positively recognized declaration, sends that declaration
   while enrollment is still open, and removes the drop. An empty, malformed, or
   missing drop closes with no declaration; process exit then remains the independent
   abandonment fact.

5. **No-contamination interval.** The staged config exists only between its launch-owned
   creation and cleanup. Outside that interval, the path is absent and no user or
   project configuration contains the bridge. Claude's ordinary `.claude.json` startup
   metadata is not launch staging and is governed by the vendor, but the adapter never
   writes it or asks Claude to persist the MCP server.

6. **Process-authority interval.** The bridge is admitted only while its process is a
   member of the enrolled agent tree whose identity the endpoint authorizer has pinned.
   It closes on a pin mismatch, parent/tree loss, endpoint close, or lifecycle closure.
   A socket connection that outlives that interval has no authority and must be refused.

## 8. Open questions, deliberately left as holes

These are holes in the design, not assumptions hidden for the implementation worker.

1. **Setting merge precedence.** `--help` and debug output establish user, project, and
   local settings paths and the `user,project,local` source names, but do not establish
   the same-key precedence of those files versus `--settings`. The first implementation
   avoids the question with strict MCP mode and stages no hooks. If a hook becomes part
   of the adapter, measure precedence with a conflicting temporary fixture before
   relying on it.

2. **MCP declaration limits and names.** This work measured provider startup and
   disappearance, not the exact generated tool names, description limits, JSON Schema
   dialect, or maximum parameter/result size accepted by Claude 2.1.258. A scripted
   bridge and a real Claude invocation must establish those facts from the wire.

3. **Pane-visible bridge failure.** Claude's debug log records provider failure, while
   print output and provider stderr did not show a pane sentence. The implementation
   must choose and test an event-driven monitor or another direct pane channel. Until
   then, claiming that MCP failure is visible to the person would be unsupported.

4. **Child pin implementation.** The second-caller design leaves the enrolled child
   `(pid, start-time)` pin as an interface. This design requires the pin for the bridge
   but does not choose its platform implementation. Same-UID access must remain refused
   until that interface is wired.

5. **Crash cleanup on every supported platform.** The design names a lease watcher and
   stale sweep, but the bridge's parent-death and PID-reuse-safe implementation is not
   selected. The acceptance test must cover normal return, trapped interruption,
   abrupt shell death, and a stale directory from an older process identity.

6. **Vendor child environment.** The measurements establish that invocation settings
   reach hooks and that ordinary startup writes `.claude.json`; they do not establish a
   complete allowlist of environment variables Claude passes to an MCP child. The
   bridge must be tested with a sentinel environment value and must prove that no
   inherited secret reaches the worker request or a persistent file.

## 9. Assertions

These are acceptance assertions, not implementation suggestions.

1. Starting `claude` in a positively enrolled nocx pane causes Claude's real MCP client
   to request exactly the five worker declarations from the staged `nocx-worker` server;
   each declaration's name, params schema, and result schema is the corresponding
   existing `agenttools` row.

2. A scripted model/tool-call turn invokes all five declarations through the real
   stdio bridge, and the bridge sends the corresponding JSON-RPC 2.0 requests to the
   real `tool.sock`; no request carries a caller-supplied session id or bearer token.

3. `--strict-mcp-config` prevents the invocation from starting the project's or user's
   configured MCP servers. Removing the wrapper leaves no staged MCP file and a later
   ordinary `claude` invocation does not list `nocx-worker`.

4. With no enrollment, the pane contains the backend refusal sentence, the model has no
   nocx tool declarations, Claude still runs the user's ordinary command, and no launch
   directory was created.

5. If temporary staging fails, the pane contains a staging refusal, Claude runs without
   orchestration, and no partial config remains.

6. If the bridge cannot start or the endpoint is unpublished, the person sees one
   bounded pane-visible refusal, the model receives no successful worker result, and the
   endpoint records no worker mutation caused by the refusal.

7. If the bridge disappears after `initialize`, the adapter does not report tool
   success. A subsequent call receives a named transport failure, the monitor closes
   the tool-availability interval, and no vendor retry is mistaken for a successful
   worker response.

8. If the vendor changes or ignores the staged MCP surface, the conformance check
   fails and the wrapper falls back to an ordinary, explicitly not-orchestrated agent;
   it does not load a user/project server as a substitute.

9. A child process launched by Claude cannot authenticate as a worker caller merely by
   sharing the uid or inheriting `NOCX_AGENT_REPORT`; only the enrolled, pinned process
   tree is admitted, and the bridge forwards no environment value as authority.

10. On normal return, interrupted return, abrupt shell death, and backend restart, the
    staged config and lease are removed or classified stale and unusable. The report is
    sent before `agent_withdraw` on the normal path, and the process exit remains a
    separate fact.

11. Running `claude` outside a nocx pane, before and after removing the wrapper, behaves
    as an ordinary Claude Code invocation. No user-owned `.claude/settings.json`,
    project `.mcp.json`, or global MCP registry was edited by the orchestration path.

## 10. What would falsify this design

The design is falsified if Claude Code has no supported stdio provider launched as a
local child, if strict invocation configuration cannot exclude the configured MCP
servers, or if the five existing declarations cannot be exposed through the provider
without a second authority table. It is also falsified if a Unix socket becomes a
supported direct MCP transport and the bridge is retained without a measured reason;
that would remove its only current necessity, though it would not remove the endpoint's
authentication contract.

It is falsified operationally if a provider failure is the only refusal path and the
person cannot see it, if an absent authorizer still permits a tool call, if a dead or
same-UID child can call the endpoint, or if a model can receive a successful-looking
worker result after the tool-availability interval closes. It is falsified by any launch
that writes `.mcp.json`, `~/.claude/settings.json`, or a persistent equivalent to stage
the surface, or by a later ordinary invocation that retains `nocx-worker` after cleanup.

Finally, it is falsified if the normal shell path sends `agent_withdraw` before the
participant declaration, treats the Claude exit code as the declaration, or lets a
staged file survive as a live surface after a crash without a lease-safe refusal. Those
are contradictions of §16 and cannot be repaired by changing MCP configuration.
