package filesystem_test

// The edit path's two remaining ends (stage 10, nocx-a62cl.11.1): a file too
// large to hold whole is refused rather than cut, and a commit whose reply was
// lost is settled by re-reading the bytes the caller meant to install.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/hashline"
)

// TestReadSnapshotAndEditRefuseAFileOverTheReadBound is the typed too-large
// refusal, both tools, with the success below it.
func TestReadSnapshotAndEditRefuseAFileOverTheReadBound(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	body := make([]byte, filesystem.MaxReadBytes+1)
	for i := range body {
		body[i] = 'a'
	}
	if err := os.WriteFile(big, body, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	source := newLocalSource(t)
	reader := filesystem.NewScopedReaderWithExactFiles(source, []string{dir}, []string{big})
	_, readErr := reader.ReadSnapshot(context.Background(), big, 64<<10)
	var limit *filesystem.ErrByteLimit
	if !errors.As(readErr, &limit) {
		t.Fatalf("files.read of an oversized file = %v, want *ErrByteLimit", readErr)
	}

	editor := filesystem.NewScopedEditorWithExactFiles(source, []string{dir}, []string{big})
	if _, editErr := editor.Edit(context.Background(), big, "h1-00-00-0-0", "PUT 1.=1:\n+short"); !errors.As(editErr, &limit) {
		t.Fatalf("files.edit of an oversized file = %v, want *ErrByteLimit", editErr)
	}

	// The paired success: the same tools on a file under the bound.
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("small\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	smallReader := filesystem.NewScopedReaderWithExactFiles(source, []string{dir}, []string{small})
	snapshot, err := smallReader.ReadSnapshot(context.Background(), small, 64<<10)
	if err != nil {
		t.Fatalf("files.read of a small file: %v", err)
	}
	smallEditor := filesystem.NewScopedEditorWithExactFiles(source, []string{dir}, []string{small})
	if _, err := smallEditor.Edit(context.Background(), small, snapshot.Revision, "PUT 1.=1:\n+edited"); err != nil {
		t.Fatalf("files.edit of a small file: %v", err)
	}
}

// unknownOutcomeProvider is the local provider whose conditional write answers
// a LOST REPLY: the bytes are installed (installs=true) or not, and the caller
// is told the outcome is unknown either way. It is how the helper's recovery
// branch is exercised without a host.
type unknownOutcomeProvider struct {
	filesystem.Provider
	versioned filesystem.VersionedFiles
}

func (p *unknownOutcomeProvider) Versioned() filesystem.VersionedFiles { return p.versioned }

type lostReply struct {
	filesystem.VersionedFiles
	installs bool
}

func (l lostReply) Commit(ctx context.Context, path string, data []byte, expect filesystem.Expect) (filesystem.Version, error) {
	if l.installs {
		if _, err := l.VersionedFiles.Commit(ctx, path, data, expect); err != nil {
			return filesystem.Version{}, err
		}
	}
	return filesystem.Version{}, &filesystem.ErrUnknownWriteOutcome{Path: path}
}

func unknownOutcomeSource(t *testing.T, installs bool) *localSource {
	t.Helper()
	inner := local.New()
	provider := &unknownOutcomeProvider{
		Provider:  inner,
		versioned: lostReply{VersionedFiles: inner.Versioned(), installs: installs},
	}
	return newSourceWithProvider(t, provider, filesystem.Capabilities{Versioned: provider.Versioned()})
}

// TestEditRecoversALostCommitReplyThatLanded: the response was lost and the
// bytes are on disk, so the call is a success — decided by re-reading the file
// and comparing the digest of exactly the bytes the caller meant to install.
func TestEditRecoversALostCommitReplyThatLanded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	source := unknownOutcomeSource(t, true)
	snapshot, err := filesystem.NewScopedReaderWithExactFiles(source, []string{dir}, []string{path}).ReadSnapshot(context.Background(), path, 64<<10)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	editor := filesystem.NewScopedEditorWithExactFiles(source, []string{dir}, []string{path})
	result, err := editor.Edit(context.Background(), path, snapshot.Revision, "PUT 1.=1:\n+after")
	if err != nil {
		t.Fatalf("an installed write whose reply was lost must be reported applied: %v", err)
	}
	if result.Revision == "" {
		t.Fatal("the recovered success carries no revision")
	}
	body, err := os.ReadFile(path) //nolint:gosec // a path this test just created
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "after\n" {
		t.Fatalf("body = %q, want the installed bytes", body)
	}
}

// And the other outcome of the same branch: the reply was lost and nothing was
// installed. The re-read does not match, so the answer stays "unknown, read it
// again" rather than claiming a write that did not happen.
func TestEditReportsALostCommitReplyThatDidNotLand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	source := unknownOutcomeSource(t, false)
	snapshot, err := filesystem.NewScopedReaderWithExactFiles(source, []string{dir}, []string{path}).ReadSnapshot(context.Background(), path, 64<<10)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	editor := filesystem.NewScopedEditorWithExactFiles(source, []string{dir}, []string{path})
	_, err = editor.Edit(context.Background(), path, snapshot.Revision, "PUT 1.=1:\n+after")
	var unknown *filesystem.ErrUnknownWriteOutcome
	if !errors.As(err, &unknown) {
		t.Fatalf("edit error = %v, want *ErrUnknownWriteOutcome", err)
	}
	body, readErr := os.ReadFile(path) //nolint:gosec // a path this test just created
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(body) != "before\n" {
		t.Fatalf("body = %q, want the original bytes", body)
	}
}

// TestCreateRefusesContentThatIsNotTextAndAnOccupiedName keeps the two refusals
// files.create always had, now that the bytes reach the provider instead of
// hashline's own open: content that is not text is refused before anything is
// written, and a name that is taken is refused by the conditional create.
func TestCreateRefusesContentThatIsNotTextAndAnOccupiedName(t *testing.T) {
	dir := t.TempDir()
	source := newLocalSource(t)
	editor := filesystem.NewScopedEditorWithExactParents(source, []string{dir}, []string{dir})
	binary := filepath.Join(dir, "binary.dat")
	if _, err := editor.Create(context.Background(), binary, "one\x00two"); !errors.Is(err, hashline.ErrBinaryFile) {
		t.Fatalf("create of binary content = %v, want ErrBinaryFile", err)
	}
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Fatalf("create of binary content wrote the file (stat err = %v)", err)
	}

	taken := filepath.Join(dir, "taken.txt")
	if err := os.WriteFile(taken, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var exists *filesystem.ErrExists
	if _, err := editor.Create(context.Background(), taken, "second\n"); !errors.As(err, &exists) {
		t.Fatalf("create on an occupied name = %v, want *ErrExists", err)
	}
	body, err := os.ReadFile(taken) //nolint:gosec // a path this test just created
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "first\n" {
		t.Fatalf("body = %q, want the original bytes", body)
	}
}
