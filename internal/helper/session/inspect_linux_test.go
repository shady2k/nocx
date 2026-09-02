//go:build linux

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// The /proc reads themselves, against a procfs laid out in a temporary
// directory. The `root` field exists for exactly this: the reads are checkable
// without the OS agreeing to produce a process in the state the test wants,
// and the failure paths below — a cwd link that cannot be resolved, a cmdline
// that is not there — are otherwise unreachable on a machine we control.

// testBoot is the boot instant procEntry writes into the fake /proc/stat, so
// a start time can be asserted as an exact value rather than a plausible one.
var testBoot = time.Unix(1_756_000_000, 0)

func procEntry(t *testing.T, pid int) (*procfsSource, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "77")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The machine-wide /proc/stat, which is where the boot instant comes from
	// and is a DIFFERENT file from the per-process one below it. The two
	// sharing a name is exactly why this is written out rather than assumed.
	stat := fmt.Sprintf("cpu 1 2 3\nbtime %d\nprocesses 9\n", testBoot.Unix())
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatalf("write /proc/stat: %v", err)
	}
	return &procfsSource{root: root}, dir
}

// procStatLine builds a /proc/<pid>/stat line the way the kernel lays one out:
// pid, the command in parentheses, the state letter, the parent, then the
// filler that carries starttime at field 22.
func procStatLine(pid int, comm, state string, ppid int, startTicks int64) string {
	fields := []string{state, fmt.Sprint(ppid)}
	// Fields 5..21 are of no interest here and are filled with zeroes; field
	// 22 is starttime.
	for range 17 {
		fields = append(fields, "0")
	}
	fields = append(fields, fmt.Sprint(startTicks))
	line := fmt.Sprintf("%d (%s)", pid, comm)
	for _, f := range fields {
		line += " " + f
	}
	return line + " 0 0 0\n"
}

