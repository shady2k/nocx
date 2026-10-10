package local

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

func prepare(t *testing.T, path string) *DownloadDestination {
	t.Helper()
	d, err := PrepareDownload(path)
	if err != nil {
		t.Fatalf("PrepareDownload(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func readDownloadFixture(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(filepath.Base(path))
}

func TestPrepareDownloadPinsDirectoryAgainstRenameAndSymlinkReplacement(t *testing.T) {
	parent := t.TempDir()
	selected := filepath.Join(parent, "selected")
	moved := filepath.Join(parent, "moved")
	victim := filepath.Join(parent, "victim")
	for _, dir := range []string{selected, victim} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(victim, "result.bin"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := prepare(t, filepath.Join(selected, "result.bin"))
	if err := os.Rename(selected, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, selected); err != nil {
		t.Fatal(err)
	}
	out, err := d.Put(context.Background(), 8, strings.NewReader("download"))
	if err != nil || out.State != transfer.StateWritten {
		t.Fatalf("Put = (%+v, %v), want written", out, err)
	}
	got, err := readDownloadFixture(t, filepath.Join(moved, "result.bin"))
	if err != nil || string(got) != "download" {
		t.Fatalf("pinned destination = %q, %v; want download", got, err)
	}
	got, err = readDownloadFixture(t, filepath.Join(victim, "result.bin"))
	if err != nil || string(got) != "safe" {
		t.Fatalf("symlink target = %q, %v; want untouched safe content", got, err)
	}
}

func TestPrepareDownloadCancellationCleansPinnedDirectory(t *testing.T) {
	parent := t.TempDir()
	selected := filepath.Join(parent, "selected")
	moved := filepath.Join(parent, "moved")
	victim := filepath.Join(parent, "victim")
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "result.bin"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := prepare(t, filepath.Join(selected, "result.bin"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &gatedReader{entered: make(chan struct{}), release: make(chan struct{})}
	defer reader.unblock()
	type result struct {
		out transfer.Outcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := d.Put(ctx, 7, reader)
		done <- result{out: out, err: err}
	}()
	<-reader.entered
	entries, err := os.ReadDir(selected)
	if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Name(), ".nocx-upload-") {
		t.Fatalf("selected directory while source blocked = %v, %v; want sink temp", entries, err)
	}
	if renameErr := os.Rename(selected, moved); renameErr != nil {
		t.Fatal(renameErr)
	}
	if symlinkErr := os.Symlink(victim, selected); symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	cancel()
	reader.unblock()
	got := <-done
	if !errors.Is(got.err, context.Canceled) || len(got.out.Stranded) != 0 {
		t.Fatalf("cancelled Put = (%+v, %v), want cancellation without stranded files", got.out, got.err)
	}
	entries, err = os.ReadDir(moved)
	if err != nil || len(entries) != 0 {
		t.Fatalf("pinned directory after cleanup = %v, %v; want empty", entries, err)
	}
	body, err := readDownloadFixture(t, filepath.Join(victim, "result.bin"))
	if err != nil || string(body) != "safe" {
		t.Fatalf("symlink target after cancellation = %q, %v; want untouched safe content", body, err)
	}
}

type gatedReader struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	read    bool
}

func (r *gatedReader) unblock() { r.once.Do(func() { close(r.release) }) }

func (r *gatedReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	close(r.entered)
	<-r.release
	return copy(p, []byte("private")), nil
}

func TestPrepareDownloadCreatesPrivateTempAndReplacement(t *testing.T) {
	oldMask := syscall.Umask(0o022)
	defer syscall.Umask(oldMask)

	dir := t.TempDir()
	destination := filepath.Join(dir, "result.bin")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := prepare(t, destination)
	reader := &gatedReader{entered: make(chan struct{}), release: make(chan struct{})}
	result := make(chan error, 1)
	defer reader.unblock()
	go func() {
		out, err := d.Put(context.Background(), 7, reader)
		if err == nil && out.State != transfer.StateWritten {
			err = errors.New("Put did not report written")
		}
		result <- err
	}()
	<-reader.entered
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	foundTemp := false
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".nocx-upload-") {
			foundTemp = true
			info, statErr := entry.Info()
			if statErr != nil {
				t.Errorf("stat temp: %v", statErr)
			} else if info.Mode().Perm() != 0o600 {
				t.Errorf("temp mode = %v; want 0600", info.Mode().Perm())
			}
		}
	}
	if !foundTemp {
		t.Fatal("sink temp file was not present while source read was blocked")
	}
	reader.unblock()
	if putErr := <-result; putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("stat replacement: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("replacement mode = %v; want 0600", info.Mode().Perm())
	}
	got, err := readDownloadFixture(t, destination)
	if err != nil || string(got) != "private" {
		t.Fatalf("replacement = %q, %v", got, err)
	}
}

func TestPrepareDownloadRejectsInvalidParentBeforePut(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "not-dir")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"relative/file",
		filepath.Join(base, "missing", "file"),
		filepath.Join(file, "child"),
		string(filepath.Separator),
	} {
		if d, err := PrepareDownload(path); err == nil {
			_ = d.Close()
			t.Errorf("PrepareDownload(%q) succeeded for invalid/inaccessible destination", path)
		}
	}
	got, err := readDownloadFixture(t, file)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing file after rejected prepares = %q, %v", got, err)
	}
}

func TestPrepareDownloadSourceErrorPreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.bin")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := prepare(t, path)
	out, err := d.Put(context.Background(), 8, strings.NewReader("short"))
	if err == nil || len(out.Stranded) != 0 {
		t.Fatalf("short-source Put = (%+v, %v), want source failure without stranded temp", out, err)
	}
	got, readErr := readDownloadFixture(t, path)
	if readErr != nil || string(got) != "original" {
		t.Fatalf("existing destination = %q, %v; want original", got, readErr)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != "result.bin" {
		t.Fatalf("directory after source error = %v, %v; want only destination", entries, readErr)
	}
}

func writeFixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

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

func TestOSReadFS_ClassifiesItsRefusals(t *testing.T) {
	dir := t.TempDir()
	t.Run("missing", func(t *testing.T) {
		if _, _, err := (osReadFS{}).Open(filepath.Join(dir, "nope")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Open of a missing path: %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (osReadFS{}).Open(sub); !errors.Is(err, transfer.ErrNotRegular) {
			t.Fatalf("Open of a directory: %v, want transfer.ErrNotRegular", err)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
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

func TestOSReadFS_RefusesAPathItsSyntaxRejects(t *testing.T) {
	for _, p := range []string{"relative/path", "/not/../clean"} {
		if _, _, err := (osReadFS{}).Open(p); err == nil {
			t.Fatalf("Open(%q) succeeded; the provider owns path syntax", p)
		}
	}
}

func TestOSReadFS_TheOpenHandlePinsTheBytes(t *testing.T) {
	dir := t.TempDir()
	p := writeFixture(t, dir, "a.txt", "the original bytes")
	r, size, err := (osReadFS{}).Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
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

func TestProviderSource_StreamsARealFile(t *testing.T) {
	body := strings.Repeat("bytes-", 50_000)
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
