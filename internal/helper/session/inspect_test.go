package session

import (
	"encoding/binary"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// The inspector's rules, tested on whatever machine runs the suite.
//
// THE PLATFORM HALVES ARE NOT RUN HERE AND THAT IS NOT PRETENDED. What
// darwinSource does — nametomib, KERN_PROCARGS2, kern.proc.pid — happens
// inside a macOS kernel and this file never reaches it; what is driven here is
// the seam, with a source that answers from a table. That is deliberately
// where the defect this bead names lives: not in the syscalls, but in the
// parse of what they return and in the rule that a diagnostic the platform
// cannot answer is NAMED rather than left to look unchanged.
//
// TestTheHelperHasAnInspectorOnEveryPlatformItShipsOn below is the other half,
// and it is the one that runs the real syscalls — on Linux here, and on macOS
// in the CI job whose runner is a Mac.

// procargs2 builds a KERN_PROCARGS2 block the way the macOS kernel lays one
// out, so the parse is checked against the wire format rather than against
// itself.
func procargs2(argc uint32, execPath string, pad int, args []string, env []string) []byte {
	buf := binary.LittleEndian.AppendUint32(nil, argc)
	buf = append(buf, execPath...)
	buf = append(buf, 0)
	for range pad {
		buf = append(buf, 0)
	}
	for _, a := range args {
		buf = append(buf, a...)
		buf = append(buf, 0)
	}
	for _, e := range env {
		buf = append(buf, e...)
		buf = append(buf, 0)
	}
	return buf
}

func TestTheArgumentBlockIsParsedAsEachKernelLaysItOut(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    []byte
		format argvFormat
		want   []string
	}{
		{
			name:   "procargs2: the ordinary case, with the kernel's alignment padding between the exec path and argv[0]",
			raw:    procargs2(3, "/bin/zsh", 7, []string{"-zsh", "-l", "-c"}, []string{"PATH=/usr/bin", "HOME=/Users/x"}),
			format: argvProcArgs2,
			want:   []string{"-zsh", "-l", "-c"},
		},
		{
			name:   "procargs2: no padding at all, which a parser skipping a FIXED run would misread",
			raw:    procargs2(1, "/bin/sh", 0, []string{"sh"}, nil),
			format: argvProcArgs2,
			want:   []string{"sh"},
		},
		{
			name:   "procargs2: the environment is never read, however much of it follows — argc is what stops the read, and the environment carries secrets",
			raw:    procargs2(1, "/bin/sh", 3, []string{"sh"}, []string{"A=1", "SECRET=hunter2", "C=3"}),
			format: argvProcArgs2,
			want:   []string{"sh"},
		},
		{
			name:   "procargs2: an empty argument is an argument and survives rather than collapsing",
			raw:    procargs2(2, "/bin/sh", 1, []string{"sh", ""}, []string{"A=1"}),
			format: argvProcArgs2,
			want:   []string{"sh", ""},
		},
		{
			name:   "procargs2: argc says more than the block holds — report what is there, because partial evidence is evidence",
			raw:    procargs2(9, "/bin/sh", 1, []string{"sh", "-l"}, nil),
			format: argvProcArgs2,
			want:   []string{"sh", "-l"},
		},
		{
			name:   "procargs2: argc of zero — a process with no argument vector says nothing",
			raw:    procargs2(0, "/bin/sh", 1, nil, nil),
			format: argvProcArgs2,
			want:   nil,
		},
		{
			name:   "procargs2: a block too short to hold argc at all",
			raw:    []byte{1, 2},
			format: argvProcArgs2,
			want:   nil,
		},
		{
			name:   "procargs2: a block holding argc and nothing else",
			raw:    binary.LittleEndian.AppendUint32(nil, 2),
			format: argvProcArgs2,
			want:   nil,
		},
		{
			name:   "procargs2: an all-ones argc, which is what a truncated read looks like",
			raw:    append(binary.LittleEndian.AppendUint32(nil, 0xFFFFFFFF), "/bin/sh\x00sh\x00"...),
			format: argvProcArgs2,
			want:   nil,
		},
		{
			name:   "cmdline: the trailing NUL is a SEPARATOR, not an empty final argument",
			raw:    []byte("/bin/zsh\x00-l\x00"),
			format: argvPlain,
			want:   []string{"/bin/zsh", "-l"},
		},
		{
			name:   "cmdline: an empty argument the user actually passed is kept",
			raw:    []byte("sh\x00\x00"),
			format: argvPlain,
			want:   []string{"sh", ""},
		},
		{
			name:   "cmdline: a kernel thread has no command line at all",
			raw:    nil,
			format: argvPlain,
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseArgv(tc.raw, tc.format)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseArgv = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestADiagnosticThePlatformCannotAnswerIsNamedRatherThanLeftBlank is the
// bead. macOS exposes no sysctl for another process's working directory —
// proc_pidinfo(PROC_PIDVNODEPATHINFO) is the only route and it needs cgo — so
// the honest answer is "we do not know", and it has to be ON THE WIRE. Left
// blank, a reader falls back to launch.cwd and shows the directory the shell
// STARTED in as though it were the directory it is in now.
func TestADiagnosticThePlatformCannotAnswerIsNamedRatherThanLeftBlank(t *testing.T) {
	insp := &osInspector{src: fakeSource{
		name: "sysctl",
		live: map[int]bool{4242: true, 4300: true},
		// no cwd entry: this source cannot answer that at all
		argvBlob: map[int][]byte{4242: procargs2(1, "/bin/zsh", 4, []string{"-zsh"}, nil)},
		format:   argvProcArgs2,
		comms:    map[int]string{4300: "cargo"},
	}}

	obs := insp.Observe(4242, 4300)
	if obs == nil {
		t.Fatal("a live process produced no observation at all: this source answers argv and the foreground command even where it cannot answer cwd")
	}
	if obs.Source != "sysctl" {
		t.Errorf("Source = %q, want sysctl — evidence that cannot say where it came from cannot be weighed", obs.Source)
	}
	if obs.Cwd != "" {
		t.Errorf("Cwd = %q: this platform cannot observe it, and a guess here is the stale value the bead is about", obs.Cwd)
	}
	if !slices.Contains(obs.Unavailable, proto.DiagnosticCwd) {
		t.Errorf("Unavailable = %v, want it to name cwd: silence is what makes a stale launch value look like a current observation", obs.Unavailable)
	}
	// And the diagnostics it CAN answer are answered, or the platform would be
	// reporting nothing at all — which is the state this bead found.
	if !reflect.DeepEqual(obs.Argv, []string{"-zsh"}) {
		t.Errorf("Argv = %q, want the argument vector sysctl does supply", obs.Argv)
	}
	if obs.ForegroundPgid != 4300 || obs.ForegroundCommand != "cargo" {
		t.Errorf("foreground = %d/%q, want 4300/cargo — 'what is running in here right now' is the diagnostic a person actually wants", obs.ForegroundPgid, obs.ForegroundCommand)
	}
}

// TestADiagnosticThatFailedIsNamedTheSameWayOneThatIsImpossibleIs — the rule
// is per-observation and not per-platform. /proc/<pid>/cwd is refused often
// enough to matter (a process that changed uid, a hardened container), and a
// refusal that arrives as an empty string is indistinguishable from a shell
// sitting in "": the reader falls back to the launch record on Linux for the
// same reason it would on macOS.
func TestADiagnosticThatFailedIsNamedTheSameWayOneThatIsImpossibleIs(t *testing.T) {
	insp := &osInspector{src: fakeSource{
		name: "procfs",
		live: map[int]bool{4242: true, 4300: true},
		// every read fails
	}}
	obs := insp.Observe(4242, 4300)
	if obs == nil {
		t.Fatal("a live process whose diagnostics all failed produced no observation: the process existing IS evidence")
	}
	for _, want := range []proto.Diagnostic{proto.DiagnosticCwd, proto.DiagnosticArgv, proto.DiagnosticForegroundCommand} {
		if !slices.Contains(obs.Unavailable, want) {
			t.Errorf("Unavailable = %v, want it to name %q", obs.Unavailable, want)
		}
	}
}

// TestNothingIsNamedUnavailableWhenEverythingWasAnswered is the pairing
// AGENTS.md asks for by name: for every "reports the failure" there is a case
// where an ordinary machine succeeds, and then the list is empty — and EMPTY,
// not nil, because a nil slice marshals to null and null is not an answer.
func TestNothingIsNamedUnavailableWhenEverythingWasAnswered(t *testing.T) {
	insp := &osInspector{src: fakeSource{
		name:     "procfs",
		live:     map[int]bool{4242: true, 4300: true},
		cwds:     map[int]string{4242: "/home/dev/somewhere/else"},
		argvBlob: map[int][]byte{4242: []byte("/bin/zsh\x00-l\x00")},
		format:   argvPlain,
		comms:    map[int]string{4300: "cargo"},
	}}
	obs := insp.Observe(4242, 4300)
	if obs == nil {
		t.Fatal("no observation")
	}
	if len(obs.Unavailable) != 0 {
		t.Errorf("Unavailable = %v, want empty: everything asked for was answered", obs.Unavailable)
	}
	if obs.Unavailable == nil {
		t.Error("Unavailable is nil and marshals to null, which is not the same answer as []")
	}
	if obs.Cwd != "/home/dev/somewhere/else" {
		t.Errorf("Cwd = %q: the whole point is that this is where the shell IS, not where it started", obs.Cwd)
	}
}

// TestTheShellInTheForegroundIsNotAMissingDiagnostic separates the two
// silences that look alike. "Nothing is running but the shell" is an ANSWER
// and is said by omission; "we could not find out what is running" is not, and
// is said by naming it.
func TestTheShellInTheForegroundIsNotAMissingDiagnostic(t *testing.T) {
	insp := &osInspector{src: fakeSource{
		name:     "sysctl",
		live:     map[int]bool{4242: true},
		argvBlob: map[int][]byte{4242: procargs2(1, "/bin/zsh", 4, []string{"-zsh"}, nil)},
		format:   argvProcArgs2,
	}}
	for _, fg := range []int{0, 4242} {
		obs := insp.Observe(4242, fg)
		if obs == nil {
			t.Fatalf("foregroundPgid %d produced no observation", fg)
		}
		if obs.ForegroundPgid != 0 || obs.ForegroundCommand != "" {
			t.Errorf("foregroundPgid %d reported %d/%q, want nothing: the shell itself is not a job", fg, obs.ForegroundPgid, obs.ForegroundCommand)
		}
		if slices.Contains(obs.Unavailable, proto.DiagnosticForegroundCommand) {
			t.Errorf("foregroundPgid %d named foregroundCommand unavailable: there was nothing to ask about", fg)
		}
	}
}

// TestAProcessTheKernelDoesNotHaveProducesNoObservation: nil, never an empty
// record, which would decode as "we looked and this process has no working
// directory". The distinction is the whole reason SessionEntry.Observed is a
// pointer.
func TestAProcessTheKernelDoesNotHaveProducesNoObservation(t *testing.T) {
	insp := &osInspector{src: fakeSource{name: "procfs", live: map[int]bool{}}}
	if obs := insp.Observe(4242, 0); obs != nil {
		t.Fatalf("a dead process produced %+v, want no observation at all", obs)
	}
}

// TestTheHelperHasAnInspectorOnEveryPlatformItShipsOn is the acceptance in one
// line, and it is deliberately not written as "if GOOS == linux". macOS is the
// platform nocx ships on first; a composition root resolving to nothing there
// is the defect this bead names, and it was invisible to every other test in
// this package because they all install an inspector by hand.
//
// It is also the only test here that runs the real syscalls, so it is what
// makes the darwin source more than a thing that compiles: on the macOS CI
// runner this observes a real process through sysctl, and fails if
// kern.procargs2 or kern.proc.pid does not answer.
func TestTheHelperHasAnInspectorOnEveryPlatformItShipsOn(t *testing.T) {
	insp := NewInspector()
	if insp == nil {
		t.Fatal("this platform's composition root resolves to no inspector: the inventory would fall back to launch metadata alone and say nothing about it")
	}
	// About a process that certainly exists — ours. A suite that only checked
	// the failure paths is how contentkey shipped a key that was never
	// obtainable on an ordinary machine.
	obs := insp.Observe(os.Getpid(), 0)
	if obs == nil {
		t.Fatal("the inspector says nothing about the running test process, which is as ordinary a machine as there is")
	}
	if obs.Source == "" {
		t.Error("the evidence does not say where it came from")
	}
	if obs.Unavailable == nil {
		t.Error("Unavailable is nil and marshals to null")
	}
	// argv is answerable on both platforms nocx ships on, and it is the one
	// diagnostic that goes through the parse above, so this is the check that
	// the real blob and the golden vectors are the same format.
	if len(obs.Argv) == 0 {
		t.Errorf("no argv for the running test binary: %+v", obs)
	}
	if slices.Contains(obs.Unavailable, proto.DiagnosticArgv) {
		t.Errorf("argv is named unavailable on an ordinary machine: %+v", obs)
	}
}

// fakeSource answers from tables. It is the seam that makes everything above
// runnable off darwin; the real ones are procfsSource and darwinSource, each
// compiled only on its own platform.
type fakeSource struct {
	name     string
	live     map[int]bool
	cwds     map[int]string
	argvBlob map[int][]byte
	format   argvFormat
	comms    map[int]string
}

var errFakeNoAnswer = errors.New("this fake has no answer for that pid")

func (f fakeSource) source() string     { return f.name }
func (f fakeSource) alive(pid int) bool { return f.live[pid] }

func (f fakeSource) cwd(pid int) (string, error) {
	if c, ok := f.cwds[pid]; ok {
		return c, nil
	}
	return "", errFakeNoAnswer
}

func (f fakeSource) argv(pid int) ([]byte, error) {
	if raw, ok := f.argvBlob[pid]; ok {
		return raw, nil
	}
	return nil, errFakeNoAnswer
}

func (f fakeSource) argvFormat() argvFormat { return f.format }

func (f fakeSource) comm(pid int) (string, error) {
	if c, ok := f.comms[pid]; ok {
		return c, nil
	}
	return "", errFakeNoAnswer
}