// TestProcfsReadsWhatAnOrdinaryEntryHolds is the succeeds-on-a-normal-machine
// half of the pair; the failure half is below it.
func TestProcfsReadsWhatAnOrdinaryEntryHolds(t *testing.T) {
	src, dir := procEntry(t, 77)
	where := t.TempDir()
	if err := os.Symlink(where, filepath.Join(dir, "cwd")); err != nil {
		t.Skipf("this filesystem has no symlinks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("/bin/zsh\x00-l\x00"), 0o600); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte("zsh\n"), 0o600); err != nil {
		t.Fatalf("write comm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(procStatLine(77, "zsh", "S", 42, 1500)), 0o600); err != nil {
		t.Fatalf("write stat: %v", err)
	}

	obs := (&osInspector{src: src}).Observe(77, 0)
	if obs == nil {
		t.Fatal("an existing /proc entry produced no observation at all")
	}
	if obs.Source != "procfs" {
		t.Errorf("Source = %q, want procfs", obs.Source)
	}
	if obs.Cwd != where {
		t.Errorf("Cwd = %q, want %q", obs.Cwd, where)
	}
	if len(obs.Argv) != 2 {
		t.Errorf("Argv = %q, want the two arguments cmdline holds", obs.Argv)
	}
	if want := proto.FormatTime(testBoot.Add(15 * time.Second)); obs.StartTime != want {
		t.Errorf("StartTime = %q, want %q: 1500 ticks is 15 seconds after boot, and boot is what /proc/stat's btime names", obs.StartTime, want)
	}
	if obs.Ppid != 42 {
		t.Errorf("Ppid = %d, want 42", obs.Ppid)
	}
	if obs.State != proto.ProcessSleeping {
		t.Errorf("State = %q, want %q for proc(5)'s S", obs.State, proto.ProcessSleeping)
	}
	if len(obs.Unavailable) != 0 {
		t.Errorf("Unavailable = %v, want empty: everything asked for was answered", obs.Unavailable)
	}
}

// TestProcfsNamesWhatItCouldNotRead: the same entry with nothing readable in
// it. A refused cwd link is not hypothetical — a process that changed uid, a
// hardened container, a pid that exited between the liveness check and the
// read — and reported as a blank it is indistinguishable from a shell that has
// not moved.
func TestProcfsNamesWhatItCouldNotRead(t *testing.T) {
	src, _ := procEntry(t, 77)

	obs := (&osInspector{src: src}).Observe(77, 0)
	if obs == nil {
		t.Fatal("an existing /proc entry produced no observation at all")
	}
	for _, want := range []proto.Diagnostic{
		proto.DiagnosticCwd, proto.DiagnosticArgv,
		proto.DiagnosticStartTime, proto.DiagnosticPpid, proto.DiagnosticState,
	} {
		if !slices.Contains(obs.Unavailable, want) {
			t.Errorf("Unavailable = %v, want it to name %q", obs.Unavailable, want)
		}
	}
}

// TestProcfsSaysNothingAboutAPidItDoesNotHave — a process that exited leaves
// no entry, and the answer is no observation rather than an empty one.
func TestProcfsSaysNothingAboutAPidItDoesNotHave(t *testing.T) {
	src, _ := procEntry(t, 77)
	if obs := (&osInspector{src: src}).Observe(9999, 0); obs != nil {
		t.Fatalf("a pid with no /proc entry produced %+v", obs)
	}
}

// TestTheCommandNameCannotTearTheStatLineApart is the trap proc(5) sets and
// the reason this parse skips to the LAST ')'. Field 2 is the command name in
// parentheses and the kernel does not escape it, so a process that renamed
// itself `evil) X 1` puts a state letter and a parent pid inside the field —
// and a naive whitespace split then reads THOSE, silently, because they parse.
func TestTheCommandNameCannotTearTheStatLineApart(t *testing.T) {
	for _, tc := range []struct {
		name string
		comm string
	}{
		{name: "a comm holding a closing parenthesis and a space", comm: "evil) X 1"},
		{name: "a comm that spells a whole plausible prefix", comm: "sh) R 999 0 0 0 0 0"},
		{name: "a comm holding only spaces", comm: "my shell"},
		{name: "an ordinary comm, so the same parse is checked against the easy case", comm: "zsh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseProcStat([]byte(procStatLine(77, tc.comm, "T", 4242, 900)), testBoot)
			if err != nil {
				t.Fatalf("parseProcStat: %v", err)
			}
			if got.State != proto.ProcessStopped {
				t.Errorf("State = %q, want stopped: the parse read a field from inside the command name", got.State)
			}
			if got.Ppid != 4242 {
				t.Errorf("Ppid = %d, want 4242: the parse read a field from inside the command name", got.Ppid)
			}
			if want := testBoot.Add(9 * time.Second); !got.StartTime.Equal(want) {
				t.Errorf("StartTime = %v, want %v", got.StartTime, want)
			}
		})
	}
}

// TestEveryKernelStateLetterMapsOntoTheClosedVocabularyOrOntoNothing. A letter
// the vocabulary cannot spell must produce the EMPTY state, which Observe
// reports as unavailable — never a value invented to fill the field, which is
// the same lie as leaving it blank one step further from being noticed.
func TestEveryKernelStateLetterMapsOntoTheClosedVocabularyOrOntoNothing(t *testing.T) {
	for letter, want := range map[string]proto.ProcessState{
		"R": proto.ProcessRunning,
		"S": proto.ProcessSleeping,
		"D": proto.ProcessUninterruptible,
		"T": proto.ProcessStopped,
		"t": proto.ProcessStopped,
		"Z": proto.ProcessZombie,
		"X": "", "x": "", "I": "", "W": "", "P": "", "K": "", "?": "",
	} {
		if got := linuxProcessState(letter); got != want {
			t.Errorf("linuxProcessState(%q) = %q, want %q", letter, got, want)
		}
	}
}

// TestAStatLineThatIsNotTheLayoutProcDescribesIsRefused rather than half-read.
// A truncated line still holds a state letter and a parent, and reporting
// those beside a start time taken from whatever field happened to be last is
// evidence assembled out of two different reads.
func TestAStatLineThatIsNotTheLayoutProcDescribesIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{name: "no parenthesised command name at all", line: "77 zsh S 42 0 0"},
		{name: "the line stops before field 22", line: "77 (zsh) S 42 0 0 0"},
		{name: "an empty file", line: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseProcStat([]byte(tc.line), testBoot); err == nil {
				t.Error("parseProcStat accepted a line that is not the layout proc(5) describes")
			}
		})
	}
}

