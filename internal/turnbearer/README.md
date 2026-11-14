# Session bearer integration

`Minter` keeps one bearer per agent process incarnation. The session identifier
is the actor's identifier, not a pane identifier. Mint a new bearer after a
process restart; never reuse the old session's bridge environment.

The actor calls `Mint(session)` once, then `BeginTurn(session, Authority{Tools:
runTools})` before sending each prompt. `EndTurn(session)` closes admission,
cancels remaining calls, closes connections and blocks until accepted calls and
caller-slot releases finish. Only then may the actor publish the terminal and
start another turn. `Pending(session)` includes accepted calls waiting for an
approval, but not idle connections. These are the endpoint settlement and idle
signals for the session actor introduced by nocx-a62cl.14.5.

Normal end keeps the bearer. Cancellation, process failure, window close and
idle shutdown retire it through the function returned by `Mint`. In particular,
a cancelled turn ends its session; do not call `BeginTurn` again on that bearer.
The actor owns this distinction. The bearer registry does not decide whether a
turn ended normally or was cancelled.

Connections capture authority once at admission. A lookup in progress at the
end of a turn may still finish building its invocation, but cannot publish it.
A connection admitted with no running turn is refused. Run IDs remain unique
endpoint scopes, as on the original assistant admission path.

## Bridge reconnect and retry

`internal/mcpstdio` keeps the bearer in `endpointLink`, replaces a closed
connection in `attach`, and sends the bearer before request bytes. The next
turn's first call reconnects and receives the new turn's authority.

`endpointLink.call` may retry once only when no complete newline-delimited
frame was written. A complete frame, including a complete write followed by an
error, is never replayed. A refused admission is returned rather than retried.
This transport retry is safe within a turn. Cross-turn safety comes from
ADR-4khgzeyp: normal end leaves no request in flight, and cancellation retires
the session bearer. A request delayed in the bridge before connecting cannot
be identified as an old turn by the endpoint; the actor must enforce those
lifecycle rules. Do not use reconnect as a substitute for settlement.
