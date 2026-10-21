# ADR-0096 — Fish keeps native startup; the helper adapts its private I/O

- **Status:** Accepted
- **Date:** 2026-10-21
- **Decided by:** the worker on `nocx-sd8uk.1.1`, implementing the owner's request to use
  their real Fish shell rather than nocx's conventional unsupported-shell mode.
- **Related:** AD-5/6/7/8, [ADR-0024](0024-authenticated-shell-integration-channel.md),
  [ADR-0049](0049-the-channel-we-own-is-the-carrier.md) and
  [ADR-0081](0081-recovery-and-prompt-boundaries-are-backend-owned-non-authoritative-effects.md).
  None is superseded; this adds a native helper-hosted shell adapter.

## Context and drivers

Fish 4.9.3 is the owner's account shell. The account resolver already starts it, but
local classification intentionally excludes it from integration. Substituting Bash
would fix neither the person's language nor their startup configuration.

The helper already supplies the connected lifecycle socket at fd 4. Fish has neither
a bounded binary-read primitive nor Bash's permanent exec-redirection operation.
Authenticated lifecycle must remain separate from terminal observations; credentials
must not become arguments, exported variables, named files, history or log text.

Startup fidelity includes history, native universal variables and incoming universal
change notifications, not merely sourcing `config.fish` and drawing its prompt.
The final shell must retain tty stdin and the native Fish configuration order.

## Considered options

`--no-config` followed by source replay is not native startup. In Fish 4.9.3 it also
disables the ordinary history/universal-variable engine; source cannot re-enable it.
An XDG configuration proxy with a symlink to the real `fish_variables` also fails:
Fish caches the proxy directory, its notifier watches that lexical directory instead
of the real store's parent, and a missing symlink target can become a separate store.

A plain `--init-command` runs after user startup, too late to protect a pending bearer
pipe from startup descendants. A `status.fish` autoload interception cannot run:
`status` is a reserved name. Forcing the locale helper by deleting `LANG` is not a
solution either; a universal `LANG` can bypass that conditional call.

External `nc`, `jq` or Python, a second lifecycle protocol, and OSC-based lifecycle
inference introduce dependencies or change authority without serving this request.
Leaving every Fish pane conventional rejects the requested integrated workflow.

## Decision

Run normal interactive Fish, without changing its XDG directories or locale. Before
handing over any private descriptor, inspect that exact executable's public native
configuration entrypoint. Admit only an audited prelude whose earlier autoload edges
are known; the initial supported prelude is Fish 4.9.3's. Normalize comments and
indentation, not executable syntax, for its compatibility digest.

That prelude can first autoload the locale helper, the macOS environment helper or
the guaranteed interactive theme-migration helper. Temporarily point
`fish_function_path` at public shims for all three. The first shim locally disables
`fish_trace`, sources the complete private pipe through `/dev/fd/3`, and restores the
original function lookup before forwarding the original helper with its arguments.
This precedes user-controlled startup and leaves native startup in charge.

Restore an originally inherited `fish_function_path` as the same exported global,
including an empty value. Only original absence permits erasing the injected global
to reveal a native universal. If neither exists, restore the native default from
Fish's own configuration/system/vendor-directory values. Changing the path invalidates
autoloaded shims without the tombstones `functions --erase` would create.

The pipe sets explicitly global, unexported lifecycle, recovery and agent-tool values;
no public shim or hook file contains them. Fish parses the pipe completely before
executing its assignments, so descendants encounter EOF, not unread credentials.
Its inherited read descriptor can remain until exit: a drained non-seekable pipe is
not a reusable bearer file. Do not claim that a scoped redirection closes it permanently.
Public startup scaffolding is removed after hook loading; an early exit handler also
covers startup that exits before the post-config hook. A killed process can leave only
public scaffolding, never a bearer file.

The existing helper binary supplies private `fish-channel` operations for bounded,
exactly-one-frame I/O and JSON/NUL data conversion. Raw framing stays in
`lifecyclecodec`; the kernel still authenticates and owns every lifecycle transition.
Per-call nonblocking socket operations and cancellation polling leave the surviving
shell's socket flags and lifetime alone. A short-lived receiver must not read ahead
and discard a subsequent reply.

Configured-agent execution uses the same data conversion and a private anonymous
request descriptor. The helper applies argv/environment at OS exec while preserving
original stdin/stdout/stderr. Fish variable names and tracing rules must not become
new restrictions on the configured program's environment.

## Consequences and evidence

Public native-prelude changes require checking all earlier autoload edges before
admitting a new digest. An unrecognized layout starts conventional Fish without the
private pipe, socket or activation marker; the product reports integration unavailable.
This is preferable to either leaking an unread bearer or silently changing Fish's
history and universal semantics. Helper-less remote Fish has no proven TCP adapter
from this decision and is not claimed as enhanced support.

Isolated Fish 4.9.3 probes established native universal persistence, pre-user-config
pipe consumption, and forwarding to a real user autoload function without an erase
tombstone. They also established that redirecting a saved prompt to `/dev/stdout`
bypasses Fish's prompt capture; native prompt wrappers must call the saved prompt
unredirected, while marker-only wrappers may discard its visual output. Full runtime
and product acceptance belong to the stage record, not this design's status.

Primary sources: [Fish invocation](https://fishshell.com/docs/current/cmds/fish.html),
[Fish 4.9.3 startup](https://github.com/fish-shell/fish-shell/blob/4.9.3/src/bin/fish.rs),
[reserved names](https://github.com/fish-shell/fish-shell/blob/4.9.3/src/parser_keywords.rs),
[autoload invalidation](https://github.com/fish-shell/fish-shell/blob/4.9.3/src/function.rs),
[universal notifier](https://github.com/fish-shell/fish-shell/blob/4.9.3/src/universal_notifier/inotify.rs)
and [cached configuration paths](https://github.com/fish-shell/fish-shell/blob/4.9.3/src/path.rs).
