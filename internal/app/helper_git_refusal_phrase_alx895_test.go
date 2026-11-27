package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestGitAxisRefusalNamesTheChoiceNotTheReasonToken is the user-visible half of
// the contract: a Script host's refusal tells a person what the host does and
// what would change it, and never leaks the wire's closed-set token.
func TestGitAxisRefusalNamesTheChoiceNotTheReasonToken(t *testing.T) {
	far, panes, _ := gitFarStand(t, hostmode.Script)
	selection := helperGitFactory(far, func() *panedriver.Driver { return panes })(
		&gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"})
	if selection.Factory != nil || selection.Refusal == nil {
		t.Fatalf("selection = %+v, want a refusal on a Script host", selection)
	}
	message := selection.Refusal.Message
	if strings.Contains(message, "mode-script") {
		t.Errorf("refusal message = %q, want a phrase rather than the mode-script token", message)
	}
	if !strings.Contains(message, "Script") {
		t.Errorf("refusal message = %q, want it to name the Script method", message)
	}
	if !strings.Contains(message, "nocx helper") {
		t.Errorf("refusal message = %q, want it to name the nocx helper", message)
	}
}

// TestEveryFarHelperReasonHasAPhraseForGit keeps the closed set closed: a
// reason added tomorrow reaches a person as a sentence, not as
// "far helper unavailable: <token>".
func TestEveryFarHelperReasonHasAPhraseForGit(t *testing.T) {
	for _, reason := range []farhelper.Reason{
		farhelper.ReasonModeRaw,
		farhelper.ReasonModeScript,
		farhelper.ReasonConsentPending,
		farhelper.ReasonConsentDenied,
		farhelper.ReasonUnsupportedPlatform,
		farhelper.ReasonInstallFailed,
		farhelper.ReasonExecForbidden,
		farhelper.ReasonVersionMismatch,
		farhelper.ReasonHelperLost,
		farhelper.ReasonConnection,
	} {
		t.Run(string(reason), func(t *testing.T) {
			action := farhelper.ActionNone
			if reason == farhelper.ReasonModeRaw || reason == farhelper.ReasonModeScript {
				action = farhelper.ActionHostSettings
			}
			selection := gitAxisRefusal(reason, action)
			if selection.Refusal == nil {
				t.Fatal("gitAxisRefusal answered no refusal")
			}
			message := selection.Refusal.Message
			token := (&farhelper.Unavailable{Reason: reason, Action: action}).Error()
			if message == "" {
				t.Fatal("refusal message is empty")
			}
			if message == token {
				t.Fatalf("refusal message = %q, which is the wire token", message)
			}
			if strings.Contains(message, "far helper unavailable") {
				t.Errorf("refusal message = %q, want a phrase rather than the unavailable token", message)
			}
		})
	}
}

// TestGitOpenHelperLostCarriesAPhraseAndTheTypedRefusal covers the other two
// places Git reports the same closed set: acquiring a lost helper and any read
// whose lease ended with it. Both answer a sentence for the person, while
// errors.As still reaches the reason the transport's own branch reads.
func TestGitOpenHelperLostCarriesAPhraseAndTheTypedRefusal(t *testing.T) {
	far, panes, _ := gitFarStand(t, hostmode.Helper)
	selected := helperGitFactory(far, func() *panedriver.Driver { return panes })(
		&gitPaneSession{fakeRemoteSession: &fakeRemoteSession{host: "far.example"}, pane: "pane"})
	if selected.Factory == nil {
		t.Fatalf("selection = %+v, want a helper-backed factory", selected)
	}
	repo, outcome, err := selected.Factory.Open(t.Context(), fixtureRepo(t))
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("open = %+v, %v", outcome, err)
	}
	defer func() { _ = repo.Close() }()
	lease, err := far.Acquire(t.Context(), "conn")
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Client().Close(); err != nil {
		t.Fatal(err)
	}
	waittest.WaitFor(t, "helper loss", func() bool {
		status, _ := far.Axis("conn")
		return status.Reason == farhelper.ReasonHelperLost
	})

	_, _, err = selected.Factory.Open(t.Context(), "/repo")
	assertHelperLostPhrase(t, err)

	_, err = repo.Status(t.Context())
	assertHelperLostPhrase(t, err)
}

func assertHelperLostPhrase(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error is nil, want a helper-lost refusal")
	}
	message := err.Error()
	if strings.Contains(message, "far helper unavailable") || strings.Contains(message, "helper-lost") {
		t.Errorf("error = %q, want a phrase rather than the helper-lost token", message)
	}
	if !strings.Contains(message, "nocx helper") {
		t.Errorf("error = %q, want it to name the nocx helper", message)
	}
	var unavailable *farhelper.Unavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != farhelper.ReasonHelperLost {
		t.Fatalf("error = %v, want errors.As to reach farhelper.ReasonHelperLost", err)
	}
}
