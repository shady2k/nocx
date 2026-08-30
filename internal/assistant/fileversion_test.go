package assistant

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countingFileVersionSource struct {
	statFn func(string) (os.FileInfo, error)
	readFn func(string) ([]byte, error)
	stats  int
	reads  int
}

func (s *countingFileVersionSource) Stat(path string) (os.FileInfo, error) {
	s.stats++
	if s.statFn != nil {
		return s.statFn(path)
	}
	return os.Stat(path)
}

func (s *countingFileVersionSource) ReadFile(path string) ([]byte, error) {
	s.reads++
	if s.readFn != nil {
		return s.readFn(path)
	}
	// #nosec G304 -- the test source reads the fixture path it was given.
	return os.ReadFile(path)
}

func writeVersionTestFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestFileVersion_CapturesHashForSmallFile(t *testing.T) {
	path := writeVersionTestFile(t, "small.txt", "hello")
	source := new(countingFileVersionSource)

	version, err := CaptureFileVersionFrom(path, source, DefaultFileVersionPolicy())
	if err != nil {
		t.Fatalf("CaptureFileVersionFrom: %v", err)
	}
	if version.Path != path || version.Strategy != FileVersionHash || version.SHA256 == "" {
		t.Fatalf("version = %+v, want path, hash strategy and digest", version)
	}
	if source.reads != 1 {
		t.Fatalf("reads = %d, want 1", source.reads)
	}
	if source.stats != 2 {
		t.Fatalf("stats = %d, want 2 for a race-checked hash capture", source.stats)
	}
}

func TestFileVersion_UsesStatForLargeNonExecutableFile(t *testing.T) {
	path := writeVersionTestFile(t, "large.bin", strings.Repeat("x", 32))
	source := new(countingFileVersionSource)
	policy := FileVersionPolicy{HashFilesUpTo: 8, HashExecutable: true}

	version, err := CaptureFileVersionFrom(path, source, policy)
	if err != nil {
		t.Fatalf("CaptureFileVersionFrom: %v", err)
	}
	if version.Strategy != FileVersionStat || version.SHA256 != "" {
		t.Fatalf("version = %+v, want stat-only identity", version)
	}
	if source.reads != 0 || source.stats != 1 {
		t.Fatalf("source calls = stat %d/read %d, want 1/0", source.stats, source.reads)
	}
}

func TestFileVersion_HashesExecutableRegardlessOfSize(t *testing.T) {
	path := writeVersionTestFile(t, "run.sh", "x")
	// #nosec G302 -- executable mode is required by this fixture.
	if chmodErr := os.Chmod(path, 0o700); chmodErr != nil {
		t.Fatalf("Chmod: %v", chmodErr)
	}

	version, err := CaptureFileVersion(path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	if version.Strategy != FileVersionHash || version.SHA256 == "" {
		t.Fatalf("version = %+v, want executable hash identity", version)
	}
}

func TestFileVersion_RejectsChangedFileAndNamesIt(t *testing.T) {
	path := writeVersionTestFile(t, "changed.txt", "before")
	version, err := CaptureFileVersion(path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	writeErr := os.WriteFile(path, []byte("after"), 0o600)
	if writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}

	err = VerifyFileVersion(version)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("VerifyFileVersion error = %v, want a refusal naming %q", err, path)
	}
}

func TestFileVersion_RejectsRenameOverOnRealFilesystem(t *testing.T) {
	path := writeVersionTestFile(t, "replaced.txt", "old")
	version, err := CaptureFileVersion(path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement.tmp")
	if err := os.WriteFile(replacement, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile replacement: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if err := VerifyFileVersion(version); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("VerifyFileVersion error = %v, want rename-over refusal naming %q", err, path)
	}
}

func TestFileVersion_CaptureStatFailureIsVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	source := &countingFileVersionSource{statFn: func(string) (os.FileInfo, error) {
		return nil, errors.New("stat denied")
	}}

	_, err := CaptureFileVersionFrom(path, source, DefaultFileVersionPolicy())
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "stat denied") {
		t.Fatalf("capture error = %v, want named stat failure", err)
	}
}

func TestFileVersion_CaptureReadFailureIsVisible(t *testing.T) {
	path := writeVersionTestFile(t, "read-fails.txt", "small")
	source := &countingFileVersionSource{readFn: func(string) ([]byte, error) {
		return nil, errors.New("read denied")
	}}

	_, err := CaptureFileVersionFrom(path, source, DefaultFileVersionPolicy())
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "read denied") {
		t.Fatalf("capture error = %v, want named read failure", err)
	}
}

func TestFileVersion_VerifyStatFailureIsVisible(t *testing.T) {
	path := writeVersionTestFile(t, "verify-stat.txt", "content")
	version, err := CaptureFileVersion(path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	source := &countingFileVersionSource{statFn: func(string) (os.FileInfo, error) {
		return nil, errors.New("stat disappeared")
	}}

	err = VerifyFileVersionFrom(version, source)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "stat disappeared") {
		t.Fatalf("verify error = %v, want named stat failure", err)
	}
}

