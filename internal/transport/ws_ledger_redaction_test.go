package transport

// The redaction receipt on the ledger's write path, and the save flow that
// turns one masked span on a ledger row into a vault reference
// (nocx-rtg0.24). Off the real socket, into the real store.
//
// The three things command_history keeps at masked_count / masked_kinds /
// redactions are what history.query's contract promises on every entry, and
// the ledger held none of them: `masked, _, _, err := maskCommandSafe(...)`
// threw the findings and the segments away. These are the assertions that
// say it no longer does — and that the receipt survives a close, which
// writes the same column.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credentialcapture"
	"github.com/shady2k/nocx/internal/log"
)

// ledgerSecret is the plaintext that must never reach the store, in any
// column or anywhere in the file.
const ledgerSecret = "sk-proj-abcdef1234567890" //nolint:gosec // a synthetic detector fixture

// ledgerSecretIntent is a command carrying it.
const ledgerSecretIntent = `curl -H "Authorization: Bearer ` + ledgerSecret + `" https://openrouter.ai/api`

// newLedgerStoreAt is newLedgerStore at a caller-chosen path — the tests
// that grep the file itself need to know where it is.
func newLedgerStoreAt(t *testing.T, path string) content.ContentDB {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:   path,
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// maskingOfEntry reads the stored receipt off a ledger row.
func maskingOfEntry(t *testing.T, db content.ContentDB, id string) (content.EntryMasking, *content.LedgerEntry) {
	t.Helper()
	row := mustEntry(t, db, id)
	m, err := content.EntryMaskingOf(row.Payload)
	if err != nil {
		t.Fatalf("EntryMaskingOf(%q): %v", row.Payload, err)
	}
	return m, row
}

// assertNoSecret fails when the row carries the plaintext anywhere.
func assertNoSecret(t *testing.T, row *content.LedgerEntry) {
	t.Helper()
	if strings.Contains(row.Intent, ledgerSecret) {
		t.Fatalf("the raw secret is in the stored intent: %q", row.Intent)
	}
	if strings.Contains(row.Payload, ledgerSecret) {
		t.Fatalf("the raw secret is in the stored payload: %q", row.Payload)
	}
}

// ── the receipt is kept, and it is not empty ──────────────────────────────

// A user runs a command carrying an API key. The ledger records the masked
// intent AND what it masked: the count, the kinds and the spans — the three
// fields history.query's contract declares on every entry.
func TestLedgerOpen_KeepsTheRedactionReceipt(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.open", map[string]any{
		"envelope": ledgerEnv(sid, "receipt-1", ledgerSecretIntent, 1),
	}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}

	got, row := maskingOfEntry(t, db, "receipt-1")
	assertNoSecret(t, row)
	if got.MaskedCount != 1 {
		t.Fatalf("maskedCount = %d, want 1 — the receipt reflects what was actually masked", got.MaskedCount)
	}
	if len(got.MaskedKinds) != 1 || got.MaskedKinds[0] != "openai" {
		t.Fatalf("maskedKinds = %v, want [openai]", got.MaskedKinds)
	}
	if len(got.Redactions) != 1 {
		t.Fatalf("redactions = %+v, want the one span the mask left", got.Redactions)
	}
	// The span is byte offsets into the STORED intent, and it names the mask
	// the detector wrote — not an offset into the raw command the renderer
	// sent, which is a different string.
	r := got.Redactions[0]
	if r.Start < 0 || r.End > len(row.Intent) || r.Start >= r.End {
		t.Fatalf("span [%d:%d) does not fit the stored intent %q", r.Start, r.End, row.Intent)
	}
	if slice := row.Intent[r.Start:r.End]; !strings.HasPrefix(slice, r.Prefix) || !strings.HasSuffix(slice, r.Suffix) {
		t.Fatalf("span slices %q, which is not the mask %q…%q", slice, r.Prefix, r.Suffix)
	}
}

// An intent with nothing to mask still carries a receipt — the empty one.
// A reader that finds no receipt at all is looking at a row written by a
// build that kept none, which is a different fact from "no secrets".
func TestLedgerOpen_KeepsAnEmptyReceiptForACleanIntent(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.open", map[string]any{
		"envelope": ledgerEnv(sid, "receipt-clean", "make test", 1),
	}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	got, row := maskingOfEntry(t, db, "receipt-clean")
	if got.MaskedCount != 0 || len(got.MaskedKinds) != 0 || len(got.Redactions) != 0 {
		t.Fatalf("receipt = %+v, want the empty one", got)
	}
	if !strings.Contains(row.Payload, "masking") {
		t.Fatalf("payload = %q, want the receipt present and empty rather than absent", row.Payload)
	}
}

