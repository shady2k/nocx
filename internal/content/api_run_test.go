package content

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openAPIRunStore(t *testing.T, budget Budget) (ContentDB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := Open(context.Background(), Config{Path: path, Key: testKeyInternal(), Budget: budget})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func apiRunStartForTest(requestText string) APIRunStart {
	return APIRunStart{
		CollectionPath: "/collections/acme",
		RequestRelPath: "users/create.json",
		Method:         "POST",
		URL:            "https://api.example/users",
		Request: APIRaw{
			Text:  requestText,
			Spans: []APIRunSpan{{From: 29, To: 39, Kind: "secret", Name: "API_TOKEN"}},
		},
		StartedAt: 100,
	}
}

func apiRunResultForTest() APIRunResult {
	return APIRunResult{
		Outcome:      APIRunAnswered,
		Environment:  "prod",
		Route:        APIRunRoute{Kind: "connection", ProfileID: "bastion", InsecureTLS: true},
		RemoteAddr:   "10.0.0.5:443",
		DNSAddresses: []string{"10.0.0.5", "10.0.0.6"},
		Timings:      APIRunTimings{DNSMs: 1, ConnectMs: 2, TLSMs: 3, TTFBMs: 4, TotalMs: 5},
		Certificates: []APIRunCertificate{{Subject: "CN=api", Issuer: "CN=ca", DNSNames: []string{"api.example"}}},
		Response: &APIRunResponse{
			Status:  201,
			Headers: []APIRunHeader{{Name: "Content-Type", Value: "application/json", Enabled: true}},
			Text:    `{"id":"u-1"}`,
			Raw: APIRaw{
				Text:  "HTTP/1.1 201 Created\n\n{\"id\":\"u-1\"}",
				Spans: []APIRunSpan{},
			},
			TLSVersion:     "TLS 1.3",
			TLSCipherSuite: "TLS_AES_128_GCM_SHA256",
			Trust:          APIRunTrust{State: "verified"},
		},
		EndedAt: 200,
	}
}

func TestAPIRunRoundTripSurvivesRestartAndKeepsDurableIdentity(t *testing.T) {
	ctx := context.Background()
	budget := testBudgetInternal()
	db, path := openAPIRunStore(t, budget)
	repo := db.APIRuns()
	const secret = "live-token-must-never-be-written"
	const reference = "⟦API_TOKEN⟧"
	start := apiRunStartForTest("POST /users HTTP/1.1\nAuthorization: Bearer " + reference + "\n\n")
	begin, err := repo.Begin(ctx, start)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if begin.Outcome != APIRunPending || begin.ID <= 0 {
		t.Fatalf("pending run = %+v", begin)
	}
	result := apiRunResultForTest()
	result.Response.Raw.Text = "HTTP/1.1 201 Created\nAuthorization: Bearer " + reference + "\n\n"
	settled, err := repo.Complete(ctx, begin.ID, result)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if settled.Request.Text != start.Request.Text || settled.Response == nil || settled.Response.Text != result.Response.Text {
		t.Fatalf("settled exchange lost request/response: %+v", settled)
	}
	if settled.Route != result.Route || settled.Environment != result.Environment {
		t.Fatalf("settled route/environment = %+v / %q", settled.Route, settled.Environment)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	reopened, err := Open(ctx, Config{Path: path, Key: testKeyInternal(), Budget: budget})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.APIRuns().Get(ctx, begin.ID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.CollectionPath != start.CollectionPath || got.RequestRelPath != start.RequestRelPath {
		t.Fatalf("durable identity = %q/%q", got.CollectionPath, got.RequestRelPath)
	}
	if got.Request.Text != start.Request.Text || got.Response == nil || got.Response.Raw.Text != result.Response.Raw.Text {
		t.Fatalf("restart round trip lost exchange: %+v", got)
	}
	if !strings.Contains(got.Request.Text, reference) || !strings.Contains(got.Response.Raw.Text, reference) {
		t.Fatalf("secret reference was not preserved in request/response raw text: %+v", got)
	}
	if strings.Contains(got.Request.Text, secret) || strings.Contains(got.Response.Raw.Text, secret) {
		t.Fatalf("credential value crossed the repository boundary: %+v", got)
	}
	if got.Outcome != APIRunAnswered || got.EndedAt == nil || *got.EndedAt != result.EndedAt {
		t.Fatalf("restart lifecycle = %+v", got)
	}

	for _, name := range []string{"content.db", "content.db-wal", "content.db-shm"} {
		data, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), name)) //nolint:gosec // test scans package-created files
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			t.Fatalf("read %s: %v", name, readErr)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("%s contains the credential bytes", name)
		}
	}
}

