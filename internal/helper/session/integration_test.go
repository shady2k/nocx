package session_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// TestHelperHostIntegrationWithoutInstalledScript is the helper-host happy path:
// the helper owns the shell PTY, while the renderer-facing block, cwd and exit
// facts remain the OSC bytes in that PTY stream. The installed integration tree
// is deliberately absent, so a passing test cannot be explained by SFTP
// delivery or an rc-file hook sourced from the host.
func TestHelperHostIntegrationWithoutInstalledScript(t *testing.T) {
	shellPath, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("look up bash: %v", err)
	}
	home := t.TempDir()
	installed := filepath.Join(home, ".nocx", "integration")
	if removeErr := os.RemoveAll(installed); removeErr != nil {
		t.Fatalf("remove installed integration tree: %v", removeErr)
	}
	if _, statErr := os.Stat(installed); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("installed integration tree = %v, want absent", statErr)
	}

	sink := newSink()
	spawner := session.NewLocalSpawner(discardLog(), session.Shell{Path: shellPath})
	svc := newService(t, sink, spawner, session.Limits{})
	entry := call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{
		Cwd: home, Cols: 80, Rows: 24, Env: map[string]string{"HOME": home},
	}).Entry

	sub := proto.SubscriberID("1234567890abcdef1234567890abcdef")
	subRaw, err := proto.SessionBytes(string(sub))
	if err != nil {
		t.Fatalf("subscriber: %v", err)
	}
	attached := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, Fresh: true, RequestWrite: true,
	})

	awaitHelperSink(t, sink, subRaw, "the helper's initial prompt and cwd", func(out string) bool {
		return strings.Contains(out, "\x1b]133;A\a") &&
			strings.Contains(out, "\x1b]133;B\a") &&
			strings.Contains(out, "\x1b]7;")
	})

	rawSession, err := proto.SessionBytes(entry.Session.Session)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	beforeCommand := len(sink.bytesFor(subRaw))
	// A real user command opens and closes the renderer's block through the
	// stream. The helper only moves these bytes; it never parses them.
	svc.SessionData(callCtx(), proto.SessionFrame{
		Session: rawSession, Subscriber: subRaw, Epoch: attached.Write.Epoch,
		Payload: []byte("false\n"),
	})
	awaitHelperSink(t, sink, subRaw, "the command's block close and exit status", func(out string) bool {
		if len(out) <= beforeCommand {
			return false
		}
		out = out[beforeCommand:]
		start := strings.Index(out, "\x1b]133;C\a")
		finish := strings.Index(out, "\x1b]133;D;1\a")
		return start >= 0 && finish > start
	})

	beforeCwd := len(sink.bytesFor(subRaw))
	svc.SessionData(callCtx(), proto.SessionFrame{
		Session: rawSession, Subscriber: subRaw, Epoch: attached.Write.Epoch,
		Payload: []byte("cd /tmp\n"),
	})
	awaitHelperSink(t, sink, subRaw, "the command's updated cwd", func(out string) bool {
		if len(out) <= beforeCwd {
			return false
		}
		return strings.Contains(out[beforeCwd:], "\x1b]7;") &&
			strings.Contains(out[beforeCwd:], "/tmp")
	})

	if _, err := os.Stat(installed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed integration tree after helper spawn = %v, want absent", err)
	}
}

func awaitHelperSink(t *testing.T, sink *recordingSink, subscriber [16]byte, what string, want func(string) bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		next := sink.waiter()
		if want(string(sink.bytesFor(subscriber))) {
			return
		}
		select {
		case <-next:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s: helper emitted no required marker bytes", what)
		}
	}
}
