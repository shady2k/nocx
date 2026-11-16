package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

func tabbyRepairConnection(t *testing.T, ps *profile.JSONStore, secrets credential.MaterialStore, extra ...WSServerOption) *websocket.Conn {
	t.Helper()
	_, logger := logtest.New(t)
	options := []WSServerOption{
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithProfileService(profile.NewProfileService(ps)), WithCredentialStore(secrets),
	}
	options = append(options, extra...)
	ws := NewWSServer(logger, newRegWithStub(logger), options...)
	if err := ws.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func tabbyWireResult(t *testing.T, raw []byte, schema string) json.RawMessage {
	t.Helper()
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Error) > 0 || len(response.Result) == 0 {
		t.Fatalf("RPC failed: %s", raw)
	}
	var value any
	if err := json.Unmarshal(response.Result, &value); err != nil {
		t.Fatal(err)
	}
	if err := loadSchema(t, schema).Validate(value); err != nil {
		t.Fatalf("wire result violates %s: %v", schema, err)
	}
	return response.Result
}

func tabbyWireError(t *testing.T, raw []byte) string {
	t.Helper()
	var response struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil {
		t.Fatalf("expected error, got %s", raw)
	}
	return response.Error.Message
}

func tabbyRepairPreview(t *testing.T, conn *websocket.Conn, config, passphrase string) TabbyPreviewResponse {
	t.Helper()
	raw := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{"config": config, "passphrase": passphrase})
	var preview TabbyPreviewResponse
	if err := json.Unmarshal(tabbyWireResult(t, raw, "profiles.tabbyPreview.schema.json"), &preview); err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestTabbyImport_NativeEnvelopeWireContractsAndPasswordPrecedence(t *testing.T) {
	for _, method := range []string{"profiles.importTabby", "profiles.tabbyExecute"} {
		for _, wildcardFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wildcard-first=%t", method, wildcardFirst), func(t *testing.T) {
				ps := profile.NewJSONStore(filepath.Join(t.TempDir(), "p.json"))
				secrets := newTestStore()
				conn := tabbyRepairConnection(t, ps, secrets)
				exact := `{"type":"ssh:password","key":{"user":"root","host":"shared.example","port":22},"value":"exact-password"}`
				wildcard := `{"type":"ssh:password","key":{"user":"root","host":"","port":22},"value":"fallback-password"}`
				ordered := exact + "," + wildcard
				if wildcardFirst {
					ordered = wildcard + "," + exact
				}
				plaintext := `{"config":{"profiles":[
					{"id":"shared-a","type":"ssh","name":"Shared A","options":{"host":"shared.example"}},
					{"id":"shared-b","type":"ssh","name":"Shared B","options":{"host":"shared.example"}},
					{"id":"inline-a","type":"ssh","name":"Inline A","options":{"host":"shared.example","password":"inline-password-a"}},
					{"id":"inline-b","type":"ssh","name":"Inline B","options":{"host":"shared.example","password":"inline-password-b"}},
					{"id":"fallback","type":"ssh","name":"Fallback","options":{"host":"other.example"}},
					{"id":"group","type":"ssh","name":"Grouped","group":"g","options":{"host":"group.example"}},
					{"id":"default","type":"ssh","name":"Default","options":{"host":"default.example","privateKeys":["/tmp/key-a","/tmp/key-b"]}}
				],"groups":[{"id":"g","name":"Group","defaults":{"ssh":{"options":{"port":2200,"user":"deployer"}}}}]},"secrets":[` + ordered + `,
					{"type":"ssh:password","key":{"user":"deployer","host":"group.example","port":2200},"value":"group-password"},
					{"type":"ssh:password","key":{"host":"default.example"},"value":"default-password"}
				]}`
				v := encryptTabbyVaultForTest(t, plaintext, "native-passphrase")
				config := fmt.Sprintf("version: 7\nencrypted: true\nvault:\n  version: %d\n  contents: %q\n  keySalt: %q\n  iv: %q\n", v.Version, v.Contents, v.KeySalt, v.IV)
				if method == "profiles.importTabby" {
					raw := jsonrpcCall(t, conn, method, map[string]any{"config": config, "passphrase": "native-passphrase"})
					result := tabbyWireResult(t, raw, "profiles.importTabby.schema.json")
					if string(result) != "7" {
						t.Fatalf("legacy count = %s, want 7", result)
					}
				} else {
					preview := tabbyRepairPreview(t, conn, config, "native-passphrase")
					if preview.ProfilesToImport != 7 || preview.GroupsToImport != 1 || preview.SecretsToImport != 6 || len(preview.Warnings) == 0 {
						t.Fatalf("incorrect preview: %+v", preview)
					}
					encoded, err := json.Marshal(preview)
					if err != nil {
						t.Fatal(err)
					}
					for _, private := range []string{"exact-password", "fallback-password", "inline-password-a", "inline-password-b", "group-password", "default-password", "native-passphrase", "/tmp/key-a", "/tmp/key-b"} {
						if bytes.Contains(encoded, []byte(private)) {
							t.Fatalf("preview leaked private source value %q", private)
						}
					}
					before, err := ps.LoadProfiles()
					if err != nil || len(before) != 0 {
						t.Fatalf("preview wrote profiles: %v, %v", before, err)
					}
					assertTabbySecretCount(t, secrets, 0)
					raw := jsonrpcCall(t, conn, method, map[string]any{"planToken": preview.PlanToken})
					tabbyWireResult(t, raw, "profiles.tabbyExecute.schema.json")
				}
				profiles, err := ps.LoadProfiles()
				if err != nil || len(profiles) != 7 {
					t.Fatalf("imported profiles: %d, %v", len(profiles), err)
				}
				want := map[string]string{"shared-a": "exact-password", "shared-b": "exact-password", "inline-a": "inline-password-a", "inline-b": "inline-password-b", "fallback": "fallback-password", "group": "group-password", "default": "default-password"}
				for _, p := range profiles {
					secret, err := secrets.Get(context.Background(), credential.SecretID(p.Options.PasswordSecret))
					if err != nil {
						t.Fatal(err)
					}
					if err := secret.Use(func(value []byte) error {
						if string(value) != want[p.ID] {
							t.Errorf("profile %s got wrong password binding", p.ID)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				assertTabbySecretCount(t, secrets, 6)
			})
		}
	}
}

func assertTabbySecretCount(t *testing.T, store *memSecretStore, want int) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.m) != want {
		t.Fatalf("secret count = %d, want %d", len(store.m), want)
	}
}

