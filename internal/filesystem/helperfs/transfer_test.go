package helperfs_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/transfer"
)

func ops(calls []rpc) []string {
	result := make([]string, 0, len(calls))
	for _, call := range calls {
		result = append(result, call.op)
	}
	return result
}

func TestHelperFilesystemUploadAndDownload(t *testing.T) {
	const chunk = 256 << 10
	for _, tc := range []struct {
		decision transfer.Decision
		wantOps  []string
	}{
		{transfer.Overwrite, []string{proto.OpWriteOpen, proto.OpWrite, proto.OpWrite, proto.OpWrite, proto.OpWriteCommit}},
		{transfer.KeepBoth, []string{proto.OpWriteOpen, proto.OpWrite, proto.OpWrite, proto.OpWrite, proto.OpWriteCommit, proto.OpWriteCommit}},
		{transfer.Skip, nil},
	} {
		t.Run(string(tc.decision), func(t *testing.T) {
			root, localRoot := tempTree(t), tempTree(t)
			svc := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(root)))}
			reg, _ := singlePeer(t, svc)
			p := helperfs.New(reg, panelID, root)
			lp := local.New(local.WithRoot(localRoot))
			ctx := testContext(t)
			const name = "note.txt"
			data := bytes.Repeat([]byte("n"), 2*chunk+12)
			writeFile(t, filepath.Join(root, name), []byte("original"))
			writeFile(t, filepath.Join(localRoot, name), []byte("original"))
			if tc.decision == transfer.KeepBoth {
				const existing = "note (1).txt"
				writeFile(t, filepath.Join(root, existing), []byte("occupied suffix"))
				writeFile(t, filepath.Join(localRoot, existing), []byte("occupied suffix"))
			}
			upload := transfer.Upload{DestDir: root, Name: name, Size: int64(len(data)), OnExists: tc.decision}
			var progress, localProgress []int64
			out, err := p.Sink().Put(ctx, upload, bytes.NewReader(data), func(n int64) { progress = append(progress, n) })
			upload.DestDir = localRoot
			want, wantErr := lp.Sink().Put(ctx, upload, bytes.NewReader(data), func(n int64) { localProgress = append(localProgress, n) })
			if err != nil || wantErr != nil || out.State != want.State || out.FinalName != want.FinalName || len(out.Stranded) != 0 {
				t.Fatalf("Upload: local = %+v (%v), helper = %+v (%v)", want, wantErr, out, err)
			}
			if !reflect.DeepEqual(progress, localProgress) {
				t.Fatalf("progress = %v, want %v", progress, localProgress)
			}
			calls := ops(svc.observed())
			if !slices.Equal(calls, tc.wantOps) {
				t.Fatalf("Upload sent %v, want %v", calls, tc.wantOps)
			}
			t.Logf("Upload %s: %d bytes, %d RPCs %v", tc.decision, len(data), len(calls), calls)
			if !bytes.Equal(readFile(t, filepath.Join(root, name)), readFile(t, filepath.Join(localRoot, name))) {
				t.Fatal("Upload changed the original destination differently from local")
			}
			if out.State == transfer.StateSkipped {
				return
			}
			// Download through a symlink to the newly written file as a Files
			// caller would, and compare to the local source over the same tree.
			path := filepath.Join(root, "linked")
			if err := os.Symlink(filepath.Join(root, out.FinalName), path); err != nil {
				t.Fatal(err)
			}
			got, wantBytes := download(t, ctx, p.Source(), path), download(t, ctx, local.New().Source(), path)
			if !bytes.Equal(got, wantBytes) || !bytes.Equal(got, data) {
				t.Fatalf("Download got %d bytes, want the %d uploaded bytes", len(got), len(data))
			}
		})
	}
}

func download(t *testing.T, ctx context.Context, source transfer.Source, path string) []byte {
	t.Helper()
	d, err := source.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var b bytes.Buffer
	n, err := source.Get(ctx, d, &b, func(int64) {})
	if err != nil || n != d.Size || int64(b.Len()) != n {
		t.Fatalf("download %s: size=%d, delivered=%d, buffer=%d, error=%v", path, d.Size, n, b.Len(), err)
	}
	return b.Bytes()
}

