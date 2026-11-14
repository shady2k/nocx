# Tool endpoint contracts

The local `tool.sock` endpoint exposes these JSON-RPC methods. Their wire
contracts are the existing unified tool documents under [`tools/`](tools/):

| Method             | Contract document                                                    |
| ------------------ | -------------------------------------------------------------------- |
| `workers.spawn`    | [`workers.spawn.schema.json`](tools/workers.spawn.schema.json)       |
| `workers.say`      | [`workers.say.schema.json`](tools/workers.say.schema.json)           |
| `workers.report`   | [`workers.report.schema.json`](tools/workers.report.schema.json)     |
| `workers.holdings` | [`workers.holdings.schema.json`](tools/workers.holdings.schema.json) |
| `workers.close`    | [`workers.close.schema.json`](tools/workers.close.schema.json)       |
| `workers.inbox`    | [`workers.inbox.schema.json`](tools/workers.inbox.schema.json)       |
| `session.read`     | [`session.read.schema.json`](tools/session.read.schema.json)         |
| `session.keys`     | [`session.keys.schema.json`](tools/session.keys.schema.json)         |
| `session.message`  | [`session.message.schema.json`](tools/session.message.schema.json)   |

Each document is the single declaration for that method's params and result:
the top-level schema describes params, and `$defs.result` describes the JSON
result. The endpoint's composition path embeds the same files through
`contracts/tools.Schemas`, assembles them with `agenttools.Assemble`, and sends
requests through `assistant.NewToolDispatcher`. That dispatcher validates raw
params before execution and validates the executor's result against the same
`$defs.result` declaration.

`internal/toolendpoint/contract_test.go` keeps the second half of the contract
honest. It sends every method through a real Unix socket, reads the response's
actual `result` bytes, extracts `$defs.result` from the referenced document, and
validates those bytes. It also sends one malformed params payload per method to
prove the referenced params schema is enforced. No schema body is copied into
the endpoint or re-declared for this socket.

## `tools.catalogue` — what this caller may call

The method documents above say what each call takes and returns. They do not
say **which of them this caller is allowed to make**, and that is not a
constant: `Registry.ForGrant` narrows the offered set to the admitted grant, and
the authorizer mints deliberately disjoint grants for a coordinator and for a
worker (`internal/app/worker_auth.go`). A caller that assumed the whole list
would be telling its model about calls that do not exist for it. The two sets are
NOT disjoint by name alone — `workers.report` is offered to both, because the
kind that addresses a participant is on a coordinator's grant too — so a caller
learns what it may really do from the refusal the call itself returns, not from
the catalogue it was shown.

`tools.catalogue` is that answer, and it is the endpoint's because the endpoint
is where the grant is. **No caller may derive its own eligibility.**

Params: none. The grant is the admitted invocation's, never a parameter.

Result:

```json
{
  "tools": [
    {
      "name": "workers.spawn",
      "summary": "start one worker in a terminal pane of its own and hand it a task",
      "params": { "type": "object" },
      "result": { "type": "object" }
    }
  ]
}
```

`params` is `Tool.ParamsSchema` and `result` is `Tool.ResultSchema` — the two
halves the contract document already declares, lifted apart by
`agenttools.Assemble` so that the return shape never rides inside the parameters
(`nocx-ydu92`). `summary` is the declaration's description.

**The names here are ours, not any client's.** A caller that speaks a protocol
with its own vocabulary — MCP calls these `description`, `inputSchema` and
`outputSchema` — translates at its own boundary. That translation is what an
adapter is for, and letting a client's field names into this document is how a
domain acquires a vendor.

An empty `tools` array is a valid answer and means the grant reaches nothing. It
is not an error, and a caller must render it as "no tools", never as a failure to
list them.

## `tools.instructions` — what this caller is TOLD

`tools.catalogue` answers what a caller MAY call. It does not say anything to the
agent, and that gap was measured: a coordinator's Claude session initialized
nocx's stdio MCP server, received `serverInfo` and nothing else, and — asked to
spawn a worker — followed the global terminal-multiplexer skill it already had
while the `workers.*` calls granted to it sat unused one namespace away
(nocx-xn63t.6.45). The MCP `initialize` result has a field for exactly this
sentence, and this is the method that fills it.

