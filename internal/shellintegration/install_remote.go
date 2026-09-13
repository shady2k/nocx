package shellintegration

import (
	"context"
	"fmt"
	"path"
)

// EnsureInstalledRemote publishes the integration bundle through an injected
// filesystem carrier. The composition root owns adapting SSH/SFTP to FS, so
// this package remains usable by the helper without linking an SSH client.
func (s *Impl) EnsureInstalledRemote(_ context.Context, fs FS, remoteHome string) error {
	if remoteHome == "" {
		return fmt.Errorf("shellintegration: remote home directory is empty")
	}
	return s.publishRemote(fs, remoteHome, "remote bundle published")
}

// UninstallRemote removes the committed integration bundle through an injected
// filesystem carrier. Only manifest-owned, unmodified files are removed;
// ~/.nocx and the launch carrier remain in place.
func (s *Impl) UninstallRemote(_ context.Context, fs FS, remoteHome string) (removed, conflicts []string, err error) {
	if remoteHome == "" {
		return nil, nil, fmt.Errorf("shellintegration: remote home directory is empty")
	}

	root := path.Join(remoteHome, dirName)
	res, err := NewPublisher(s.log, fs, root).Uninstall()
	if err != nil {
		return nil, nil, fmt.Errorf("shellintegration: remote uninstall: %w", err)
	}
	s.log.Info("shellintegration: remote bundle uninstalled",
		"root", root, "removed", res.Removed, "conflicts", res.Conflicts)
	return res.Removed, res.Conflicts, nil
}

// EnsureInstalledOverPipe publishes the bundle through the same FS seam as
// EnsureInstalledRemote. The caller adapts its already-open auxiliary channel
// to FS at the composition root.
func (s *Impl) EnsureInstalledOverPipe(_ context.Context, fs FS, remoteHome string) error {
	if remoteHome == "" {
		return fmt.Errorf("shellintegration: remote home directory is empty")
	}
	return s.publishRemote(fs, remoteHome, "remote bundle published over the multiplex master")
}

func (s *Impl) publishRemote(fs FS, remoteHome, event string) error {
	root := path.Join(remoteHome, dirName)
	res, err := NewPublisher(s.log, fs, root).Publish(launchBundle())
	if err != nil {
		s.log.Info("remote bundle publish refused",
			"root", root, "error", err)
		return fmt.Errorf("shellintegration: remote publish: %w", err)
	}
	s.log.Info(event,
		"root", root, "version", res.Version, "generation", res.Generation,
		"published", res.Published, "reason", res.Reason)
	return nil
}

// GetRemoteHome asks the remote host where the account's home is, through an
// injected seam. The composition root adapts a transport to that seam without
// exposing its concrete type here.
//
// The QUESTION is this function's and the COMMANDS are internal/remoteprobe's:
// the probe is one fixed command and the fallback is another, both spelled
// there, so this package neither composes shell text nor depends on which
// transport runs it.
func (s *Impl) GetRemoteHome(home RemoteHome) (string, error) {
	dir, err := home.Home()
	if err != nil {
		return "", fmt.Errorf("shellintegration: get remote home: %w", err)
	}
	if dir == "" {
		return "", fmt.Errorf("shellintegration: could not determine remote home")
	}
	return dir, nil
}