func TestFileVersion_VerifyHashReadFailureIsVisible(t *testing.T) {
	path := writeVersionTestFile(t, "verify-read.txt", "content")
	version, err := CaptureFileVersion(path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	source := &countingFileVersionSource{readFn: func(string) ([]byte, error) {
		return nil, errors.New("read disappeared")
	}}

	err = VerifyFileVersionFrom(version, source)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "read disappeared") {
		t.Fatalf("verify error = %v, want named read failure", err)
	}
}

func TestFileVersion_CaptureSecondStatFailureIsVisible(t *testing.T) {
	path := writeVersionTestFile(t, "capture-stat-after.txt", "content")
	source := &countingFileVersionSource{}
	source.statFn = func(path string) (os.FileInfo, error) {
		if source.stats == 2 {
			return nil, errors.New("stat after capture disappeared")
		}
		return os.Stat(path)
	}

	_, err := CaptureFileVersionFrom(path, source, DefaultFileVersionPolicy())
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "stat after capture disappeared") {
		t.Fatalf("capture error = %v, want named second-stat failure", err)
	}
}

func TestFileVersion_VerifySecondStatFailureIsVisible(t *testing.T) {
	path := writeVersionTestFile(t, "verify-stat-after.txt", "content")
	version, err := CaptureFileVersion(path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	source := &countingFileVersionSource{}
	source.statFn = func(path string) (os.FileInfo, error) {
		if source.stats == 2 {
			return nil, errors.New("stat after verification disappeared")
		}
		return os.Stat(path)
	}

	err = VerifyFileVersionFrom(version, source)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "stat after verification disappeared") {
		t.Fatalf("verify error = %v, want named second-stat failure", err)
	}
}

func TestFileVersion_HashCaptureRefusesReadReplaceRace(t *testing.T) {
	path := writeVersionTestFile(t, "race.txt", "before")
	source := &countingFileVersionSource{}
	source.readFn = func(path string) ([]byte, error) {
		// #nosec G304 -- the race fixture reads the path it is deliberately mutating.
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return data, os.WriteFile(path, []byte("after"), 0o600)
	}

	_, err := CaptureFileVersionFrom(path, source, DefaultFileVersionPolicy())
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("capture error = %v, want named read/replace race refusal", err)
	}
}

func TestApprovalStore_CarriesAndChecksEveryApprovedFileVersion(t *testing.T) {
	first := writeVersionTestFile(t, "first.txt", "first")
	second := writeVersionTestFile(t, "second.txt", "second")
	firstVersion, err := CaptureFileVersion(first)
	if err != nil {
		t.Fatalf("CaptureFileVersion first: %v", err)
	}
	secondVersion, err := CaptureFileVersion(second)
	if err != nil {
		t.Fatalf("CaptureFileVersion second: %v", err)
	}
	proposal := Approval{
		RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: "args",
		FileVersionState: FileVersionBindingCaptured,
		FileVersions:     []FileVersion{firstVersion, secondVersion},
	}
	store := NewApprovalStore()
	store.Request(proposal)
	if !store.Approve(Approval{RunID: proposal.RunID, Attempt: proposal.Attempt, Tool: proposal.Tool, CallID: proposal.CallID, ArgHash: proposal.ArgHash}) {
		t.Fatal("Approve returned false")
	}
	versions, ok := store.ApprovedFileVersions(proposal)
	if !ok || len(versions) != 2 || versions[0].Path != first || versions[1].Path != second {
		t.Fatalf("ApprovedFileVersions = %v, %v; want both approved paths", versions, ok)
	}
	if err := store.VerifyApprovedFileVersions(proposal); err != nil {
		t.Fatalf("VerifyApprovedFileVersions: %v", err)
	}
	if err := os.WriteFile(second, []byte("changed"), 0o600); err != nil {
		t.Fatalf("WriteFile second: %v", err)
	}
	if err := store.VerifyApprovedFileVersions(proposal); err == nil || !strings.Contains(err.Error(), second) {
		t.Fatalf("verification error = %v, want changed second path", err)
	}
}

func TestApprovalStore_AllowsExplicitNoFileBinding(t *testing.T) {
	proposal := Approval{
		RunID: "run-no-files", Attempt: 1, Tool: "notes.search", CallID: "call-1", ArgHash: "args",
		FileVersionState: FileVersionBindingNotApplicable,
	}
	store := NewApprovalStore()
	store.Request(proposal)
	if !store.Approve(proposal) {
		t.Fatal("Approve returned false")
	}
	if err := store.VerifyApprovedFileVersions(proposal); err != nil {
		t.Fatalf("VerifyApprovedFileVersions: %v, want no-file approval to be allowed", err)
	}
}

func TestApprovalStore_RefusesFileBindingWithoutCapturedVersions(t *testing.T) {
	proposal := Approval{
		RunID: "run-missing-files", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: "args",
		FileVersionState: FileVersionBindingRequired,
	}
	store := NewApprovalStore()
	store.Request(proposal)
	if !store.Approve(proposal) {
		t.Fatal("Approve returned false")
	}
	err := store.VerifyApprovedFileVersions(proposal)
	if err == nil || !strings.Contains(err.Error(), "file version") {
		t.Fatalf("VerifyApprovedFileVersions error = %v, want missing file-version refusal", err)
	}
}
