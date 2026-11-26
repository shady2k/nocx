package client_test

// The write session's half of contracts/README.md's third check: every one of
// the four ops driven through the REAL host and the REAL client, with the
// params this test sends and the result the socket returns validated against
// the schema that freezes them.
//
// The assertions go past conformance where the wire is what carries a
// promise: the version a committed outcome reports is the digest of the bytes
// the file actually holds, a repeated commit over the socket answers the same
// outcome without a second install, and a refused commit reaches the client as
// the typed refusal its panel switches on rather than as `internal`.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// writeSessionID is a session id of the shape the contract requires, distinct
// per subtest so a leftover from one case cannot be adopted by another.
func writeSessionID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:16])
}

// callContract sends one op and validates both halves against the schemas. It
// returns the raw result so a caller can decode it for its own assertions.
func callContract(t *testing.T, c *client.Client, name, op string, params any) json.RawMessage {
	t.Helper()
	if err := validateHelperJSON(loadHelperSchema(t, "fs."+name+".params.schema.json"), mustMarshal(t, params)); err != nil {
		t.Fatalf("the %s params do not satisfy the contract: %v", name, err)
	}
	var raw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceFS, op, params, &raw); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "fs."+name+".schema.json"), raw); err != nil {
		t.Fatalf("the %s result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", name, err, raw)
	}
	return raw
}

// TestFSWriteOpsOverTheWireConformToTheirContracts uploads a file through the
// four ops the way a caller would, then checks the same session's idempotent
// repeat and its abort, contract-validating every frame.
func TestFSWriteOpsOverTheWireConformToTheirContracts(t *testing.T) {
	dir := fsTempDir(t)
	file := filepath.Join(dir, "note.txt")
	original := []byte("the bytes the caller read\n")
	if err := os.WriteFile(file, original, 0o600); err != nil {
		t.Fatal(err)
	}
	c := hostedFS(t, dir)

	// The version the caller commits against is the one a whole-file READ
	// returned, off the same socket — which is the pairing this whole design is
	// for, so the test does not build the version by hand.
	readRaw := callContract(t, c, "read", proto.OpRead, proto.FSReadParams{Path: file, Offset: 0, Length: 1 << 20, Whole: true})
	var read proto.FSReadResult
	if err := json.Unmarshal(readRaw, &read); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if read.Version == nil {
		t.Fatal("the whole-file read carried no version")
	}

	id := writeSessionID(t.Name())
	openRaw := callContract(t, c, "write-open", proto.OpWriteOpen, proto.FSWriteOpenParams{ID: id, Path: file})
	var opened proto.FSWriteOpenResult
	if err := json.Unmarshal(openRaw, &opened); err != nil {
		t.Fatalf("decode write-open: %v", err)
	}
	if opened.Resolved != file {
		t.Fatalf("resolved = %q, want %q", opened.Resolved, file)
	}

	replacement := []byte("the bytes that replace them\n")
	first := replacement[:10]
	second := replacement[10:]
	wrote := callContract(t, c, "write", proto.OpWrite, proto.FSWriteParams{ID: id, Offset: 0, Data: first})
	var size proto.FSWriteResult
	if err := json.Unmarshal(wrote, &size); err != nil {
		t.Fatalf("decode write: %v", err)
	}
	if size.Size != int64(len(first)) {
		t.Fatalf("write answered size %d, want %d", size.Size, len(first))
	}
	callContract(t, c, "write", proto.OpWrite, proto.FSWriteParams{ID: id, Offset: size.Size, Data: second})

	commitParams := proto.FSWriteCommitParams{
		ID: id, Path: file, Size: int64(len(replacement)),
		Expect: &proto.FSWriteExpect{Version: read.Version},
	}
	commitRaw := callContract(t, c, "write-commit", proto.OpWriteCommit, commitParams)
	var committed proto.FSWriteCommitResult
	if err := json.Unmarshal(commitRaw, &committed); err != nil {
		t.Fatalf("decode write-commit: %v", err)
	}
	if committed.Outcome != proto.FSOutcomeCommitted {
		t.Fatalf("outcome = %q, want %q", committed.Outcome, proto.FSOutcomeCommitted)
	}
	if committed.Version == nil {
		t.Fatal("a committed outcome carried no version")
	}
	sum := sha256.Sum256(replacement)
	if committed.Version.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("committed version sha256 = %q, want the digest of the bytes sent", committed.Version.SHA256)
	}
	landed, err := os.ReadFile(file) // #nosec G304 — the fixture this test created.
	if err != nil {
		t.Fatal(err)
	}
	if string(landed) != string(replacement) || committed.Version.Size != int64(len(replacement)) {
		t.Fatalf("the target holds %q (version %+v), want the committed bytes", landed, *committed.Version)
	}

	// The lost reply: the same commit asked again, contract-validated, answers
	// the first outcome and does not install a second time.
	againRaw := callContract(t, c, "write-commit", proto.OpWriteCommit, commitParams)
	var again proto.FSWriteCommitResult
	if err := json.Unmarshal(againRaw, &again); err != nil {
		t.Fatalf("decode repeat: %v", err)
	}
	if again.Outcome != committed.Outcome || again.Version == nil || *again.Version != *committed.Version {
		t.Fatalf("the repeat answered %+v, want %+v", again, committed)
	}

	// The abort half, over the wire: a fresh session discarded, and the abort
	// of the committed one reporting the commit rather than a removal.
	abortRaw := callContract(t, c, "write-abort", proto.OpWriteAbort, proto.FSWriteAbortParams{ID: id})
	var aborted proto.FSWriteAbortResult
	if err := json.Unmarshal(abortRaw, &aborted); err != nil {
		t.Fatalf("decode abort: %v", err)
	}
	if aborted.Outcome != proto.FSOutcomeCommitted {
		t.Fatalf("abort of a committed session answered %q, want %q", aborted.Outcome, proto.FSOutcomeCommitted)
	}
	discarded := writeSessionID(t.Name() + "-discarded")
	callContract(t, c, "write-open", proto.OpWriteOpen, proto.FSWriteOpenParams{ID: discarded, Path: file})
	callContract(t, c, "write", proto.OpWrite, proto.FSWriteParams{ID: discarded, Offset: 0, Data: []byte("never installed")})
	discardRaw := callContract(t, c, "write-abort", proto.OpWriteAbort, proto.FSWriteAbortParams{ID: discarded})
	var discardedResult proto.FSWriteAbortResult
	if err := json.Unmarshal(discardRaw, &discardedResult); err != nil {
		t.Fatalf("decode discard: %v", err)
	}
	if discardedResult.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("abort of an open session answered %q, want %q", discardedResult.Outcome, proto.FSOutcomeAborted)
	}
	after, readErr := os.ReadFile(file) // #nosec G304 — the fixture this test created.
	if readErr != nil || string(after) != string(replacement) {
		t.Fatalf("the abort changed the target: %q (%v)", after, readErr)
	}
}