// The close writes the same column as the open. Both facts survive: the
// shell arm's exit code and the receipt.
func TestLedgerClose_KeepsTheReceiptBesideTheShellArm(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	for _, id := range []string{"receipt-open-then-close", "receipt-orphan-close"} {
		if id == "receipt-open-then-close" {
			if _, errObj := ledgerCall(t, conn, "ledger.open", map[string]any{
				"envelope": ledgerEnv(sid, id, ledgerSecretIntent, 1),
			}, 2); errObj != nil {
				t.Fatalf("ledger.open error: %+v", errObj)
			}
		}
		if _, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
			"envelope": ledgerEnv(sid, id, ledgerSecretIntent, 2),
			"status":   "success",
			"facts":    map[string]any{"terminationReason": "completed", "exitCode": 0},
		}, 3); errObj != nil {
			t.Fatalf("ledger.close error: %+v", errObj)
		}

		got, row := maskingOfEntry(t, db, id)
		assertNoSecret(t, row)
		if got.MaskedCount != 1 || len(got.Redactions) != 1 {
			t.Fatalf("%s: receipt after the close = %+v, want the open's receipt intact", id, got)
		}
		var arm struct {
			Kind     string `json:"kind"`
			V        int    `json:"v"`
			ExitCode *int   `json:"exitCode"`
		}
		if err := json.Unmarshal([]byte(row.Payload), &arm); err != nil {
			t.Fatalf("%s: payload %q is not JSON: %v", id, row.Payload, err)
		}
		if arm.Kind != "shell" || arm.V != 1 || arm.ExitCode == nil || *arm.ExitCode != 0 {
			t.Fatalf("%s: the close's shell arm is missing from %s", id, row.Payload)
		}
	}
}

// The receipt a rewrite has settled must not come back. A close arriving
// after the save writes the kind arm and leaves the receipt alone —
// resurrecting the consumed span would let a retried save replace text at
// stale offsets, which is exactly what the idempotency rule prevents.
func TestLedgerClose_DoesNotResurrectARewrittenReceipt(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.open", map[string]any{
		"envelope": ledgerEnv(sid, "receipt-race", ledgerSecretIntent, 1),
	}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	got, _ := maskingOfEntry(t, db, "receipt-race")
	if err := db.Ledger().RewriteRedaction(context.Background(), "receipt-race",
		got.Redactions[0], "{{secret:openrouter.ai}}"); err != nil {
		t.Fatalf("RewriteRedaction: %v", err)
	}

	if _, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
		"envelope": ledgerEnv(sid, "receipt-race", ledgerSecretIntent, 2),
		"status":   "success",
		"facts":    map[string]any{"terminationReason": "completed", "exitCode": 0},
	}, 3); errObj != nil {
		t.Fatalf("ledger.close error: %+v", errObj)
	}

	after, row := maskingOfEntry(t, db, "receipt-race")
	if len(after.Redactions) != 0 {
		t.Fatalf("the close resurrected a settled redaction: %+v", after.Redactions)
	}
	if !strings.Contains(row.Intent, "{{secret:openrouter.ai}}") {
		t.Fatalf("intent = %q, want the reference the rewrite wrote", row.Intent)
	}
}

// ── the save flow reaches a ledger row ────────────────────────────────────

// newLedgerCaptureWSServer wires the REAL content store, a fake vault and an
// injected capture registry: everything secrets.captureSave touches, with
// only the vault faked.
func newLedgerCaptureWSServer(t *testing.T, db content.ContentDB) (*WSServer, *credentialcapture.CaptureRegistry, func()) {
	t.Helper()
	caps, err := credentialcapture.NewCaptureRegistry()
	if err != nil {
		t.Fatalf("NewCaptureRegistry: %v", err)
	}
	fv := &fakeVaultLifecycle{resolvedName: "openrouter.ai", createNamedID: "sec:v1:file:abc123"}
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger),
		WithContentDB(db), WithVaultLifecycle(fv), WithCaptureRegistry(caps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, caps, func() { _ = ws.Stop(ctx) }
}

// linkCapture mints a pending capture whose link names a LEDGER entry —
// the shape history.record's replacement will produce once command_history
// is gone (nocx-rtg0.19). Nothing on the wire mints one yet, which is why
// the registry is driven directly here.
func linkCapture(t *testing.T, caps *credentialcapture.CaptureRegistry, entryID string, span content.Redaction) string {
	t.Helper()
	res := caps.Submit(credentialcapture.CaptureScope{
		Connection: "1", Pane: "pane-1", EntryID: entryID, Generation: 1,
	}, []credentialcapture.PendingCredential{{
		Value:         []byte(ledgerSecret),
		SuggestedName: "openrouter.ai",
		Redaction:     span,
	}})
	if len(res) != 1 || res[0].CaptureID == "" {
		t.Fatalf("Submit returned %+v, want one minted capture", res)
	}
	return string(res[0].CaptureID)
}

