package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/transfer"
)

// DownloadDestination owns an open directory capability for one selected
// output path. The root pins the selected parent even if its pathname is
// subsequently renamed or replaced.
type DownloadDestination struct {
	mu   sync.Mutex
	root *os.Root
	name string
}

// PrepareDownload validates and pins the parent directory of path before a
// source is started. No operation after this returns resolves that parent by
// its original pathname.
func PrepareDownload(path string) (*DownloadDestination, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return nil, fmt.Errorf("local download: destination has no filename")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("local download: open destination directory %q: %w", filepath.Dir(path), err)
	}
	return &DownloadDestination{root: root, name: name}, nil
}

// Put streams into the pinned directory using the shared bounded sink. The
// sink's temporary and final files are both created as mode 0600.
func (d *DownloadDestination) Put(ctx context.Context, size int64, source io.Reader) (transfer.Outcome, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.root == nil {
		return transfer.Outcome{}, os.ErrClosed
	}
	upload := transfer.Upload{DestDir: ".", Name: d.name, Size: size, OnExists: transfer.Overwrite}
	return transfer.NewSink(rootedFS{root: d.root}, transfer.DefaultChunk).Put(ctx, upload, source, nil)
}

// Close releases the pinned directory after any active Put has unwound.
func (d *DownloadDestination) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.root == nil {
		return nil
	}
	err := d.root.Close()
	d.root = nil
	return err
}

type rootedFS struct{ root *os.Root }

func directComponent(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') ||
		(filepath.Separator != '/' && strings.ContainsRune(name, filepath.Separator)) {
		return fmt.Errorf("invalid rooted path %q: %w", name, os.ErrInvalid)
	}
	return nil
}

func (f rootedFS) Create(name string) (transfer.RemoteFile, error) {
	if err := directComponent(name); err != nil {
		return nil, err
	}
	file, err := f.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &syncingFile{file: file}, nil
}

func (f rootedFS) PosixRename(old, new string) error {
	if err := directComponent(old); err != nil {
		return err
	}
	if err := directComponent(new); err != nil {
		return err
	}
	return f.root.Rename(old, new)
}

func (f rootedFS) Rename(old, new string) error {
	if err := directComponent(old); err != nil {
		return err
	}
	if err := directComponent(new); err != nil {
		return err
	}
	if err := f.root.Link(old, new); err != nil {
		return err
	}
	if err := f.root.Remove(old); err != nil {
		return fmt.Errorf("local download: %s is now also at %s: %w", old, new, err)
	}
	return nil
}

func (f rootedFS) Remove(name string) error {
	if err := directComponent(name); err != nil {
		return err
	}
	return f.root.Remove(name)
}

var _ transfer.RemoteFS = rootedFS{}
