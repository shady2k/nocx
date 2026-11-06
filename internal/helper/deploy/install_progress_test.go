package deploy_test

// What the install tells a person who is watching it, and what a person's
// Cancel leaves behind (nocx-3an1s.2.10).
//
// Both are asserted through the seams the production callers use: the
// Progress callback is what internal/app turns into an Operations row, and
// the context is the one a withdrawal ends. Nothing here waits on a clock —
// the cancellation is driven from inside the progress callback, at a byte
// count the test names.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

// bigSource serves an artifact whose DECOMPRESSED payload is large enough to
// cross several of the install's write chunks, which is what makes "cancelled
// midway" a state the test can create rather than a hope.
type bigSource struct {
	compressed []byte
	hash       string
	size       int
}

func newBigSource(t *testing.T, size int) *bigSource {
	t.Helper()
	payload := bytes.Repeat([]byte("helper install progress payload\n"), size/32+1)[:size]
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	sum := sha256.Sum256(payload)
	return &bigSource{compressed: buf.Bytes(), hash: hex.EncodeToString(sum[:]), size: size}
}

func (s *bigSource) Artifact(deploy.Platform) ([]byte, string, error) {
	return s.compressed, s.hash, nil
}

// TestAnInstallReportsWhatItIsDoingAndHowFar asserts the two facts a row is
// made of: a byte count that moves while the payload goes out, and a CHECKING
// stage that counts nothing (one ReadFile has no byte count between its ends).
// The row itself must start at the first byte, not after a threshold — the
// first event is what makes it appear at all.
func TestAnInstallReportsWhatItIsDoingAndHowFar(t *testing.T) {
	src := newBigSource(t, 1<<20)
	fs := newFakeFS()

	type event struct {
		stage          deploy.Stage
		written, total int64
	}
	var events []event
	progress := func(stage deploy.Stage, written, total int64) {
		events = append(events, event{stage: stage, written: written, total: total})
	}

	if _, _, err := deploy.Ensure(context.Background(), fs, src, "/home/u", deploy.Platform{"linux", "amd64"}, progress); err != nil {
		t.Fatalf("install: %v", err)
	}

	if len(events) < 2 {
		t.Fatalf("the install reported %d events, want at least a first and a last", len(events))
	}
	first := events[0]
	if first.stage != deploy.StageCopying || first.written != 0 || first.total != int64(src.size) {
		t.Fatalf("the first event is %+v, want copying 0 of %d \u2014 a row that started anywhere else would appear after work it cannot account for", first, src.size)
	}
	// The count MOVES, in order, and never past the size.
	last := int64(0)
	copying := 0
	for _, ev := range events {
		if ev.stage != deploy.StageCopying {
			continue
		}
		copying++
		if ev.written < last || ev.written > int64(src.size) {
			t.Fatalf("copying progress went %d -> %d of %d", last, ev.written, src.size)
		}
		last = ev.written
	}
	if copying < 2 {
		t.Fatalf("the install reported %d copying events; a count that never moves cannot tell a person a slow link is working", copying)
	}
	if last != int64(src.size) {
		t.Fatalf("the last copying event is %d of %d, want the whole payload", last, src.size)
	}
	checking := events[len(events)-1]
	if checking.stage != deploy.StageChecking || checking.total != 0 {
		t.Fatalf("the last event is %+v, want checking with no total (the verification read is one ReadFile)", checking)
	}
}

// TestAnInstallThatIsNotNeededReportsNothing is the "no row" half: a repeat
// open of a host whose helper is already complete must never start a row, and
// the way it does not is that it reports nothing at all.
func TestAnInstallThatIsNotNeededReportsNothing(t *testing.T) {
	fs := newFakeFS()
	if _, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"}, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}
	reported := 0
	if _, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"}, func(deploy.Stage, int64, int64) { reported++ }); err != nil {
		t.Fatalf("repeat ensure: %v", err)
	}
	if reported != 0 {
		t.Fatalf("a complete install reported %d progress events, want none", reported)
	}
}

// TestACancelledInstallStopsMidWriteAndLeavesNothingTrusted is the person's
// Cancel, at the install's own seam. Two claims, and the second is the one
// that matters later: the write STOPS (the install returns the cancellation
// rather than finishing the upload), and what is left is a markerless
// directory — the state install.go's completeness check answers false for, so
// the NEXT attempt removes it and installs again instead of trusting it.
func TestACancelledInstallStopsMidWriteAndLeavesNothingTrusted(t *testing.T) {
	src := newBigSource(t, 2<<20)
	fs := newFakeFS()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reported := int64(-1)
	progress := func(stage deploy.Stage, written, total int64) {
		if stage != deploy.StageCopying || written == 0 {
			return
		}
		// The first chunk is on the wire: the person presses Cancel.
		if reported < 0 {
			reported = written
			cancel()
		}
	}

	_, _, err := deploy.Ensure(ctx, fs, src, "/home/u", deploy.Platform{"linux", "amd64"}, progress)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled install answered %v, want context.Canceled", err)
	}
	if reported <= 0 || reported >= int64(src.size) {
		t.Fatalf("the cancellation was delivered at %d of %d bytes; the write did not stop midway", reported, src.size)
	}
	if fs.hasAnyMarker() {
		t.Fatal("a cancelled install left a completion marker: the next open would trust a half-installed helper")
	}
	// And the next attempt RECOVERS rather than refusing: the markerless tree
	// is removed, and the install it produces is complete.
	if _, _, err := deploy.Ensure(context.Background(), fs, src, "/home/u", deploy.Platform{"linux", "amd64"}, nil); err != nil {
		t.Fatalf("the attempt after a cancelled install failed: %v", err)
	}
	if !fs.hasAnyMarker() {
		t.Fatal("the attempt after a cancelled install left no complete install")
	}
	if got := fs.readFileCount(); got == 0 {
		t.Fatal("the recovery did not verify the bytes it wrote")
	}
}

// TestACancelledInstallRemovesItsTemporary is the same cancellation seen from
// the tree: the per-attempt temporary must not linger next to a markerless
// directory.
func TestACancelledInstallRemovesItsTemporary(t *testing.T) {
	src := newBigSource(t, 2<<20)
	fs := newFakeFS()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := func(stage deploy.Stage, written, total int64) {
		if stage == deploy.StageCopying && written > 0 {
			cancel()
		}
	}
	if _, _, err := deploy.Ensure(ctx, fs, src, "/home/u", deploy.Platform{"linux", "amd64"}, progress); err == nil {
		t.Fatal("want the cancelled install to fail")
	}
	for path := range fs.files {
		if strings.HasPrefix(filepath.Base(path), ".install-") && filepath.Base(path) != ".install-complete" {
			t.Fatalf("the cancelled install left its temporary behind: %s", path)
		}
	}
}
