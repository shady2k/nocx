package app

// nocx-yww2 end to end: a local tab whose own rc hands the shell to another
// terminal's integration is NAMED, in the product, as a startup that did not
// return — not as ten seconds of silence.
//
// This is the check that watches the user do it. Everything below the websocket
// is the production composition: the real App, the real login-shell resolver
// seam, the real local tier, a real interactive shell on a real pty reading a
// real rcfile, the real lifecycle channel, the real bootstrap progress
// descriptor, the real transport and the SHIPPED handshake bound. The one
// injection is which shell the account names, because a machine's own answer
// would make the test report the developer's account rather than the product.
//
// The bound is not shortened, and the ten seconds it costs are the price of
// the rule that a test may not depend on timing: a shortened one made the
// assertion depend on how fast the shell came up, which is a property of the
// machine — green here and red inside the loaded CI container, where an
// emulated zsh had not finished starting when the short clock ran out.
//
// The fixture is the shape measured on the shipped v0.1.0: line 1 of the
// user's rc execs a wrapper, the wrapper keeps our descriptors and starts its
// own bare interactive shell without them. That is Kiro CLI, Amazon Q, Fig and
// Warp's hooks — every one of them installs into exactly this file.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// bootstrapHijackRC writes the user rc that hands the shell away, plus the
// wrapper it hands it to.
func bootstrapHijackRC(t *testing.T, home, rcName, shell, noRCFlags string) {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "foreign-term")
	body := "#!/bin/sh\n" +
		// No exec: the wrapper stays alive holding fds 3 and 4, so nothing
		// downstream ever sees an EOF — which is why the handshake bound was
		// the only signal there was.
		shell + " " + noRCFlags + " -i 3>&- 4>&-\n"
	if err := os.WriteFile(wrapper, []byte(body), 0o700); err != nil { //nolint:gosec // a fixture that must be executable
		t.Fatalf("write wrapper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, rcName), []byte("exec '"+wrapper+"'\n"), 0o600); err != nil {
		t.Fatalf("write user rc: %v", err)
	}
}

// openLocalIntegration opens a local session over the real socket and returns
// its id. Local integration is backend-owned rather than a renderer request.
func openLocalIntegration(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	resp := reachJSONRPCCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
		"kind": "local",
	})
	var envelope struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode open response: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("open failed: %d %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result.SessionID
}

// waitForIntegrationStatus reads session.integrationChanged frames for a
// session until one leaves `starting` — the state a session sits in until it
// either integrates or gives up, which is precisely the interval this bead is
// about. It waits on the product's own transition, never on a duration.
func waitForIntegrationStatus(t *testing.T, conn *websocket.Conn, sid string) (status, reason string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var frame struct {
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Status    string `json:"status"`
				Reason    string `json:"reason"`
			} `json:"params"`
		}
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}
		if frame.Method != "session.integrationChanged" || frame.Params.SessionID != sid {
			continue
		}
		if frame.Params.Status == transport.IntegrationStarting {
			continue
		}
		return frame.Params.Status, frame.Params.Reason
	}
	t.Fatal("the session never left `starting`: the degrade is exactly as silent as before")
	return "", ""
}

// bootstrapStack starts the production App's transport and returns a connected
// client, with the account's login shell injected.
func bootstrapStack(t *testing.T, shellPath string) *websocket.Conn {
	t.Helper()
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	f, ok := a.Pty.(*localPTYFactory)
	if !ok {
		t.Fatalf("Pty factory is %T, want *localPTYFactory", a.Pty)
	}
	f.shells = fixedShell{path: shellPath}
	if f.noteBootstrapStage == nil {
		t.Fatal("the composition root wired no bootstrap progress sink: the stage could never reach the product")
	}
	ctx := context.Background()
	if err := a.Transport.Start(ctx); err != nil {
		t.Fatalf("transport start: %v", err)
	}
	t.Cleanup(func() { _ = a.Transport.Stop(ctx) })
	return reachConnectWS(t, a.Transport)
}

// The acceptance criterion, both tiers: the owner's machine carries the
// foreign integration's block in ~/.bashrc AND ~/.zshrc, and macOS logs its
// users into zsh.
func TestLocalSession_AUserRcThatTakesTheShellIsNamedInTheProduct(t *testing.T) {
	tests := []struct {
		name   string
		bin    string
		rcName string
		noRC   string
	}{
		{name: "zsh", bin: "zsh", rcName: ".zshrc", noRC: "-f"},
		{name: "bash", bin: "bash", rcName: ".bashrc", noRC: "--norc --noprofile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shellPath, err := exec.LookPath(tt.bin)
			if err != nil {
				t.Fatalf("%s is not installed: %v — the shell this tier exists to prove must be present, "+
					"or the suite reports a broken product as a skip (nocx-gd84)", tt.bin, err)
			}
			home := storagetest.IsolateWithHome(t)
			bootstrapHijackRC(t, home, tt.rcName, shellPath, tt.noRC)

			conn := bootstrapStack(t, shellPath)
			sid := openLocalIntegration(t, conn)

			status, reason := waitForIntegrationStatus(t, conn, sid)
			if status != transport.IntegrationConventional {
				t.Errorf("status = %q, want conventional", status)
			}
			if reason != string(ssh.ReasonStartupDidNotReturn) {
				t.Errorf("reason = %q, want %q — the user is told their startup did not return, "+
					"not that a timer expired", reason, ssh.ReasonStartupDidNotReturn)
			}
		})
	}
}

// And the paired case on an ordinary machine, through the same stack: a rc
// that returns lets the shell prove itself, so the session reports `integrated`
// and never reaches a reason at all. Without it the test above would pass just
// as well against a product that called every session hijacked.
func TestLocalSession_AnOrdinaryRcStillIntegrates(t *testing.T) {
	for _, tt := range []struct{ bin, rcName string }{{"zsh", ".zshrc"}, {"bash", ".bashrc"}} {
		t.Run(tt.bin, func(t *testing.T) {
			shellPath, err := exec.LookPath(tt.bin)
			if err != nil {
				t.Fatalf("%s is not installed: %v (nocx-gd84)", tt.bin, err)
			}
			home := storagetest.IsolateWithHome(t)
			if werr := os.WriteFile(filepath.Join(home, tt.rcName),
				[]byte("export USER_RC_RAN=yes\n"), 0o600); werr != nil {
				t.Fatalf("write user rc: %v", werr)
			}

			conn := bootstrapStack(t, shellPath)
			sid := openLocalIntegration(t, conn)

			status, reason := waitForIntegrationStatus(t, conn, sid)
			if status != transport.IntegrationIntegrated {
				t.Errorf("status = %q (reason %q), want integrated", status, reason)
			}
			if reason != "" {
				t.Errorf("reason = %q, want none: an integrated session has nothing to explain", reason)
			}
		})
	}
}
