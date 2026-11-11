package lifecyclepub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
)

func TestAttemptFactCarriesMeasuredDuration(t *testing.T) {
	start := time.Now()
	end := start.Add(2 * time.Second)
	beforeStart := start.Add(-time.Second)
	for _, tc := range []struct {
		name string
		end  *time.Time
		want bool
		ms   string
	}{
		{name: "open"},
		{name: "completed", end: &end, want: true, ms: "2000"},
		{name: "instant", end: &start, want: true, ms: "0"},
		{name: "negative interval", end: &beforeStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := lifecycle.AttemptOpen
			if tc.end != nil {
				state = lifecycle.AttemptCompleted
			}
			fact := attemptFact(lifecycle.ExecutionAttempt{StartedAt: start, CompletedAt: tc.end, State: state})
			raw, err := json.Marshal(fact)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			value, ok := wire["durationMs"]
			if ok != tc.want {
				t.Fatalf("durationMs present=%v, want %v: %s", ok, tc.want, raw)
			}
			if tc.want && string(value) != tc.ms {
				t.Fatalf("durationMs=%s, want %s", value, tc.ms)
			}
		})
	}
}