Params: none, for `tools.catalogue`'s reason: the grant is the admitted
invocation's, never a parameter.

Result:

```json
{
  "instructions": "This session runs inside nocx. Start and manage workers with its own tools and nothing else: …"
}
```

The text is chosen from the admitted grant, because the two callers can do
different things and an instruction that outruns its grant sends an agent to a
call that is refused:

- **A grant that permits `delegate`** is a coordinator's (`workers.spawn` is that
  delegation), and it is told which `workers.*` calls start and manage work, that
  they are the only way to do it — not another terminal multiplexer — and, for a
  client that defers MCP tools, how to load one first (Claude Code's `ToolSearch`
  with `select:mcp__nocx__workers_spawn`).
- **A grant that permits `observe` alone** is a worker's, and it is told to report
  and read its mail (`workers.report`, `workers.inbox`) and nothing about
  spawning.
- **A grant that is neither** is told where it is and nothing more. Silence is
  not an option: an empty answer is the state this method exists to end.

`mcp__<server>__<tool>` is the CLIENT's naming, and `<server>` is the key the
staged MCP config writes (`internal/shellintegration/scripts/nocx.bash`). A
selector naming a key that config does not write names a tool that does not
exist, and the failure is silent, so the text's server name is pinned to that
script by `internal/toolendpoint/instructions_test.go`.

The MCP adapter (`internal/mcpstdio`) asks this once, at `initialize`, and copies
the answer into the result's `instructions` verbatim — it holds no text of its
own, and its boundary test refuses the domain's vocabulary outright. The ask is
bounded: an endpoint that declines, or is not there, leaves the field out of the
result and the handshake still completes, because an initialize that fails costs
the client every tool it would have had.

There is no JSON Schema file for this method, and none for `tools.catalogue`
either: both are tool-endpoint methods, whose wire documents live under
[`tools/`](tools/) when the method is a tool. The MCP `initialize` result is a
client-protocol shape, not a renderer one, so it is declared by the MCP
specification rather than in this directory.

## `calls.cancel` — stop ONE call on a connection its siblings share

A connection is the endpoint's admission interval and one stdio session holds
one for its whole life (ADR-0058, `nocx-tlaft`), so closing it to stop a call
would take every sibling with it. A caller that stops waiting therefore says so
on the connection instead:

```json
{ "jsonrpc": "2.0", "method": "calls.cancel", "params": { "requestId": 7 } }
```

`requestId` is **the caller's own id for the request it sent**, spelled as it
sent it. The endpoint gives each request a context of its own, derived from the
connection's, and this message cancels exactly that one; the siblings, the
connection and the session's admission all go on. The cancelled call's executor
sees its context end, so a long poll releases what it held instead of waiting
for its own bound, and a mutation stops instead of producing its effects
(`nocx-98qho`).

**It is a notification, not a request, and it is never answered.** The caller
has already stopped waiting, so there is no answer to read; and an error written
without an id on this socket means the CONNECTION was refused to the adapter, so
answering a malformed cancel would end the session rather than one call. A
cancel that names a request which already finished, or one this connection never
carried, is read and dropped — the race between an answer and a cancellation is
ordinary, and neither party is wrong.

**The endpoint still answers the cancelled request** with the `call abandoned`
refusal, because every request it read gets an answer. MCP carries no response
for a cancelled request, so the adapter drops it; the answer is what a caller
that speaks this socket directly reads instead of a silent end.

`internal/mcpstdio` translates MCP's `notifications/cancelled` into this
message. MCP carries the CLIENT's request id and the endpoint numbers its own,
so the adapter binds its id to the endpoint's when the frame is about to leave
and names that one; the translation is asserted by
`internal/mcpstdio/session_test.go`'s `TestTheCancelNotificationNamesTheEndpointRequest`.

There is no JSON Schema file for this method for `tools.catalogue`'s reason: it
is not a tool, and its shape is three fields the paragraph above states.