func TestTabbyImport_InvalidMergeBeforeMint(t *testing.T) {
	for _, invalid := range []string{"missing ID", "missing host", "merged group cycle"} {
		t.Run(invalid, func(t *testing.T) {
			ps := profile.NewJSONStore(filepath.Join(t.TempDir(), "p.json"))
			svc := profile.NewProfileService(ps)
			if err := svc.SaveGroup(profile.ProfileGroup{ID: "parent", Name: "Parent"}); err != nil {
				t.Fatal(err)
			}
			if err := svc.SaveGroup(profile.ProfileGroup{ID: "child", ParentGroupID: "parent", Name: "Child"}); err != nil {
				t.Fatal(err)
			}
			secrets := newFailAfterStore(0)
			conn := tabbyRepairConnection(t, ps, secrets)
			config := "profiles:\n  - type: ssh\n    id: valid\n    name: Valid\n    options:\n      host: valid.example\n      password: inline-secret\n"
			switch invalid {
			case "missing ID":
				config += "  - type: ssh\n    options:\n      host: invalid.example\n"
			case "missing host":
				config += "  - type: ssh\n    id: invalid\n"
			case "merged group cycle":
				config += "groups:\n  - id: parent\n    parentGroupId: child\n    name: Parent\n"
			}
			tabbyWireError(t, jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{"config": config}))
			secrets.mu.Lock()
			calls := secrets.successes
			secrets.mu.Unlock()
			if calls != 0 {
				t.Fatalf("invalid merge attempted %d secret writes", calls)
			}
			profiles, err := ps.LoadProfiles()
			if err != nil || len(profiles) != 0 {
				t.Fatalf("invalid merge wrote profiles: %v, %v", profiles, err)
			}
		})
	}
}

