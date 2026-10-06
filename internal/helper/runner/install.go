// Package runner installs this helper build's minimal native executable. Its
// bytes are embedded in the helper generation; mutable paths are never a source.
package runner

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

var (
	ErrNotBuilt = errors.New("sandbox: runner artifact unavailable")
	ErrInstall  = errors.New("sandbox: runner artifact installation refused")
)

const maxRunnerBytes = 32 << 20

func Install(privateDir string) (_ string, err error) {
	compressed, openErr := runnerFS.Open("bin/" + runtime.GOOS + "-" + runtime.GOARCH + "/nocx-sandbox-runner.gz")
	if openErr != nil {
		return "", ErrNotBuilt
	}
	defer func() { _ = compressed.Close() }()
	reader, gzipErr := gzip.NewReader(compressed)
	if gzipErr != nil {
		return "", ErrInstall
	}
	defer func() { _ = reader.Close() }()
	if err = os.MkdirAll(privateDir, 0o700); err != nil {
		return "", ErrInstall
	}
	dirFD, openErr := unix.Open(privateDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if openErr != nil {
		return "", ErrInstall
	}
	defer func() { _ = unix.Close(dirFD) }()
	var directory unix.Stat_t
	if unix.Fstat(dirFD, &directory) != nil || int64(directory.Uid) != int64(os.Getuid()) || directory.Mode&0o777 != 0o700 {
		return "", ErrInstall
	}
	file, createErr := os.CreateTemp(privateDir, ".native-runner-")
	if createErr != nil {
		return "", ErrInstall
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, maxRunnerBytes+1))
	if copyErr != nil || written == 0 || written > maxRunnerBytes {
		return "", ErrInstall
	}
	if file.Chmod(0o700) != nil || file.Sync() != nil || file.Close() != nil {
		return "", ErrInstall
	}
	digest := hash.Sum(nil)
	name := hex.EncodeToString(digest)
	target := filepath.Join(privateDir, name)
	// linkat never overwrites a prior install; a collision is accepted only
	// after nofollow owner/mode/content verification of the exact artifact.
	if linkErr := unix.Linkat(dirFD, filepath.Base(file.Name()), dirFD, name, 0); linkErr != nil {
		if linkErr != unix.EEXIST || !matchesArtifact(dirFD, name, digest) {
			return "", ErrInstall
		}
	}
	if unix.Unlinkat(dirFD, filepath.Base(file.Name()), 0) != nil || unix.Fsync(dirFD) != nil {
		return "", ErrInstall
	}
	return target, nil
}

func matchesArtifact(dirFD int, name string, want []byte) bool {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), "sandbox-runner")
	defer func() { _ = file.Close() }()
	var state unix.Stat_t
	if unix.Fstat(fd, &state) != nil || state.Mode&unix.S_IFMT != unix.S_IFREG || int64(state.Uid) != int64(os.Getuid()) || state.Mode&0o777 != 0o700 {
		return false
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, maxRunnerBytes+1))
	if err != nil || read == 0 || read > maxRunnerBytes {
		return false
	}
	got := hash.Sum(nil)
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
