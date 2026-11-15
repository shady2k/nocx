package pi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/harness"
)

func TestConnectorSweepsOnlyDeadOwnerConfigsAtStartup(t *testing.T) {
	dead := exec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	configs := filepath.Join(data, "pi", "configs")
	if err := os.MkdirAll(configs, 0o700); err != nil {
		t.Fatal(err)
	}
	deadName := fmt.Sprintf("session-%d-orphan.json", dead.Process.Pid)
	liveName := fmt.Sprintf("session-%d-pending.json", os.Getpid())
	for _, name := range []string{deadName, liveName, "session-unknown.json", "unrelated.json"} {
		if err := os.WriteFile(filepath.Join(configs, name), []byte("credential"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newTestConnector(data, []string{"PI_OFFLINE=1"})
	defer func() { _ = c.Close(context.Background()) }()
	if _, err := os.Stat(filepath.Join(configs, deadName)); !os.IsNotExist(err) {
		t.Fatalf("dead owner's credential config remains: %v", err)
	}
	for _, name := range []string{liveName, "session-unknown.json", "unrelated.json"} {
		if _, err := os.Stat(filepath.Join(configs, name)); err != nil {
			t.Fatalf("removed unproven orphan %s: %v", name, err)
		}
	}
	// Another incarnation starting must not repeat the connector's startup sweep.
	late := filepath.Join(configs, deadName)
	if err := os.WriteFile(late, []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	launch, err := c.Start(context.Background(), harness.TurnSpec{ConversationID: "later"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = launch.Cleanup() }()
	if _, err := os.Stat(late); err != nil {
		t.Fatalf("incarnation swept configs after connector startup: %v", err)
	}
	for _, env := range launch.Env {
		if path, ok := strings.CutPrefix(env, "NOCX_PI_SESSION_FILE="); ok {
			if !strings.HasPrefix(filepath.Base(path), fmt.Sprintf("session-%d-", os.Getpid())) {
				t.Fatalf("config has no backend owner: %s", path)
			}
			return
		}
	}
	t.Fatal("launch has no config path")
}
