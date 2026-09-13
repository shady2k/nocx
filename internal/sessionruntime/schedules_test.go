package sessionruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// The contract's schedules, run against the REAL runtime (nocx-ygxjv.9).
//
// contract_test.go's schedules take a Runtime and nothing else — that is what
// nocx-ygxjv.6 bought — so they are run here against the implementation this
// bead lands, constructed over instruments of its own (harness_test.go), and
// the sentences they fail with are the same sentences they fail the model
// with. Nothing in this file rewrites a schedule or relaxes one: EVERY schedule
// in the contract file runs here and every one of them passes, and the list is
// asserted to be EXHAUSTIVE over the schedules in the contract file, so a
// schedule added there cannot be silently absent here — or quietly skipped.
//
// There is no longer a way to record an exception. There was, for three
// schedules (nocx-ygxjv.11), and the reason their entries had one is the reason
// it is gone: each asserted a property in the SHAPE of one implementation —
// a screen that ends with the last line's bytes, a parser the runtime owns —
// rather than in terms of the behaviour, so a real runtime failed sentences
// that said nothing about it. A schedule stated in terms any runtime can honour
// has no exception to record, and one that cannot must be rewritten rather than
// listed: an entry here that said "a real terminal cannot reach this" would be
// this file conceding the thing it exists to check.
//
// The REMOVAL PAIRS are deliberately not here. A pair is the model switching
// one rule OFF (without(rule) in model_test.go) and asserting that a NAMED
// assertion then fires; a real runtime has no rules to remove, so that half is
// a statement about the model and stays in contract_test.go. What is judged
// here is the half that is a statement about the contract: every schedule, with
// every rule on, against the real runtime.

// realSchedule is one contract schedule and how a real runtime answers it.
type realSchedule struct {
	name string
	run  func(t *testing.T) error
}

// realSchedules is every schedule in contract_test.go, in the file's own
// order, each one built over a fresh runtime.
func realSchedules() []realSchedule {
	return []realSchedule{
		{
			name: "scheduleFenceAuthenticatedFirst",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleFenceAuthenticatedFirst(s)
			},
		},
		{
			name: "scheduleFenceSightedFirst",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleFenceSightedFirst(s)
			},
		},
		{
			// This one PASSES on a real runtime, and it is worth saying why:
			// the schedule never draws the sighted row on the screen, so its
			// guard — "the screen still holds the sighted content, so this
			// schedule would prove nothing" — is vacuous here, and the
			// assertion it guards passes for a reason the model's screen gave
			// it and a real one does not need. The rule it is about (the pin
			// outlives the screen moving underneath it) is asserted with real
			// evidence by TestThePinOutlivesTheScreenItWasTakenFrom, which
			// draws the row first and then scrolls the grid past it.
			name: "scheduleFenceSightedFirstSurvivesScreenTrim",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleFenceSightedFirstSurvivesScreenTrim(s)
			},
		},
		{
			name: "scheduleTakeoverWithInputQueued",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleTakeoverWithInputQueued(s)
			},
		},
		{
			name: "scheduleDisconnectAfterAdmission",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleDisconnectAfterAdmission(s)
			},
		},
		{
			name: "scheduleResizeDuringOutput",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleResizeDuringOutput(s)
			},
		},
		{
			name: "scheduleObserverResync",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleObserverResync(s)
			},
		},
		{
			name: "scheduleKeyEncodedAgainstModes",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleKeyEncodedAgainstModes(s)
			},
		},
		{
			name: "scheduleRendezvousExpiresUnjoined",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleRendezvousExpiresUnjoined(s)
			},
		},
		{
			name: "schedulePreconditionStaleAtExecution",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return schedulePreconditionStaleAtExecution(s)
			},
		},
		{
			name: "scheduleRuntimeFailure",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleRuntimeFailure(s)
			},
		},
		{
			name: "scheduleConsumerThatNeverReads",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleConsumerThatNeverReads(s)
			},
		},
		{
			name: "scheduleOneSessionCannotSpendAnothersAllowance",
			run: func(t *testing.T) error {
				// Two SESSIONS over ONE allowance, which is a piece of
				// construction rather than judgement: whether the allowance is
				// per session is unobservable unless the two share an account.
				allowance := NewAllowance()
				busy, _, _ := realRuntime(t, func(c *Config) {
					c.Allowance = allowance
					c.Incarnation = Incarnation{Session: busySessionID, Generation: 1}
				})
				other, _, _ := realRuntime(t, func(c *Config) {
					c.Allowance = allowance
					c.Incarnation = Incarnation{Session: otherSessionID, Generation: 1}
				})
				return scheduleOneSessionCannotSpendAnothersAllowance(busy, other)
			},
		},
		{
			name: "scheduleEffectDeliveryPolicy",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleEffectDeliveryPolicy(s)
			},
		},
		{
			name: "scheduleHostileRepeatCount",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleHostileRepeatCount(s)
			},
		},
		{
			name: "scheduleHostileUnterminatedOSC",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleHostileUnterminatedOSC(s)
			},
		},
		{
			name: "scheduleHostileOversizedDCS",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleHostileOversizedDCS(s)
			},
		},
		{
			name: "driveEffectKinds",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return driveEffectKinds(s)
			},
		},
		{
			name: "driveReportHole",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return driveReportHole(s)
			},
		},
	}
}

