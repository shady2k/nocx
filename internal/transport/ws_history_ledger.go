package transport

// The translation between history.* and the ledger (nocx-rtg0.19).
//
// command_history is gone and history.record / history.query keep their wire
// shapes exactly — they answer from the ledger now. Everything the two
// vocabularies disagree about is decided HERE, in one file, because a mapping
// spread across two handlers is how the write and the read come to disagree
// about what a row means.
//
// Four disagreements, all four decided on the bead before any of this was
// written, and each one repeated at the code that implements it.

import (
	"encoding/json"

	"github.com/shady2k/nocx/internal/content"
)

// environmentForHost derives the environment a recorded command ran in from
// the only locator history.record carries: a host string, empty for the local
// machine.
//
// THE ASSUMPTION IS: EMPTY HOST → local, NON-EMPTY → ssh. It is written here
// rather than left implicit because it has an expiry date. EnvironmentIDFor
// hashes the KIND into the id, and only these two kinds are minted today — so
// the moment nocx-eepi ships container environments, a container on the same
// host hashes to a third id and a caller holding a bare host string cannot
// name it. Whoever ships that epic will find this comment by grepping for the
// constructor, which is why the note lives at the derivation and not in a
// design document.
func environmentForHost(host string) content.Environment {
	if host == "" {
		return content.Environment{
			ID:   content.EnvironmentIDFor(content.EnvLocal, ""),
			Kind: content.EnvLocal,
		}
	}
	return content.Environment{
		ID:       content.EnvironmentIDFor(content.EnvSSH, host),
		Kind:     content.EnvSSH,
		Endpoint: &host,
	}
}

// terminationForStatus is the execution's own fact derived from the only
// outcome history.record sends. The ledger's lifecycle writer gets this from
// the renderer, which watched the command end; history.record arrives after
// the fact with a status and nothing else, so the reason is derived from it
// rather than invented as `completed` for everything.
func terminationForStatus(status content.EntryStatus) content.TerminationReason {
	switch status {
	case content.EntryFailure:
		return content.TermFailed
	case content.EntryInterrupted:
		return content.TermInterrupted
	case content.EntryUnknown:
		// The honest one: the run ended and nobody observed how. Mapping it
		// to `completed` would let a command whose outcome was lost render
		// as one that finished cleanly.
		return content.TermInterrupted
	default:
		return content.TermCompleted
	}
}

// mergeShellExitCode folds the shell arm into an entry payload that already
// carries its redaction receipt. Two sparse writers, one column: the receipt
// is the masking owner's and the exit code is design §3.3's shell arm, and
// neither may clobber the other.
func mergeShellExitCode(payload string, exitCode *int) (string, error) {
	var into map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &into); err != nil {
		return "", err
	}
	arm := json.RawMessage(content.ShellPayloadJSON(exitCode))
	var armFields map[string]json.RawMessage
	if err := json.Unmarshal(arm, &armFields); err != nil {
		return "", err
	}
	for k, v := range armFields {
		into[k] = v
	}
	out, err := json.Marshal(into)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// historyLedgerQuery expresses the recall ladder in the ledger's own terms.
//
// The rung's coordinates change shape and nothing else does: history.query
// names a HOST, the ledger names an ENVIRONMENT, and the two are one hash
// apart (environmentForHost above, with its expiry note). The pane rung names
// the durable pane directly, because that identity survives a backend restart
// while the terminal session id does not. The rung itself, the directory, the
// search text and the page size cross unchanged.
func historyLedgerQuery(scope content.Scope, paneID, cwd, host string, limit int, before *string, text string) content.LedgerQuery {
	q := content.LedgerQuery{
		Scope:  scope,
		PaneID: paneID,
		Cwd:    cwd,
		Text:   text,
		Limit:  limit,
		// history.query is the shell/run corpus. Ask entries have their own
		// target-local recall path and must never appear as runnable commands.
		Kind: content.EntryShell,
	}
	// The rung's coordinates are only meaningful on the rungs that have
	// them; `everywhere` carries none, and sending one would be a filter the
	// user cannot see.
	if scope == content.ScopeHost || scope == content.ScopeDirectory {
		q.EnvironmentID = environmentForHost(host).ID
	}
	if before != nil {
		// THE CURSOR IS THE ROW'S OWN ID — the one handle history.query has
		// ever put on the wire. The store resolves it to that row's commit
		// order; nothing here or there compares ids.
		q.BeforeID = *before
	}
	return q
}

// historyQueryEntryOf projects one ledger row onto history.query's row.
//
// It is a PROJECTION of ledgerEntryWireOf rather than a second reader of the
// same payload: that function already owns "what does this row say", down to
// converting redaction offsets from the bytes the store slices to the UTF-16
// units the renderer decorates. Two readers of one payload is how the two
// wire methods would come to disagree about a receipt.
//
// A nil entry means the row is not answerable on this wire and is omitted —
// see the host rule below.
func historyQueryEntryOf(row content.LedgerEntrySummary) (*historyQueryEntry, error) {
	wire, err := ledgerEntryWireOf(row)
	if err != nil {
		return nil, err
	}

	// HOST: the ledger says nil for a row whose environment row is gone; this
	// wire says a plain string where "" MEANS the local machine. Sending ""
	// for a row we cannot place would say "this ran on your machine" about a
	// command we cannot locate — and this list is one a person reruns from,
	// so that is an invitation to run it in the wrong place. Such a row is
	// omitted instead. It is rare by construction: environment_id is a
	// foreign key, so the row can only appear after its environment was
	// deleted underneath it.
	if wire.Host == nil {
		return nil, nil
	}

	return &historyQueryEntry{
		ID:      wire.ID,
		Command: wire.Intent,
		Cwd:     wire.Cwd,
		Host:    *wire.Host,
		// STATUS: entries.status carries `pending` — an intent whose
		// execution is not yet confirmed — and this contract's enum cannot
		// express it. It maps to `running` rather than widening the enum:
		// `pending` is the ledger's word for a phase, and the only honest
		// rendering of it in front of a person is a command that is running.
		Status:      historyStatusOf(wire.Status),
		ExitCode:    wire.ExitCode,
		StartedAt:   wire.StartedAt,
		EndedAt:     wire.EndedAt,
		MaskedCount: wire.MaskedCount,
		MaskedKinds: wire.MaskedKinds,
		Redactions:  wire.Redactions,
	}, nil
}

// historyStatusOf is the enum mapping, in one place so the write path and the
// read path cannot answer it differently.
func historyStatusOf(status string) string {
	if content.EntryStatus(status) == content.EntryPending {
		return string(content.EntryRunning)
	}
	return status
}