type tabbyFailingDocument struct {
	storage.DocumentStore
	mu   sync.Mutex
	fail bool
}

func (s *tabbyFailingDocument) Write(name string, doc any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("config write refused")
	}
	return s.DocumentStore.Write(name, doc)
}

func (s *tabbyFailingDocument) setFailure(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = fail
}

type tabbyFaultSecrets struct {
	*memSecretStore
	faultMu      sync.Mutex
	creates      int
	failCreateAt int
	failDelete   bool
}

func (s *tabbyFaultSecrets) Create(ctx context.Context, value credential.Secret) (credential.SecretID, error) {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	s.creates++
	if s.creates == s.failCreateAt {
		return "", errors.New("secret mint refused")
	}
	return s.memSecretStore.Create(ctx, value)
}

func (s *tabbyFaultSecrets) Delete(ctx context.Context, id credential.SecretID) error {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failDelete {
		return errors.New("secret cleanup refused")
	}
	return s.memSecretStore.Delete(ctx, id)
}

func TestTabbyExecute_RollbackAndRetry(t *testing.T) {
	for _, failure := range []string{"mint", "config", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			doc := &tabbyFailingDocument{DocumentStore: storage.NewDocumentStore(t.TempDir())}
			ps := profile.NewJSONStoreWithDocStore(doc, "p.json")
			secrets := &tabbyFaultSecrets{memSecretStore: newTestStore()}
			oldID, err := secrets.Create(context.Background(), credential.NewSecret("preexisting"))
			if err != nil {
				t.Fatal(err)
			}
			old := profile.SSHProfile{Base: profile.Base{ID: "keep", Type: "ssh", Name: "Keep"}, Options: profile.StoredSSHProfileOptions{Host: "keep.example", PasswordSecret: string(oldID)}}
			if saveErr := profile.NewProfileService(ps).SaveProfile(old); saveErr != nil {
				t.Fatal(saveErr)
			}
			conn := tabbyRepairConnection(t, ps, secrets)
			config := "profiles:\n  - id: new-a\n    type: ssh\n    name: New A\n    options:\n      host: a.example\n      password: new-password-a\n  - id: new-b\n    type: ssh\n    name: New B\n    options:\n      host: b.example\n      password: new-password-b\n"
			preview := tabbyRepairPreview(t, conn, config, "")
			if failure == "config" {
				doc.setFailure(true)
			} else {
				secrets.faultMu.Lock()
				secrets.failCreateAt = 3 // preexisting, one minted, then failure
				secrets.failDelete = failure == "cleanup"
				secrets.faultMu.Unlock()
			}
			params := map[string]any{"planToken": preview.PlanToken}
			message := tabbyWireError(t, jsonrpcCall(t, conn, "profiles.tabbyExecute", params))
			if failure == "cleanup" {
				if !strings.Contains(message, "secret mint refused") || !strings.Contains(message, "secret cleanup refused") {
					t.Fatalf("lost original or cleanup error: %s", message)
				}
				assertTabbySecretCount(t, secrets.memSecretStore, 2)
				message = tabbyWireError(t, jsonrpcCall(t, conn, "profiles.tabbyExecute", params))
				if !strings.Contains(message, "secret cleanup refused") {
					t.Fatalf("retry ignored pending cleanup: %s", message)
				}
				fresh := tabbyRepairPreview(t, conn, config, "")
				message = tabbyWireError(t, jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{"planToken": fresh.PlanToken}))
				if !strings.Contains(message, "secret cleanup refused") {
					t.Fatalf("fresh preview bypassed pending cleanup: %s", message)
				}
				secrets.faultMu.Lock()
				creates := secrets.creates
				secrets.failDelete = false
				secrets.faultMu.Unlock()
				if creates != 3 {
					t.Fatalf("retry minted before cleaning residual secret: %d calls", creates)
				}
			} else {
				assertTabbySecretCount(t, secrets.memSecretStore, 1)
			}
			remaining, err := ps.LoadProfiles()
			if err != nil || len(remaining) != 1 || remaining[0].ID != "keep" || remaining[0].Options.PasswordSecret != string(oldID) {
				t.Fatalf("failed attempt changed profiles: %v, %v", remaining, err)
			}
			if exists, existsErr := secrets.Exists(context.Background(), oldID); existsErr != nil || !exists {
				t.Fatalf("rollback removed preexisting secret: %v", existsErr)
			}
			doc.setFailure(false)
			tabbyWireResult(t, jsonrpcCall(t, conn, "profiles.tabbyExecute", params), "profiles.tabbyExecute.schema.json")
			assertTabbySecretCount(t, secrets.memSecretStore, 3)
			profiles, err := ps.LoadProfiles()
			if err != nil || len(profiles) != 3 {
				t.Fatalf("retry did not commit profiles: %v, %v", profiles, err)
			}
			for _, p := range profiles {
				if exists, err := secrets.Exists(context.Background(), credential.SecretID(p.Options.PasswordSecret)); err != nil || !exists {
					t.Fatalf("retry committed stale secret reference for %s: %v", p.ID, err)
				}
			}
		})
	}
}