// The bead's happy path: a command carrying a key is recorded on the
// ledger, the user saves the key, and the ledger row holds the reference
// instead of the mask — addressed by the entry's UUIDv7, which is the id the
// ledger has.
func TestSecretsCaptureSave_RewritesALedgerRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.db")
	db := newLedgerStoreAt(t, path)
	ws, caps, stop := newLedgerCaptureWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	const entryID = "0192f0aa-0000-7000-8000-00000000cafe"
	if _, errObj := ledgerCall(t, conn, "ledger.open", map[string]any{
		"envelope": ledgerEnv(sid, entryID, ledgerSecretIntent, 1),
	}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	got, _ := maskingOfEntry(t, db, entryID)
	if len(got.Redactions) != 1 {
		t.Fatalf("receipt = %+v, want the one span the save will rewrite", got)
	}
	capID := linkCapture(t, caps, entryID, got.Redactions[0])

	resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": capID}, 3)
	if resp.Error != nil {
		t.Fatalf("captureSave error: %+v", resp.Error)
	}
	var saved struct {
		Name    string `json:"name"`
		Partial bool   `json:"partial"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(resp.Result, &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if saved.Partial {
		t.Fatalf("the save reported a partial result: %+v — the ledger rewrite is owed", saved)
	}
	if saved.Name != "openrouter.ai" {
		t.Fatalf("saved name = %q", saved.Name)
	}

	after, row := maskingOfEntry(t, db, entryID)
	assertNoSecret(t, row)
	if !strings.Contains(row.Intent, "{{secret:openrouter.ai}}") {
		t.Fatalf("intent = %q, want the vault reference", row.Intent)
	}
	if len(after.Redactions) != 0 {
		t.Fatalf("redactions = %+v, want the saved segment gone", after.Redactions)
	}

	// A retried save of the same capture rewrites nothing twice.
	resp = vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": capID}, 4)
	if resp.Error != nil {
		t.Fatalf("retried captureSave error: %+v", resp.Error)
	}
	_, again := maskingOfEntry(t, db, entryID)
	if again.Intent != row.Intent {
		t.Fatalf("intent changed on the retry: %q → %q", row.Intent, again.Intent)
	}

	// And the plaintext is nowhere in the file the store wrote.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	blob, err := os.ReadFile(path) //nolint:gosec // the test owns the temp path it just wrote
	if err != nil {
		t.Fatalf("read the store file: %v", err)
	}
	if strings.Contains(string(blob), ledgerSecret) {
		t.Fatal("the raw secret is on disk after the save flow")
	}
}

// A link naming an entry the retention sweep removed is skipped: the
// rewrite is moot, the secret still exists, and the save is not partial.
func TestSecretsCaptureSave_LedgerRowGoneIsNotAPartialSave(t *testing.T) {
	db := newLedgerStore(t)
	ws, caps, stop := newLedgerCaptureWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)
	_ = openLocalSession(t, conn)

	capID := linkCapture(t, caps, "0192f0aa-0000-7000-8000-0000deadbeef",
		content.Redaction{Kind: "openai", Start: 0, End: 5})
	resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": capID}, 3)
	if resp.Error != nil {
		t.Fatalf("captureSave error: %+v", resp.Error)
	}
	var saved struct {
		Partial bool   `json:"partial"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(resp.Result, &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if saved.Partial {
		t.Fatalf("a vanished row made the save partial: %+v", saved)
	}
}

// The store failing is the external call this path makes. A closed store
// settles the save as a PARTIAL result — the secret exists, the rewrite is
// owed — rather than reporting success over a row that still holds a mask.
func TestSecretsCaptureSave_LedgerRewriteFailureIsPartial(t *testing.T) {
	db := newLedgerStore(t)
	ws, caps, stop := newLedgerCaptureWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	const entryID = "0192f0aa-0000-7000-8000-00000000beef"
	if _, errObj := ledgerCall(t, conn, "ledger.open", map[string]any{
		"envelope": ledgerEnv(sid, entryID, ledgerSecretIntent, 1),
	}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	got, _ := maskingOfEntry(t, db, entryID)
	capID := linkCapture(t, caps, entryID, got.Redactions[0])
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": capID}, 3)
	if resp.Error != nil {
		t.Fatalf("captureSave error: %+v", resp.Error)
	}
	var saved struct {
		Name    string `json:"name"`
		Partial bool   `json:"partial"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(resp.Result, &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if !saved.Partial || saved.Error == "" {
		t.Fatalf("save = %+v, want a partial result naming the rewrite failure", saved)
	}
	if saved.Name != "openrouter.ai" {
		t.Fatalf("saved name = %q, want the created secret's name — step 1 succeeded", saved.Name)
	}
}