// TestFSWriteCommitRefusalReachesTheClientAsItsCode is the other half of a
// contract: a refusal must arrive as the code the caller switches on, not as
// `internal`. The condition is a real one — somebody wrote between the read
// and the commit.
func TestFSWriteCommitRefusalReachesTheClientAsItsCode(t *testing.T) {
	dir := fsTempDir(t)
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("as read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := hostedFS(t, dir)

	readRaw := callContract(t, c, "read", proto.OpRead, proto.FSReadParams{Path: file, Offset: 0, Length: 1 << 20, Whole: true})
	var read proto.FSReadResult
	if err := json.Unmarshal(readRaw, &read); err != nil {
		t.Fatalf("decode read: %v", err)
	}

	id := writeSessionID(t.Name())
	callContract(t, c, "write-open", proto.OpWriteOpen, proto.FSWriteOpenParams{ID: id, Path: file})
	callContract(t, c, "write", proto.OpWrite, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("as written\n")})

	if err := os.WriteFile(file, []byte("somebody else's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	err := c.Call(context.Background(), proto.ServiceFS, proto.OpWriteCommit, proto.FSWriteCommitParams{
		ID: id, Path: file, Size: int64(len("as written\n")),
		Expect: &proto.FSWriteExpect{Version: read.Version},
	}, &raw)
	var refusal *client.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("commit over a changed file = %v, want a *client.RefusalError", err)
	}
	if refusal.Code != proto.FSCodeChangedSinceRead {
		t.Fatalf("code = %q, want %q", refusal.Code, proto.FSCodeChangedSinceRead)
	}
	landed, readErr := os.ReadFile(file) // #nosec G304 — the fixture this test created.
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(landed) != "somebody else's\n" {
		t.Fatalf("the other writer's bytes were replaced: %q", landed)
	}

	// The connection keeps serving after the refusal, and the session is still
	// open: the abort goes through on the same socket.
	callContract(t, c, "write-abort", proto.OpWriteAbort, proto.FSWriteAbortParams{ID: id})
	rootRaw := callContract(t, c, "root", proto.OpRoot, proto.FSRootParams{})
	if len(rootRaw) == 0 {
		t.Fatal("the connection answered nothing after a refusal")
	}
}
