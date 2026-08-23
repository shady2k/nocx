package local

// The read-stream half against the REAL filesystem under t.TempDir(),
// never against a fake, for the reason the sink's local tests give: `os` IS
// the dependency here, so a fake would only restate this package's own
// beliefs about it. What is asserted is the set of contracts
// transfer.RemoteReadFS documents and the compiler cannot check, plus the
// failure paths `os` can actually produce.

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

func writeFixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The paired success, and the assertion the whole seam rests on: an
// ordinary file on an ordinary machine opens, reports its real length, and
// reads back byte for byte. A file of nothing but failure paths cannot
// report that the seam works at all.
func TestOSReadFS_OpensAnOrdinaryFile(t *testing.T) {
	body := strings.Repeat("nocx ", 1000)
	p := writeFixture(t, t.TempDir(), "report.bin", body)

	r, size, err := (osReadFS{}).Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", size, len(body))
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Error("the bytes read are not the file's bytes")
	}
}

// An empty file is a file: it opens, reports zero, and reads back nothing
// without an error. Zero is a size the download route frames a real
// response at, so it must not be an error anywhere on the way.
func TestOSReadFS_AnEmptyFileIsAFile(t *testing.T) {
	p := writeFixture(t, t.TempDir(), "empty", "")

	r, size, err := (osReadFS{}).Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if size != 0 {
		t.Fatalf("size = %d, want 0", size)
	}
	if b, err := io.ReadAll(r); err != nil || len(b) != 0 {
		t.Fatalf("read = (%q, %v), want empty and nil", b, err)
	}
}

// The three classified refusals, each asserted with errors.Is because the
// transport turns exactly this set into a request-shaped -32602 and
// everything else into a server fault. A permission denial reported as a
// server fault tells the person the wrong thing to do about it.
func TestOSReadFS_ClassifiesItsRefusals(t *testing.T) {
	dir := t.TempDir()

	t.Run("a missing file reports fs.ErrNotExist", func(t *testing.T) {
		if _, _, err := (osReadFS{}).Open(filepath.Join(dir, "nope")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Open of a missing path: %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("a directory reports ErrNotRegular", func(t *testing.T) {
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		// A directory is the refusal a person is most likely to provoke,
		// and it must arrive as "that is a folder" and not as an I/O
		// error: os.Open of a directory SUCCEEDS on Linux and fails only
		// at the first Read, so without the kind check this would become a
		// 200 that dies mid-body.
		if _, _, err := (osReadFS{}).Open(sub); !errors.Is(err, transfer.ErrNotRegular) {
			t.Fatalf("Open of a directory: %v, want transfer.ErrNotRegular", err)
		}
	})

	t.Run("an unreadable file reports fs.ErrPermission", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads everything; the mode says nothing")
		}
		p := writeFixture(t, dir, "secret", "x")
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (osReadFS{}).Open(p); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("Open of an unreadable file: %v, want fs.ErrPermission", err)
		}
	})
}

// Open must not BLOCK on a fifo, which is the whole reason the kind is
// asked by NAME before the open happens: os.Open on a fifo with no writer
// waits for one, and a download must never be able to wedge the handler
// that took it. This test hangs rather than failing if the ordering is ever
// reversed, which is the honest shape — the failure being guarded against
// IS a hang — so the package timeout is what catches it.
func TestOSReadFS_DoesNotBlockOnAFifo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("this platform has no mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := (osReadFS{}).Open(p)
		done <- err
	}()
	err := <-done
	if !errors.Is(err, transfer.ErrNotRegular) {
		t.Fatalf("Open of a fifo: %v, want transfer.ErrNotRegular", err)
	}
}

// The provider's path syntax is the provider's, and the seam applies it
// rather than trusting the transport's: the transport validates absolute
// and clean for the shape it can see, and the provider owns the rest.
func TestOSReadFS_RefusesAPathItsSyntaxRejects(t *testing.T) {
	for _, p := range []string{"relative/path", "/not/../clean"} {
		if _, _, err := (osReadFS{}).Open(p); err == nil {
			t.Fatalf("Open(%q) succeeded; the provider owns path syntax", p)
		}
	}
}

// The size is measured on the OPEN handle, and this is what that buys: a
// file replaced after the open still streams the bytes that were open, at
// the length they had. A size taken from the name would describe the new
// file while the reader delivered the old one, and the response would be
// framed at a length its own body could not meet.
func TestOSReadFS_TheOpenHandlePinsTheBytes(t *testing.T) {
	dir := t.TempDir()
	p := writeFixture(t, dir, "a.txt", "the original bytes")

	r, size, err := (osReadFS{}).Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Replaced by rename, which is how an editor saves and how the sink's
	// own promote lands: the name now points at a different inode.
	replacement := writeFixture(t, dir, "b.txt", "completely different and longer")
	if renameErr := os.Rename(replacement, p); renameErr != nil {
		t.Fatal(renameErr)
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "the original bytes" || size != int64(len("the original bytes")) {
		t.Fatalf("read %q at declared size %d; the handle must pin the object the size was measured on", got, size)
	}
}

// And the seam under the engine, end to end on a real file: the provider's
// Source streams a real file through the real chunk loop. This is the local
// half of "a file on a host is read and streamed out".
func TestProviderSource_StreamsARealFile(t *testing.T) {
	body := strings.Repeat("bytes-", 50_000) // ~300 KB, more than one chunk
	p := writeFixture(t, t.TempDir(), "big.bin", body)

	src := New().Source()
	d, err := src.Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = d.Close() }()
	if d.Name != "big.bin" || d.Size != int64(len(body)) {
		t.Fatalf("Open = %+v, want big.bin at %d bytes", d, len(body))
	}

	var out strings.Builder
	sent, err := src.Get(context.Background(), d, &out, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sent != int64(len(body)) || out.String() != body {
		t.Fatalf("delivered %d bytes; want the file's %d, byte for byte", sent, len(body))
	}
}
