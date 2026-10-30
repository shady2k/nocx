package runtimebundle

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/assistant/pydantic"
	"github.com/shady2k/nocx/internal/coordinator"
)

// Locator owns the coordinator's immutable runtime pin. Construction does no
// IO; startup explicitly Pin/Adopts even when the experimental flag is off.
type Locator struct {
	root, expected, target string
	view                   View
	err                    error
	mu                     sync.Mutex
	lease                  *Lease
	inheritedFD            int
	closed                 bool
}

func New(root, expectedDigest, target string) *Locator {
	return &Locator{root: root, expected: expectedDigest, target: target, view: osView{}, inheritedFD: -1}
}

// LocatorForExecutable treats a present empty handoff as explicitly unavailable,
// never as permission to search the environment or use a user's Python.
func LocatorForExecutable(executable string) *Locator {
	return locatorForExecutable(executable, runtime.GOOS, runtime.GOARCH, os.LookupEnv)
}

func locatorForExecutable(executable, goos, goarch string, lookup func(string) (string, bool)) *Locator {
	target := goos + "/" + goarch
	if root, present := lookup(coordinator.AgentRuntimeRootEnv); present {
		digest, _ := lookup(coordinator.AgentRuntimeDigestEnv)
		locator := New(root, digest, target)
		if value, present := lookup(coordinator.AgentRuntimeLeaseFDEnv); present {
			fd, err := strconv.Atoi(value)
			if err != nil || fd < 3 {
				locator.err = ErrInvalid
			} else {
				locator.inheritedFD = fd
			}
		}
		return locator
	}
	root, err := PackagedRoot(executable, goos, goarch)
	locator := New(root, "", target)
	locator.err = err
	return locator
}

// PackagedRoot is shared by the desktop launcher and standalone coordinator.
func PackagedRoot(executable, goos, goarch string) (string, error) {
	if !filepath.IsAbs(executable) || !supportedTarget(goos+"/"+goarch) {
		return "", ErrInvalid
	}
	base := filepath.Dir(executable)
	if goos == "darwin" {
		return filepath.Clean(filepath.Join(base, "..", "Resources", "agent-runtime", "darwin-"+goarch)), nil
	}
	return filepath.Clean(filepath.Join(base, "..", "lib", "nocx", "agent-runtime", "linux-"+goarch)), nil
}

// Adopt transfers lease ownership on success only. A failed adoption leaves
// the caller responsible for closing its lease. No manifest bytes are read
// before this shared lock protects the version against pruning.
func (l *Locator) Adopt(lease *Lease) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrLeaseClosed
	}
	if l.err != nil {
		return l.err
	}
	if l.lease != nil {
		return ErrInvalid
	}
	digest := l.expected
	if digest == "" {
		digest = filepath.Base(l.root)
	}
	if err := lease.validateShared(l.root, digest); err != nil {
		return err
	}
	l.expected, l.lease = digest, lease
	return nil
}

// Pin adopts the launcher's inherited descriptor, or acquires an independent
// shared descriptor for an explicitly staged root. Mutable packaged sources
// must first pass serverbin.PrepareRuntime at the composition root.
func (l *Locator) Pin(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.closed {
		return ErrLeaseClosed
	}
	if l.err != nil {
		return l.err
	}
	if l.root == "" {
		return fs.ErrNotExist
	}
	if l.lease != nil {
		if l.lease.File() == nil {
			return ErrLeaseClosed
		}
		return nil
	}
	digest := l.expected
	if digest == "" {
		digest = filepath.Base(l.root)
	}
	if !ValidDigest(digest) || filepath.Base(l.root) != digest {
		return ErrInvalid
	}
	var lease *Lease
	var err error
	if l.inheritedFD >= 3 {
		lease, err = inheritedLease(l.inheritedFD, l.root, digest)
	} else {
		lease, err = AcquireShared(ctx, filepath.Dir(l.root), digest)
	}
	if err != nil {
		return err
	}
	if err = lease.validateShared(l.root, digest); err != nil {
		_ = lease.Close()
		return err
	}
	l.expected, l.lease = digest, lease
	return nil
}

// Close is called after stopping model operations. Already-running Python
// children retain their own inherited descriptor even if the parent crashes.
func (l *Locator) Close() error {
	l.mu.Lock()
	l.closed = true
	lease := l.lease
	l.lease = nil
	l.mu.Unlock()
	return lease.Close()
}

func (l *Locator) Locate(ctx context.Context) (pydantic.Runtime, error) {
	unavailable := func(err error) (pydantic.Runtime, error) {
		return pydantic.Runtime{}, fmt.Errorf("Pydantic agent runtime is unavailable; repair the installation or disable Pydantic agent (Experimental) in Settings: %w", err)
	}
	if err := l.Pin(ctx); err != nil {
		return unavailable(err)
	}
	l.mu.Lock()
	digest := l.expected
	leaseFile := l.lease.File()
	l.mu.Unlock()
	if leaseFile == nil {
		return unavailable(ErrLeaseClosed)
	}
	manifest, err := ReadManifest(ctx, l.view, l.root, digest, l.target)
	if err != nil {
		return unavailable(err)
	}
	for _, name := range []string{InterpreterPath, EntrypointPath} {
		if err := manifest.available(l.view, l.root, name); err != nil {
			return unavailable(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return unavailable(err)
	}
	return pydantic.Runtime{Interpreter: filepath.Join(l.root, filepath.FromSlash(InterpreterPath)), Entrypoint: filepath.Join(l.root, filepath.FromSlash(EntrypointPath)), Directory: filepath.Join(l.root, "app"), ManifestDigest: manifest.Digest, Version: manifest.Digest, Lease: leaseFile}, nil
}

func (m *Manifest) available(view View, root, name string) error {
	for {
		parts := strings.Split(name, "/")
		for n := 1; n < len(parts); n++ {
			info, err := view.Lstat(filepath.Join(root, filepath.FromSlash(strings.Join(parts[:n], "/"))))
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrInvalid
			}
		}
		entry, ok := m.entries[name]
		if !ok {
			return ErrInvalid
		}
		absolute := filepath.Join(root, filepath.FromSlash(name))
		info, err := view.Lstat(absolute)
		if err != nil {
			return err
		}
		if entry.Link != "" {
			if info.Mode()&os.ModeSymlink == 0 {
				return ErrInvalid
			}
			link, err := view.Readlink(absolute)
			if err != nil {
				return err
			}
			if link != entry.Link {
				return ErrInvalid
			}
			name, err = m.linkTarget(name, func(dir string) error {
				dirInfo, statErr := view.Lstat(filepath.Join(root, filepath.FromSlash(dir)))
				if statErr != nil {
					return statErr
				}
				if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
					return ErrInvalid
				}
				return nil
			})
			if err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Size() != entry.Size || (info.Mode().Perm()&0o111 != 0) != entry.Executable {
			return ErrInvalid
		}
		return nil
	}
}

type osView struct{}

func (osView) Lstat(name string) (fs.FileInfo, error) { return os.Lstat(name) }
func (osView) Open(name string) (io.ReadCloser, error) {
	return os.Open(name) //nolint:gosec // ReadManifest/VerifyTree supply validated absolute bundle paths; this OS adapter grants no path authority
}
func (osView) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (osView) Readlink(name string) (string, error)       { return os.Readlink(name) }
