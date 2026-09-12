package sessionruntime

import (
	"errors"
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
// with. Nothing in this file rewrites a schedule or relaxes one: a schedule
// that a real runtime cannot honour is listed with the assertion that fires
// and the reason, and the list is asserted to be EXHAUSTIVE over the schedules
// in the contract file, so a schedule added there cannot be silently absent
// here.
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
	// unhonoured names the assertion this schedule fails against a real
	// runtime. Empty means the schedule must pass. A schedule whose failure is
	// named here is not "skipped": the named assertion IS asserted, so the day
	// the contract changes the entry has to be changed with it.
	unhonoured string
	// why is the reason, in the terms of what a real terminal is, that the
	// named assertion cannot hold. It is required exactly when unhonoured is
	// set, and the test below checks that both are present or neither is.
	why string
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
			// Why the schedule cannot be judged here: its evidence for "the
			// ingest path discarded nothing" is the model's screen, a window on
			// the TEXT it has ingested, and it requires the screen to END with
			// the bytes of the last ingest ("the last line\r\n"). A real
			// screen is the grid the program drew: it ends with the ROW that
			// line was written on, and a grid has no trailing newline. The rule
			// itself is asserted on this same runtime, with evidence a real
			// screen can give, by TestAWedgedConsumerCostsNoIngest.
			name: "scheduleConsumerThatNeverReads",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleConsumerThatNeverReads(s)
			},
			unhonoured: "delivery/a-wedged-consumer-costs-no-ingest",
			why:        "the contract assumes a screen is a window on ingested TEXT and ends with the last line's bytes; a real screen is the grid, which ends with the row",
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
			// Why the two hostile-sequence schedules cannot be judged here:
			// both require the runtime to have DISCARDED bytes of an
			// unterminated sequence — a loss count above zero and
			// CompletenessLostIngest — because the model holds that sequence
			// and trims it at MaxPendingSequence. This runtime holds no part of
			// a sequence at all: the parser it feeds does (ADR-0065,
			// contract.go's Emulator), so there is nothing here to trim and
			// nothing to report, and IngestState.Pending is empty BY
			// CONSTRUCTION rather than because a bound was reached. That is NOT
			// a claim about the emulator: whether the library bounds what IT
			// holds is unmeasured here, and this bead neither asserts nor
			// denies it. The halves of both schedules that need no loss — the
			// work bound, no pending sequence left, and the refusal of an
			// oversized call changing nothing — pass.
			name: "scheduleHostileUnterminatedOSC",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleHostileUnterminatedOSC(s)
			},
			unhonoured: "hostile/the-dropped-sequence-is-reported",
			why:        "the contract assumes the runtime buffers an unterminated sequence and trims it; the parser is the emulator's, so this runtime holds nothing to trim or report",
		},
		{
			name: "scheduleHostileOversizedDCS",
			run: func(t *testing.T) error {
				s, _, _ := realRuntime(t)
				return scheduleHostileOversizedDCS(s)
			},
			unhonoured: "hostile/oversized-dcs-is-bounded",
			why:        "the same assumption at the point it is stated as a count: the runtime discarded nothing, so it counts nothing",
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
// real runtime and holds each one to its answer: the sentence it must pass
// with, or the named assertion a real terminal cannot let it reach.
func TestTheContractSchedulesJudgeTheRealRuntime(t *testing.T) {
	for _, s := range realSchedules() {
		t.Run(s.name, func(t *testing.T) {
			err := s.run(t)
			switch {
			case s.unhonoured == "" && s.why != "":
				t.Fatalf("the entry names a reason but no assertion, so nothing is being asserted")
			case s.unhonoured != "" && s.why == "":
				t.Fatalf("the entry names assertion %q and no reason", s.unhonoured)
			case s.unhonoured == "":
				if err != nil {
					t.Fatalf("the schedule must pass against the real runtime: %v", err)
				}
			case err == nil:
				// The gap CLOSED. A skip that is no longer true is a lie the
				// next reader would act on, so this fails rather than passing
				// quietly: the entry has to be removed with the reason.
				t.Fatalf("the schedule now PASSES against the real runtime, so %q is stale: delete the entry and let it run", s.unhonoured)
			default:
				var ae *assertionError
				if !errors.As(err, &ae) {
					t.Fatalf("the schedule returned %v, want the named assertion %q it cannot reach", err, s.unhonoured)
				}
				if ae.Assertion != s.unhonoured {
					t.Fatalf("the schedule failed at %q; the entry says a real runtime cannot reach %q, so one of the two is stale: %v",
						ae.Assertion, s.unhonoured, err)
				}
				t.Skipf("the schedule stops at %q — %s", ae.Assertion, s.why)
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
