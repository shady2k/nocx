package main

// THE TWO RULES THE SELECTION RESTS ON, asserted against a real directory so a
// refactor cannot quietly break either (nocx-ouru5.1.13, PR #776 review).
//
//   - A test FILE only the darwin build compiles is ALREADY the derived
//     selection: its tests are macOS-only by their file, and the reachability
//     walk must not list them a second time. On a Mac they load, they run, and
//     they call the darwin implementation directly — which is exactly how the
//     committed file stopped matching what the job re-derived (`make mac-check`
//     failed before a single test ran).
//   - A test FILE only the linux build compiles must never be selected: it does
//     not exist on the Mac, and selecting it would also take it off the Linux
//     side, which is the only side that can run it.
//
// The classification is asked of the go tool for both GOOS values, so it answers
// the same on either platform and needs no darwin build.
import (
	"path/filepath"
	"testing"
)

func TestClassifyNamesTheFilesOnlyOneBuildCompiles(t *testing.T) {
	sets, err := classify("internal/filesystem/local", []string{"gtk3", "nocx_framecheck"})
	if err != nil {
		t.Fatalf("classify internal/filesystem/local: %v", err)
	}
	if !sets.darwinTests["mounts_darwin_test.go"] {
		t.Errorf("mounts_darwin_test.go is not named a darwin-only TEST file; its tests are the derived selection and the walk must not list them again")
	}
	if !sets.linuxTests["mounts_linux_test.go"] {
		t.Errorf("mounts_linux_test.go is not named a linux-only TEST file")
	}
	if sets.darwinTests["mounts_linux_test.go"] || sets.linuxTests["mounts_darwin_test.go"] {
		t.Errorf("a test file is classified for both builds")
	}
	found := false
	for _, name := range sets.darwinImpl {
		if filepath.Base(name) == "mounts_darwin.go" {
			found = true
		}
		if filepath.Base(name) == "mounts_linux.go" {
			t.Errorf("mounts_linux.go is named a darwin-only implementation")
		}
	}
	if !found {
		t.Errorf("mounts_darwin.go is not named a darwin-only implementation")
	}
}

// THE REVIEW'S OWN CASE (PR #776, P1). On a Mac that test loads, runs, and calls
// the darwin `readMountTable` directly, so the walk selects it — and the
// committed file cannot hold it, because the file rule derives it already and
// `scripts/mac-only-tests.mjs` refuses an entry a darwin-only test file declares.
// The selection must therefore drop it BY NAME, which is checkable here: the
// file sets name it, whatever platform the analysis runs on.
func TestADarwinOnlyFileSTestsAreDerivedNotReached(t *testing.T) {
	sets, err := classify("internal/filesystem/local", []string{"gtk3", "nocx_framecheck"})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	const derived = "TestTheRealMountTableNamesADeviceForEveryMount"
	if !sets.darwinTestNames[derived] {
		t.Fatalf("%s is not named as declared by a darwin-only TEST file; on a Mac the walk would select it and the committed file would disagree with the derived list", derived)
	}
	if sets.linuxTestNames[derived] {
		t.Fatalf("%s is named a linux-only test as well", derived)
	}
	// And the walk's own skip is the name set, so a selection containing it is
	// not what this command may produce.
	if !sets.darwinTestNames["TestASymlinkIntoADarwinMountTakesThatMountsDomain"] {
		t.Errorf("the darwin-only test file's other test is not named either")
	}
}

// SUBTEST CALLBACKS ARE PART OF THE TEST (review of PR #776, round 4). A t.Run
// callback is not a call edge the graph has: its callee is testing.T.Run, whose
// body is a dependency's and is not part of this analysis, and the callback
// arm6 reaches the darwin implementation directly. internal/pty's
// TestClose_EndsAnInteractiveShell and TestRunCancellationWakesActualReadAndInput
// are both of that shape — their callbacks call NewLocal and the wake pipe — and
// both were missing from the selection. The walk follows function values passed
// as arguments (and goroutine bodies, which arrive the same way) for that
// reason.
func TestSubtestCallbacksAreFollowed(t *testing.T) {
	for _, want := range []struct{ dir, test string }{
		{"internal/pty", "TestClose_EndsAnInteractiveShell"},
		{"internal/helper/fishchannel", "TestRunCancellationWakesActualReadAndInput"},
	} {
		got, err := deriveDir(want.dir, splitTags(defaultTags))
		if err != nil {
			t.Fatalf("deriveDir %s: %v", want.dir, err)
		}
		selected := map[string]bool{}
		for _, entry := range got {
			selected[entry.test] = true
		}
		if !selected[want.test] {
			t.Errorf("%s %s is not selected: its t.Run callback reaches a darwin implementation (the pty master and wake pipe), and the walk did not follow the callback", want.dir, want.test)
		}
	}
}

// A RE-EXECUTED CHILD IS THE PARENT'S COVERAGE (review of PR #776, round 6). Some
// tests run the test BINARY again, under `-test.run=<another test of this
// package>`, and do their real work in the child: internal/helper/fishchannel's
// TestReceiveFragmentedHeaderAndBody and TestReceiveBoundedFailureAndNoOutput
// spawn their own binary with `-test.run=^TestFishChannelChildProcess$`. The
// parent then reaches darwin code only THROUGH the child, and the child on its
// own measures nothing — so neither was selected, and the darwin path they exist
// for went untested on the Mac.
func TestAReExecutedChildCountsForItsParent(t *testing.T) {
	got, err := deriveDir("internal/helper/fishchannel", splitTags(defaultTags))
	if err != nil {
		t.Fatalf("deriveDir internal/helper/fishchannel: %v", err)
	}
	selected := map[string]bool{}
	for _, entry := range got {
		selected[entry.test] = true
	}
	for _, want := range []string{
		"TestReceiveFragmentedHeaderAndBody",
		"TestReceiveBoundedFailureAndNoOutput",
	} {
		if !selected[want] {
			t.Errorf("%s is not selected: it re-executes this test binary with -test.run=^TestFishChannelChildProcess$, whose body reaches the darwin socket write, and the walk did not follow the child", want)
		}
	}
}
