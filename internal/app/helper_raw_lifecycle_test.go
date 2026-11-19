package app

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// TestRawSSHDoesNotAskForLifecycleHello checks the real helper-hosted open
// seam: raw starts a plain shell, so it must not be handed a lifecycle launch
// that only an integration script could answer. Script-delivering modes keep
// the authenticated channel.
func TestRawSSHDoesNotAskForLifecycleHello(t *testing.T) {
	for _, mode := range []hostmode.Mode{
		hostmode.Raw, hostmode.Auto, hostmode.Script, hostmode.Helper,
	} {
		t.Run(string(mode), func(t *testing.T) {
			stand := startPaneStand(t)
			stand.opener.kernel = lifecyclepub.New(lifecycle.New(lifecycle.Options{}))

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			opened, selected, err := stand.opener.OpenHosted(ctx, session.Config{
				Kind: session.KindRemote,
				Host: "host.example",
				Remote: &ssh.ConnectConfig{
					User: "dev", Port: 22, AuthMode: "password",
					Secrets:            rememberedPassword{value: openPasswordFixturePassword},
					SecretID:           "sec:pane-publish:1",
					AuthorizedEndpoint: "host.example:22",
					IntegrationMode:    string(mode),
					Shell:              "bash",
				},
			}, "")
			if err != nil {
				t.Fatalf("open helper-hosted SSH pane in %s mode: %v", mode, err)
			}
			if !selected {
				t.Fatal("local helper declined an SSH pane")
			}
			t.Cleanup(func() { _ = opened.Session.Close() })

			requests := stand.daemon.spawner.sshSpawns()
			if len(requests) != 1 {
				t.Fatalf("helper received %d SSH spawns, want one", len(requests))
			}
			wantsLifecycle := mode.DeliversScripts()
			if wantsLifecycle {
				if opened.LifecycleLane == "" {
					t.Fatal("script-delivering SSH session has no lifecycle lane")
				}
				if requests[0].Lifecycle == nil {
					t.Fatal("script-delivering SSH spawn has no lifecycle launch")
				}
			} else {
				if opened.LifecycleLane != "" {
					t.Fatalf("raw SSH session created lifecycle lane %q; its plain shell cannot send HELLO", opened.LifecycleLane)
				}
				if requests[0].Lifecycle != nil {
					t.Fatal("raw SSH spawn received lifecycle launch; its plain shell cannot send HELLO")
				}
			}
		})
	}
}
