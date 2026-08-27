package assistant

// The tap has to be provable, because a diagnostic nobody has watched work is
// the thing you reach for on the day it is wrong. Two facts: both halves of
// the exchange land in the file, and the API key does not.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWireTap_RecordsBothHalvesAndNeverTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.log")
	old := wireLogPath
	wireLogPath = path
	defer func() { wireLogPath = old }()

	grant, dir := testDirGrant(t, autonomousMatrix())
	file := filepath.Join(dir, "a.txt")
	writeFile(t, file, "wire tap fixture")
	args, err := json.Marshal(map[string]string{"path": file})
	if err != nil {
		t.Fatalf("marshal files.read args: %v", err)
	}
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "files.read",
		args: string(args),
	}))
	defer srv.Close()
	cl, _ := newClient(nil, os.DirFS(realToolsFS))
	p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
	if askErr := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); askErr != nil {
		t.Fatalf("Ask: %v", askErr)
	}
	b, err := os.ReadFile(path) // #nosec G304 — the test wrote this path itself
	if err != nil {
		t.Fatalf("the tap wrote nothing: %v", err)
	}
	got := string(b)
	for _, want := range []string{"REQUEST POST", "files.read", "RESPONSE 200", "RESPONSE BODY", "chat.completion.chunk"} {
		if !strings.Contains(got, want) {
			t.Fatalf("wire log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sk-test-123") {
		t.Fatalf("THE KEY IS IN THE WIRE LOG:\n%s", got)
	}
	t.Logf("wire log (%d bytes) starts:\n%s", len(got), got[:min(1200, len(got))])
}