func TestHelperFilesystemUploadCancellationClearsTempAndJournal(t *testing.T) {
	root, journal := tempTree(t), t.TempDir()
	svc := &fsObserver{Service: sameHostService(t, local.New(local.WithRoot(root)), journal, "11111111111111111111111111111111")}
	reg, _ := singlePeer(t, svc)
	p := helperfs.New(reg, panelID, root)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	data := bytes.Repeat([]byte("x"), transfer.DefaultChunk+1)
	u := transfer.Upload{DestDir: root, Name: "cancelled", Size: int64(len(data)), OnExists: transfer.Overwrite}
	var progressing int64
	_, err := p.Sink().Put(ctx, u, bytes.NewReader(data), func(n int64) { progressing = n; cancel() })
	if !errors.Is(err, context.Canceled) || progressing != transfer.DefaultChunk {
		t.Fatalf("upload cancellation: progress %d, err %v; want one chunk and context.Canceled", progressing, err)
	}
	if got, want := ops(svc.observed()), []string{proto.OpWriteOpen, proto.OpWrite, proto.OpWriteAbort}; !slices.Equal(got, want) {
		t.Fatalf("cancelled upload sent %v, want %v", got, want)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("cancelled upload left files: %v (%v)", files, err)
	}
	if err := filepath.WalkDir(journal, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() != "lock" {
			t.Errorf("cancelled upload left a journal record: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHelperFilesystemDownloadPinsSizeAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replace  bool
		mutateAt int64
	}{
		{name: "replacement after open", replace: true},
		{name: "replacement between ranges", replace: true, mutateAt: transfer.DefaultChunk},
		{name: "append between ranges", mutateAt: transfer.DefaultChunk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tempTree(t)
			path := filepath.Join(root, "note")
			data := bytes.Repeat([]byte("x"), transfer.DefaultChunk+13)
			writeFile(t, path, data)
			svc := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(root)))}
			reg, _ := singlePeer(t, svc)
			source := helperfs.New(reg, panelID, root).Source()
			dl, openErr := source.Open(path)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer func() { _ = dl.Close() }()
			if dl.Size != int64(len(data)) {
				t.Fatalf("Open size = %d, want %d", dl.Size, len(data))
			}
			if calls := ops(svc.observed()); !slices.Equal(calls, []string{proto.OpStat}) {
				t.Fatalf("Open sent %v, want only the stat that pins both size and identity", calls)
			}
			mutate := func() error {
				if tc.replace {
					replacement := filepath.Join(root, "replacement")
					if err := os.WriteFile(replacement, bytes.Repeat([]byte("y"), len(data)), 0o600); err != nil {
						return err
					}
					return os.Rename(replacement, path)
				}
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0) // #nosec G304 -- test-owned file.
				if err != nil {
					return err
				}
				_, err = f.WriteString("not included in the original size")
				return errors.Join(err, f.Close())
			}
			if tc.mutateAt == 0 {
				if err := mutate(); err != nil {
					t.Fatal(err)
				}
			}
			var got bytes.Buffer
			var mutateErr error
			n, err := source.Get(testContext(t), dl, &got, func(n int64) {
				if n == tc.mutateAt {
					mutateErr = mutate()
				}
			})
			if mutateErr != nil {
				t.Fatal(mutateErr)
			}
			if tc.replace {
				if !errors.As(err, new(*filesystem.ErrChangedDuringRead)) || n != tc.mutateAt || !bytes.Equal(got.Bytes(), data[:tc.mutateAt]) {
					t.Fatalf("replacement: %d bytes (%v), want %d original bytes and ErrChangedDuringRead", n, err, tc.mutateAt)
				}
			} else if err != nil || n != int64(len(data)) || !bytes.Equal(got.Bytes(), data) {
				t.Fatalf("append: %d bytes (%v), want exactly the original %d bytes", n, err, len(data))
			}
		})
	}
}