// TestNoBootInstantMeansNoStartTimeRatherThanOneMeasuredFromTheEpoch. The
// ticks are counted from boot, so without the boot instant they are not a
// time at all — and a start time in 1970 is a VALUE, which is the one thing
// this record must never invent. The other two facts of the triple survive,
// because they never depended on the boot instant.
func TestNoBootInstantMeansNoStartTimeRatherThanOneMeasuredFromTheEpoch(t *testing.T) {
	got, err := parseProcStat([]byte(procStatLine(77, "zsh", "R", 42, 1500)), time.Time{})
	if err != nil {
		t.Fatalf("parseProcStat: %v", err)
	}
	if !got.StartTime.IsZero() {
		t.Errorf("StartTime = %v, want the zero time: ticks since a boot nobody knows are not an instant", got.StartTime)
	}
	if got.Ppid != 42 || got.State != proto.ProcessRunning {
		t.Errorf("the other two facts were lost with the start time: %+v", got)
	}
}

// TestTheBootInstantIsReadOnceAndFromProcStat is both halves of the pair: it
// succeeds on a well-formed /proc/stat, and a source whose /proc/stat carries
// no btime answers the zero time instead of guessing.
func TestTheBootInstantIsReadOnceAndFromProcStat(t *testing.T) {
	src, _ := procEntry(t, 77)
	if got := src.bootTime(); !got.Equal(testBoot) {
		t.Errorf("bootTime = %v, want %v", got, testBoot)
	}
	// Read once: overwriting the file cannot move a machine's boot instant,
	// and a start time that wobbles cannot be compared against a recorded one.
	if err := os.WriteFile(filepath.Join(src.root, "stat"), []byte("btime 1\n"), 0o600); err != nil {
		t.Fatalf("rewrite /proc/stat: %v", err)
	}
	if got := src.bootTime(); !got.Equal(testBoot) {
		t.Errorf("bootTime = %v after a rewrite, want the first answer %v", got, testBoot)
	}

	blind := &procfsSource{root: t.TempDir()}
	if got := blind.bootTime(); !got.IsZero() {
		t.Errorf("bootTime = %v with no /proc/stat at all, want the zero time", got)
	}
}

// TestTheRealProcfsAnswersTheTripleForTheRunningTestProcess — the ordinary
// machine. Everything above drives a procfs this test laid out itself; this
// one reads the real one, which is the only check that the layout assumed
// here is the layout the kernel actually writes.
func TestTheRealProcfsAnswersTheTripleForTheRunningTestProcess(t *testing.T) {
	obs := NewInspector().Observe(os.Getpid(), 0)
	if obs == nil {
		t.Fatal("no observation for the running test process")
	}
	for _, no := range []proto.Diagnostic{
		proto.DiagnosticStartTime, proto.DiagnosticPpid, proto.DiagnosticState,
	} {
		if slices.Contains(obs.Unavailable, no) {
			t.Errorf("%q is unavailable on an ordinary machine: %+v", no, obs)
		}
	}
	if obs.Ppid != os.Getppid() {
		t.Errorf("Ppid = %d, want %d — the kernel and os.Getppid disagree, so the field is being read from the wrong place", obs.Ppid, os.Getppid())
	}
	started, err := time.Parse(time.RFC3339Nano, obs.StartTime)
	if err != nil {
		t.Fatalf("StartTime %q does not parse as the spelling every other time on this wire uses: %v", obs.StartTime, err)
	}
	// A start time is an instant in the past and within this machine's uptime.
	// Bounded on BOTH ends deliberately: an unbounded check passes for a value
	// taken from the epoch, which is exactly the failure it exists for.
	if !started.Before(time.Now().Add(time.Second)) || started.Before(time.Now().Add(-365*24*time.Hour)) {
		t.Errorf("StartTime = %v, which is not an instant this process could have started at", started)
	}
}
