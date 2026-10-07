//go:build darwin

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

const seatbeltExecutable = "/usr/bin/sandbox-exec"

func backendAvailable() error {
	info, err := os.Stat(seatbeltExecutable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("sandbox-exec unavailable")
	}
	paths := baseline("darwin")
	roots := make([]Root, 0, len(paths))
	for _, path := range paths {
		rootInfo, rootErr := os.Stat(path)
		if errors.Is(rootErr, os.ErrNotExist) {
			continue
		}
		if rootErr != nil || !rootInfo.IsDir() {
			return errors.New("Seatbelt baseline unavailable")
		}
		actual, canonicalErr := canonicalDir(path)
		if canonicalErr != nil {
			return errors.New("Seatbelt baseline unavailable")
		}
		roots = append(roots, Root{Path: actual, Access: ReadOnly, Kind: DirectoryRoot, Provenance: SystemRoot})
	}
	tempRoot, err := canonicalDir(os.TempDir())
	if err != nil {
		return errors.New("Seatbelt temporary directory unavailable")
	}
	profile, err := CompileSeatbeltProfile(Policy{
		Version: PolicyVersion, Backend: MacOSSeatbelt, BackendVersion: MacOSBaselineVersion,
		WorkspaceRoot: "/", Shell: "/usr/bin/true", Runner: "/usr/bin/true",
		Runtime: RuntimePaths{Root: tempRoot}, Roots: roots,
	}, "0123456789abcdef0123456789abcdef")
	if err != nil {
		return errors.New("Seatbelt profile unsupported")
	}
	file, writer, err := os.Pipe()
	if err != nil {
		return errors.New("Seatbelt private profile unavailable")
	}
	defer func() { _ = file.Close() }()
	defer func() { _ = writer.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, seatbeltExecutable, "-f", "/dev/fd/3", "/usr/bin/true")
	command.ExtraFiles = []*os.File{file}
	command.Env = []string{"PATH=/usr/bin:/bin"}
	command.WaitDelay = 2 * time.Second
	if err = command.Start(); err != nil {
		return errors.New("Seatbelt enforcement unavailable")
	}
	// Only the child keeps the read end: cancellation must break a blocked write.
	_ = file.Close()
	_, writeErr := writer.WriteString(profile)
	closeErr := writer.Close()
	runErr := command.Wait()
	if writeErr != nil || closeErr != nil || runErr != nil {
		return errors.New("Seatbelt enforcement unavailable")
	}
	return nil
}

func privateSeatbeltProfile(directory, text string) (_ *os.File, err error) {
	file, err := os.CreateTemp(directory, ".seatbelt-profile-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	if err = os.Remove(file.Name()); err == nil {
		_, err = file.WriteString(text)
	}
	if err == nil {
		_, err = file.Seek(0, 0)
	}
	if err != nil {
		return nil, err
	}
	return file, nil
}