func TestAPIRunBackupContainsOwnTables(t *testing.T) {
	ctx := context.Background()
	db, _ := openAPIRunStore(t, testBudgetInternal())
	repo := db.APIRuns()
	begin, err := repo.Begin(ctx, apiRunStartForTest("GET /users HTTP/1.1\n\n"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, completeErr := repo.Complete(ctx, begin.ID, apiRunResultForTest()); completeErr != nil {
		t.Fatalf("Complete: %v", completeErr)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if backupErr := db.Backup(ctx, snapshot); backupErr != nil {
		t.Fatalf("Backup: %v", backupErr)
	}
	copyDB, err := Open(ctx, Config{Path: snapshot, Key: testKeyInternal(), Budget: testBudgetInternal()})
	if err != nil {
		t.Fatalf("Open backup: %v", err)
	}
	defer func() { _ = copyDB.Close() }()
	got, err := copyDB.APIRuns().Get(ctx, begin.ID)
	if err != nil {
		t.Fatalf("Get backup run: %v", err)
	}
	if got.Response == nil || got.Response.Status != 201 {
		t.Fatalf("backup lost API response: %+v", got)
	}
}

func TestAPIRunRetentionEvictsOldestCompletedButNeverPending(t *testing.T) {
	ctx := context.Background()
	budget := Budget{RetentionBytes: 35, DiskCeilingBytes: 1 << 20, CompactionFloor: 0.8}
	db, _ := openAPIRunStore(t, budget)
	repo := db.APIRuns()
	pending, err := repo.Begin(ctx, apiRunStartForTest("pending request"))
	if err != nil {
		t.Fatalf("Begin pending: %v", err)
	}
	old, err := repo.Begin(ctx, apiRunStartForTest("old request"))
	if err != nil {
		t.Fatalf("Begin old: %v", err)
	}
	oldResult := apiRunResultForTest()
	oldResult.Response.Text = "old body"
	oldResult.Response.Raw.Text = "old raw"
	oldResult.EndedAt = 101
	if _, completeErr := repo.Complete(ctx, old.ID, oldResult); completeErr != nil {
		t.Fatalf("Complete old: %v", completeErr)
	}
	newer, err := repo.Begin(ctx, apiRunStartForTest("new request"))
	if err != nil {
		t.Fatalf("Begin newer: %v", err)
	}
	newResult := apiRunResultForTest()
	newResult.Response.Text = "new body"
	newResult.Response.Raw.Text = "new raw"
	newResult.EndedAt = 102
	if _, completeErr := repo.Complete(ctx, newer.ID, newResult); completeErr != nil {
		t.Fatalf("Complete newer: %v", completeErr)
	}
	if _, getErr := repo.Get(ctx, old.ID); !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("old run error = %v, want ErrNotFound after retention", getErr)
	}
	stillPending, err := repo.Get(ctx, pending.ID)
	if err != nil {
		t.Fatalf("pending was evicted: %v", err)
	}
	if stillPending.Outcome != APIRunPending || stillPending.EndedAt != nil {
		t.Fatalf("pending lifecycle changed: %+v", stillPending)
	}
	if _, err := repo.Get(ctx, newer.ID); err != nil {
		t.Fatalf("newest run was evicted: %v", err)
	}
}

func TestAPIRunCompleteFailureRollsBackArtifactsAndKeepsPendingInterval(t *testing.T) {
	ctx := context.Background()
	db, path := openAPIRunStore(t, testBudgetInternal())
	repo := db.APIRuns()
	begin, err := repo.Begin(ctx, apiRunStartForTest("GET /users HTTP/1.1\n\n"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	raw := openKeyedConn(t, path)
	if _, execErr := raw.Exec(`CREATE TRIGGER api_run_chunk_failure BEFORE INSERT ON api_run_artifact_chunks
		BEGIN SELECT RAISE(ABORT, 'chunk write failed'); END`); execErr != nil {
		t.Fatalf("create failure trigger: %v", execErr)
	}
	if _, completeErr := repo.Complete(ctx, begin.ID, apiRunResultForTest()); completeErr == nil {
		t.Fatal("Complete succeeded through a chunk write failure")
	}
	_ = raw.Close()
	got, err := repo.Get(ctx, begin.ID)
	if err != nil {
		t.Fatalf("Get after rollback: %v", err)
	}
	if got.Outcome != APIRunPending || got.EndedAt != nil || got.Response != nil {
		t.Fatalf("failed completion changed pending row: %+v", got)
	}
}

func TestAPIRunRejectsInvalidCompletionAndPairsWithSuccess(t *testing.T) {
	ctx := context.Background()
	db, _ := openAPIRunStore(t, testBudgetInternal())
	repo := db.APIRuns()
	begin, err := repo.Begin(ctx, apiRunStartForTest("GET /users HTTP/1.1\n\n"))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	bad := apiRunResultForTest()
	bad.Response = nil
	if _, err := repo.Complete(ctx, begin.ID, bad); err == nil {
		t.Fatal("invalid answered result succeeded")
	}
	if _, err := repo.Complete(ctx, begin.ID, apiRunResultForTest()); err != nil {
		t.Fatalf("ordinary completion failed after refusal: %v", err)
	}
}