func TestTabbyExecute_RevalidatesMergedTreeAfterPreview(t *testing.T) {
	ps := profile.NewJSONStore(filepath.Join(t.TempDir(), "p.json"))
	svc := profile.NewProfileService(ps)
	if err := svc.SaveGroup(profile.ProfileGroup{ID: "parent", Name: "Parent"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveGroup(profile.ProfileGroup{ID: "child", Name: "Child"}); err != nil {
		t.Fatal(err)
	}
	secrets := newTestStore()
	conn := tabbyRepairConnection(t, ps, secrets)
	config := "profiles:\n  - id: new\n    type: ssh\n    options:\n      host: new.example\n      password: private\ngroups:\n  - id: parent\n    name: Parent\n    parentGroupId: child\n"
	preview := tabbyRepairPreview(t, conn, config, "")
	if err := svc.SaveGroup(profile.ProfileGroup{ID: "child", Name: "Child", ParentGroupID: "parent"}); err != nil {
		t.Fatal(err)
	}
	tabbyWireError(t, jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{"planToken": preview.PlanToken}))
	assertTabbySecretCount(t, secrets, 0)
}

func TestTabbyImport_VaultRollbackRemovesCatalogueRows(t *testing.T) {
	docs := storage.NewDocumentStore(t.TempDir())
	registry, err := vault.NewRegistry(file.New(docs, "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(docs, registry, logtest.Slog(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	if _, setupErr := v.Setup(context.Background(), vault.SetupRequest{Passphrase: "test-password"}); setupErr != nil {
		t.Fatal(setupErr)
	}
	oldID, err := v.CreateNamed(context.Background(), credential.NewSecret("keep"), vault.SecretMeta{Name: "Existing", Kind: vault.KindPassword})
	if err != nil {
		t.Fatal(err)
	}
	doc := &tabbyFailingDocument{DocumentStore: storage.NewDocumentStore(t.TempDir()), fail: true}
	ps := profile.NewJSONStoreWithDocStore(doc, "p.json")
	conn := tabbyRepairConnection(t, ps, v, WithVaultLifecycle(v))
	config := "profiles:\n  - id: new\n    type: ssh\n    name: New\n    options:\n      host: new.example\n      password: new-password\n"
	params := map[string]any{"config": config}
	message := tabbyWireError(t, jsonrpcCall(t, conn, "profiles.importTabby", params))
	if !strings.Contains(message, "config write refused") {
		t.Fatalf("unexpected import failure: %s", message)
	}
	inventory, err := v.BuildInventory(context.Background(), nil)
	if err != nil || len(inventory) != 1 {
		t.Fatalf("rollback left vault catalogue rows: %v, %v", inventory, err)
	}
	if exists, existsErr := v.Exists(context.Background(), oldID); existsErr != nil || !exists {
		t.Fatalf("rollback deleted preexisting vault row: %v", existsErr)
	}
	doc.setFailure(false)
	tabbyWireResult(t, jsonrpcCall(t, conn, "profiles.importTabby", params), "profiles.importTabby.schema.json")
	inventory, err = v.BuildInventory(context.Background(), nil)
	if err != nil || len(inventory) != 2 {
		t.Fatalf("retry accumulated vault catalogue rows: %v, %v", inventory, err)
	}
}

type tabbyCleanupService struct {
	capability.TabbyImportService
	store credential.SecretStore
}

func (s tabbyCleanupService) DeleteSecret(ctx context.Context, id credential.SecretID) error {
	return s.store.Delete(ctx, id)
}

func TestTabbyImport_RollbackSurvivesRequestCancellation(t *testing.T) {
	secrets := &tabbyFaultSecrets{memSecretStore: newTestStore()}
	oldID, err := secrets.Create(context.Background(), credential.NewSecret("existing"))
	if err != nil {
		t.Fatal(err)
	}
	minted, err := secrets.Create(context.Background(), credential.NewSecret("new"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan := &importPlan{pendingSecrets: []credential.SecretID{minted}}
	if err := rollbackTabbySecrets(ctx, tabbyCleanupService{store: secrets}, plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.pendingSecrets) != 0 {
		t.Fatal("successful cleanup retained residual IDs")
	}
	assertTabbySecretCount(t, secrets.memSecretStore, 1)
	if exists, err := secrets.Exists(context.Background(), oldID); err != nil || !exists {
		t.Fatalf("cleanup deleted old secret: %v", err)
	}
}

type tabbyDeadlineCleanupService struct {
	capability.TabbyImportService
	deadline time.Time
	canceled error
}

func (s *tabbyDeadlineCleanupService) DeleteSecret(ctx context.Context, _ credential.SecretID) error {
	s.deadline, _ = ctx.Deadline()
	s.canceled = ctx.Err()
	return context.DeadlineExceeded
}

func TestTabbyImport_RollbackDeadlinePreservesResidualOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := &tabbyDeadlineCleanupService{}
	plan := &importPlan{pendingSecrets: []credential.SecretID{"minted"}}
	started := time.Now()
	err := rollbackTabbySecrets(ctx, svc, plan)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost cleanup deadline error: %v", err)
	}
	if svc.canceled != nil || svc.deadline.Before(started) || svc.deadline.After(time.Now().Add(tabbyCleanupTimeout)) {
		t.Fatalf("cleanup did not receive an independent bounded context: deadline=%s, error=%v", svc.deadline, svc.canceled)
	}
	if len(plan.pendingSecrets) != 1 || plan.pendingSecrets[0] != "minted" {
		t.Fatal("timed-out cleanup lost ownership of the minted secret")
	}
}

func TestTabbyImport_OneShotRetainsFailedCleanupBeforeFreshRetry(t *testing.T) {
	ps := profile.NewJSONStore(filepath.Join(t.TempDir(), "p.json"))
	secrets := &tabbyFaultSecrets{memSecretStore: newTestStore()}
	oldID, err := secrets.Create(context.Background(), credential.NewSecret("existing"))
	if err != nil {
		t.Fatal(err)
	}
	secrets.failCreateAt = 3
	secrets.failDelete = true
	var server *WSServer
	conn := tabbyRepairConnection(t, ps, secrets, func(s *WSServer) { server = s })
	config := "profiles:\n  - id: a\n    type: ssh\n    options:\n      host: a.example\n      password: password-a\n  - id: b\n    type: ssh\n    options:\n      host: b.example\n      password: password-b\n"
	params := map[string]any{"config": config}
	message := tabbyWireError(t, jsonrpcCall(t, conn, "profiles.importTabby", params))
	if !strings.Contains(message, "secret mint refused") || !strings.Contains(message, "secret cleanup refused") {
		t.Fatalf("original or cleanup failure was lost: %s", message)
	}
	assertTabbySecretCount(t, secrets.memSecretStore, 2)
	server.planMu.Lock()
	if len(server.planStore) != 1 {
		t.Errorf("one-shot cleanup ownership entries = %d, want 1", len(server.planStore))
	}
	for _, entry := range server.planStore {
		if entry.inProgress || !entry.plan.cleanupOnly || len(entry.plan.pendingSecrets) != 1 {
			t.Error("failed one-shot did not release cleanup-only ownership")
		}
		if entry.plan.profiles != nil || entry.plan.groups != nil || entry.plan.creds != nil {
			t.Error("abandoned one-shot retained decrypted source")
		}
	}
	server.planMu.Unlock()
	message = tabbyWireError(t, jsonrpcCall(t, conn, "profiles.importTabby", params))
	if !strings.Contains(message, "secret cleanup refused") {
		t.Fatalf("fresh one-shot ignored pending cleanup: %s", message)
	}
	secrets.faultMu.Lock()
	creates := secrets.creates
	secrets.failDelete = false
	secrets.faultMu.Unlock()
	if creates != 3 {
		t.Fatalf("fresh one-shot minted before cleanup: %d calls", creates)
	}
	tabbyWireResult(t, jsonrpcCall(t, conn, "profiles.importTabby", params), "profiles.importTabby.schema.json")
	assertTabbySecretCount(t, secrets.memSecretStore, 3)
	if exists, err := secrets.Exists(context.Background(), oldID); err != nil || !exists {
		t.Fatalf("cleanup deleted a preexisting secret: %v", err)
	}
	server.planMu.Lock()
	defer server.planMu.Unlock()
	if len(server.planStore) != 0 {
		t.Fatalf("successful one-shot retained %d plans", len(server.planStore))
	}
}

func TestTabbyPlanStore_PreservesCleanupAndClaimedEntries(t *testing.T) {
	server := &WSServer{planStore: make(map[string]*planEntry)}
	expired := time.Now().Add(-2 * planTTL)
	active := &importPlan{creds: []credentialPlan{{secret: "active-source"}}}
	residual := &importPlan{creds: []credentialPlan{{secret: "abandoned-source"}}, pendingSecrets: []credential.SecretID{"minted"}}
	retryable := &importPlan{pendingSecrets: []credential.SecretID{"retry-owned"}}
	server.planStore["active"] = &planEntry{plan: active, createdAt: expired, inProgress: true}
	server.planStore["residual"] = &planEntry{plan: residual, createdAt: expired}
	server.planStore["retryable"] = &planEntry{plan: retryable, createdAt: time.Now(), inProgress: true}
	server.planStore["unused"] = &planEntry{plan: &importPlan{}, createdAt: expired}
	if _, err := server.storePlan(&importPlan{}); err != nil {
		t.Fatal(err)
	}
	if server.planStore["active"] == nil || len(active.creds) != 1 {
		t.Fatal("capacity sweep scavenged an active plan")
	}
	if server.planStore["unused"] != nil {
		t.Fatal("unused expired plan was not evicted")
	}
	if !residual.cleanupOnly || residual.creds != nil || len(residual.pendingSecrets) != 1 {
		t.Fatal("expired plan lost cleanup ownership or retained decrypted source")
	}
	token, got := server.claimCleanupPlan()
	if token != "residual" || got != residual {
		t.Fatal("cleanup claimed a live preview instead of the abandoned plan")
	}
	if _, got := server.claimCleanupPlan(); got != nil {
		t.Fatal("cleanup scavenged a claimed plan")
	}
	if server.claimPlan("residual") != nil {
		t.Fatal("cleanup-only plan was executable")
	}
	server.releasePlan(token)
	if server.claimPlan("residual") != nil {
		t.Fatal("released cleanup-only plan was executable")
	}
}

func TestTabbyPlanStore_RefusesCapacityRatherThanLosingCleanup(t *testing.T) {
	server := &WSServer{planStore: make(map[string]*planEntry)}
	for i := range maxPlans {
		server.planStore[fmt.Sprint(i)] = &planEntry{
			plan:      &importPlan{pendingSecrets: []credential.SecretID{credential.SecretID(fmt.Sprint(i))}},
			createdAt: time.Now().Add(-2 * planTTL),
		}
	}
	if _, err := server.storePlan(&importPlan{}); err == nil {
		t.Fatal("full cleanup store admitted another plan")
	}
	if len(server.planStore) != maxPlans {
		t.Fatal("capacity sweep discarded cleanup ownership")
	}
	for _, entry := range server.planStore {
		if len(entry.plan.pendingSecrets) != 1 || !entry.plan.cleanupOnly {
			t.Fatal("capacity sweep erased residual state")
		}
	}
}
