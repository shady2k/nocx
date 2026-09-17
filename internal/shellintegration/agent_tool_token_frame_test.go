package shellintegration

import (
	"strings"
	"testing"
)

// THE TOOL TOKEN'S TRANSPORT (nocx-50w7p.16).
//
// This file asserts the CONTRACTS rather than a live run, and deliberately:
// the token's road is frame 2 → stage-1's parser → an unlinked descriptor →
// capabilityFromDescriptor, and the end of that road is exercised by the
// real-shell tests, which cannot run on a host without bash 3.2 installed
// (channel_exec_test.go names the fixture). What is asserted here is every
// joint where this change could silently drop the value — which is exactly
// how the first version of it was wrong: SecretFrame carried the token and
// the reader read a third line, while the template still wrote only two lines
// to the descriptor, so the value died in the middle and every test passed.

// TestSecretFrameCarriesTheToolTokenOnlyWhenThereIsOne — a session with no
// tool surface produces the frame it produced before this existed, byte for
// byte, and a session with one carries it as the last line.
func TestSecretFrameCarriesTheToolTokenOnlyWhenThereIsOne(t *testing.T) {
	base := LaunchOptions{
		Enhanced: true, SessionID: "s-1", Domain: "d", Epoch: 3,
		Capability: strings.Repeat("ab", 32), Recovery: strings.Repeat("cd", 32),
		LifecyclePort: 4444,
	}
	without, err := SecretFrame(base)
	if err != nil {
		t.Fatalf("SecretFrame: %v", err)
	}
	withToken := base
	withToken.AgentToolToken = strings.Repeat("ef", 32)
	with, err := SecretFrame(withToken)
	if err != nil {
		t.Fatalf("SecretFrame with a token: %v", err)
	}
	if strings.Contains(string(without), withToken.AgentToolToken) {
		t.Fatal("the frame carried the tool token before there was one")
	}
	if got, want := string(with), string(without)+withToken.AgentToolToken+"\n"; got != want {
		t.Fatalf("the token is not the frame's last line:\n got %q\nwant %q", got, want)
	}
}

// TestSecretFrameRefusesANonHexToolToken — the token is a bearer and reaches a
// shell assignment, so a value that is not the hex this transport assumes is
// refused at the writer rather than quoted into a script.
func TestSecretFrameRefusesANonHexToolToken(t *testing.T) {
	opts := LaunchOptions{
		Enhanced: true, SessionID: "s-1", Domain: "d", Epoch: 3,
		Capability: strings.Repeat("ab", 32), Recovery: strings.Repeat("cd", 32),
		LifecyclePort: 4444, AgentToolToken: "not-hex-at-all",
	}
	if _, err := SecretFrame(opts); err == nil {
		t.Fatal("SecretFrame accepted a tool token that is not lowercase hex")
	}
}

// TestStage1ParsesTheOptionalToolTokenAndWritesItToTheDescriptor — the joint
// that was missing. The token is a FOURTH line of the frame, so the port can no
// longer be "everything after the second newline", and the descriptor the shell
// later reads must be written with three lines rather than two.
func TestStage1ParsesTheOptionalToolTokenAndWritesItToTheDescriptor(t *testing.T) {
	body, err := Stage1Frame(ShellBash, LaunchOptions{SessionID: "s-1"})
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, `AT=${LP#*"$NL"}; LP=${LP%%"$NL"*}`) {
		t.Fatal("stage-1 does not split the optional tool token off the port line")
	}
	// The token is OPTIONAL, and the pattern is the recovery fence's — invalid
	// characters only. The capability's `''|*[!0-9a-f]*` has an empty
	// alternative whose body REFUSES, which is right for a capability (an
	// empty one is broken) and wrong here: it refused every legacy frame as
	// secret-malformed, which is how this was found.
	if !strings.Contains(got, `case "$AT" in *[!0-9a-f]*) Q `+OutcomeToken(OutcomeSecretMalformed)+` ;; esac`) {
		t.Fatal("stage-1 does not validate the tool token's alphabet, or refuses an absent one")
	}
	if strings.Contains(got, `case "$AT" in ''|`) {
		t.Fatal("the optional tool token took the capability's pattern, so its absence is refused")
	}
	if !strings.Contains(got, `printf "%s\n%s\n%s\n" "$CP" "$FN" "$AT" >&`) {
		t.Fatal("stage-1 does not write the tool token to the capability descriptor")
	}
	if strings.Contains(got, `"$CP" "$FN" >&`) {
		t.Fatal("stage-1 still writes a two-line descriptor")
	}
}

// TestTheCapabilityReaderAssignsANonExportedToolToken — the far end of the
// road: the descriptor's third line becomes a NON-EXPORTED shell variable, so a
// child of the shell inherits nothing, and a pane with no token is left with an
// empty one rather than a missing assignment.
func TestTheCapabilityReaderAssignsANonExportedToolToken(t *testing.T) {
	block := capabilityFromDescriptor(bashUnsetExport)
	if !strings.Contains(block, `IFS= read -r __nocx_agent_token <&"${`+CapabilityFDEnv+`}"`) {
		t.Fatal("the descriptor reader does not read the tool token")
	}
	if !strings.Contains(block, "__nocx_agent_token=''") {
		t.Fatal("the descriptor reader does not declare the tool token, so an absent line is unset rather than empty")
	}
	if !strings.Contains(block, bashUnsetExport+" __nocx_agent_token") {
		t.Fatal("the descriptor reader leaves the tool token exported: a user rc under `set -a` would publish it in /proc/<pid>/environ")
	}
	// And the token is NOT in the agent-env block, which is the rule
	// TestStage1CarriesOnlyNonSecretAgentPaths keeps.
	env := stage1AgentEnv(LaunchOptions{AgentHelperPath: "/h", AgentToolSocketPath: "/s", AgentToolToken: "ab"})
	if strings.Contains(env, "ab") {
		t.Fatalf("the agent-env block carried a bearer: %q", env)
	}
}