// TestTheContractSchedulesJudgeTheRealRuntime runs every schedule against a
// real runtime and holds each one to the sentence it must pass with. There is
// no second answer: a schedule a real runtime cannot pass is a schedule written
// in the shape of another implementation, and it is rewritten rather than
// recorded.
func TestTheContractSchedulesJudgeTheRealRuntime(t *testing.T) {
	for _, s := range realSchedules() {
		t.Run(s.name, func(t *testing.T) {
			if err := s.run(t); err != nil {
				t.Fatalf("the schedule must pass against the real runtime: %v", err)
			}
		})
	}
}

// TestEveryScheduleIsJudgedAgainstTheRealRuntime is the guard that keeps the
// list above honest: every `schedule…` and `drive…` function the contract file
// declares must appear in it. A schedule added to the contract and not run
// against the implementation fails here rather than being absent from the
// evidence.
func TestEveryScheduleIsJudgedAgainstTheRealRuntime(t *testing.T) {
	declared := contractScheduleNames(t)

	judged := map[string]bool{}
	for _, s := range realSchedules() {
		if judged[s.name] {
			t.Errorf("%s is judged twice", s.name)
		}
		judged[s.name] = true
	}
	for _, name := range declared {
		if !judged[name] {
			t.Errorf("the contract declares %s and no real runtime is judged by it", name)
		}
	}
	for name := range judged {
		if !contains(declared, name) {
			t.Errorf("%s is judged here and the contract does not declare it", name)
		}
	}
}

// contractScheduleNames reads contract_test.go and answers the schedules it
// declares, sorted.
func contractScheduleNames(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "contract_test.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse the contract's schedules: %v", err)
	}
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		name := fn.Name.Name
		if strings.HasPrefix(name, "schedule") || strings.HasPrefix(name, "drive") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// TestInvalidEventsChangeNothingOnTheRealRuntime drives the contract's
// invalid-event table — the same table, unchanged — against the real runtime.
// The one entry that needs a different CONSTRUCTION is the write gate, whose
// state a runtime BEGINS in: it is built here with [CompletenessUnknown] rather
// than switched on through the interface.
func TestInvalidEventsChangeNothingOnTheRealRuntime(t *testing.T) {
	for _, ev := range invalidEvents() {
		t.Run(ev.name, func(t *testing.T) {
			var opts []func(*Config)
			if ev.build != nil {
				opts = append(opts, func(c *Config) { c.Completeness = CompletenessUnknown })
			}
			s, _, _ := realRuntime(t, opts...)
			if err := ev.drive(s); err != nil {
				t.Fatalf("%v", err)
			}
		})
	}
}
