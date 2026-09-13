package proto

// THE TOOL TOKEN'S PLACE ON THE WIRE (nocx-50w7p.16, AC4).
//
// A bearer that crosses a wire has two properties worth pinning here rather than
// leaving to the code that happens to use it: its absence has to be ABSENCE (not
// an empty string a reader could take for a value), and the helper must never
// hand it back. The second is the one that decays silently — a field added to a
// result "so the caller can see what it sent" is how a secret starts appearing
// in records and logs.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTheToolTokenIsOptionalAndAbsenceIsAbsence — this package's own rule,
// stated for the field: a request that carries no bearer says so by not naming
// one. An `agentToolToken: ""` on the wire is a value, and a helper reading it
// could not tell "no bearer" from "a bearer that is empty" — which is the
// distinction the endpoint's refusal depends on.
func TestTheToolTokenIsOptionalAndAbsenceIsAbsence(t *testing.T) {
	without, err := json.Marshal(SSHSpawnParams{Destination: SSHDestination{Host: "h"}})
	if err != nil {
		t.Fatalf("marshal without a token: %v", err)
	}
	if strings.Contains(string(without), "agentToolToken") {
		t.Fatalf("a request with no bearer named one: %s", without)
	}

	with, err := json.Marshal(SSHSpawnParams{
		Destination:    SSHDestination{Host: "h"},
		AgentToolToken: strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatalf("marshal with a token: %v", err)
	}
	if !strings.Contains(string(with), `"agentToolToken":"`+strings.Repeat("ab", 32)+`"`) {
		t.Fatalf("a request with a bearer did not carry it: %s", with)
	}
}

// TestTheHelperNeverHandsTheBearerBack — the token is the caller's, given to the
// far launcher and to nothing else. Every result shape this op can answer with
// is marshalled here and has to be free of it: a bearer echoed into a result is
// a bearer in whatever the caller then writes down.
func TestTheHelperNeverHandsTheBearerBack(t *testing.T) {
	token := strings.Repeat("ab", 32)
	answers := map[string]any{
		"SpawnResult":        SpawnResult{},
		"SessionsResult":     SessionsResult{},
		"SessionEntry":       SessionEntry{},
		"LaunchRecord":       LaunchRecord{},
		"SSHLaunchRecord":    SSHLaunchRecord{},
		"LocalLaunchRecord":  LocalLaunchRecord{},
		"CloseSessionResult": CloseSessionResult{},
		"SignalResult":       SignalResult{},
		"AdoptLifecycleRslt": AdoptLifecycleResult{},
		"LifecycleLaunch":    LifecycleLaunch{},
		"Notification":       Notification{},
		"Observation":        Observation{},
		"SpawnParamsLocal":   SpawnParams{},
		"SSHDestinationEcho": SSHDestination{},
	}
	for name, answer := range answers {
		body, err := json.Marshal(answer)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if strings.Contains(string(body), token) || strings.Contains(string(body), "agentToolToken") {
			t.Fatalf("%s carries the pane's bearer: %s", name, body)
		}
	}
}
