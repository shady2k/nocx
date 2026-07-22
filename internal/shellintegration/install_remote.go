package shellintegration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/pkg/sftp"
)

func (s *Impl) EnsureInstalledRemote(ctx context.Context, sshClient *gossh.Client, remoteHome string) error {
	if remoteHome == "" {
		return fmt.Errorf("shellintegration: remote home directory is empty")
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return fmt.Errorf("shellintegration: sftp client: %w", err)
	}
	defer sftpClient.Close()

	dir := path.Join(remoteHome, dirName)
	vf := path.Join(dir, versionFile)

	// Check version — skip if already installed and up to date.
	if versionMatches(sftpClient, vf) {
		s.log.Debug("shellintegration: remote already installed, version matches",
			"version", version)
		return nil
	}

	// Create directory.
	if err := sftpClient.MkdirAll(dir); err != nil {
		return fmt.Errorf("shellintegration: remote mkdir %s: %w", dir, err)
	}

	// Write scripts.
	for name, content := range scripts {
		p := path.Join(dir, name)
		if err := writeFile(sftpClient, p, content); err != nil {
			return fmt.Errorf("shellintegration: remote write script %s: %w", p, err)
		}
	}

	// Write version.
	if err := writeFile(sftpClient, vf, version+"\n"); err != nil {
		return fmt.Errorf("shellintegration: remote write version: %w", err)
	}

	// Append gate lines to rc files.
	for rcFile, gate := range rcGate {
		rcPath := path.Join(remoteHome, rcFile)
		if err := appendGateRemote(sftpClient, rcPath, gate); err != nil {
			s.log.Warn("shellintegration: failed to append gate to remote rc file",
				"path", rcPath, "error", err)
		}
	}

	s.log.Info("shellintegration: remote installed", "dir", dir, "version", version)
	return nil
}

// GetRemoteHome queries the remote host for the user's home directory.
func (s *Impl) GetRemoteHome(sshClient *gossh.Client) (string, error) {
	sess, err := sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("shellintegration: new session for home: %w", err)
	}
	defer sess.Close()

	output, err := sess.Output("echo $HOME")
	if err != nil {
		return "", fmt.Errorf("shellintegration: get remote home: %w", err)
	}
	home := strings.TrimSpace(string(output))
	if home == "" {
		sess2, err := sshClient.NewSession()
		if err != nil {
			return "", fmt.Errorf("shellintegration: new session for ~: %w", err)
		}
		defer sess2.Close()
		output2, err := sess2.Output("cd ~ && pwd")
		if err != nil {
			return "", fmt.Errorf("shellintegration: get remote home via ~: %w", err)
		}
		home = strings.TrimSpace(string(output2))
	}
	if home == "" {
		return "", fmt.Errorf("shellintegration: could not determine remote home")
	}
	return home, nil
}

func versionMatches(client *sftp.Client, versionPath string) bool {
	f, err := client.Open(versionPath)
	if err != nil {
		return false
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == version
}

func writeFile(client *sftp.Client, remotePath, content string) error {
	f, err := client.Create(remotePath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, bytes.NewReader([]byte(content)))
	return err
}

func appendGateRemote(client *sftp.Client, remotePath, gateLine string) error {
	f, err := client.Open(remotePath)
	if err != nil {
		return writeFile(client, remotePath, gateLine+"\n")
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return err
	}

	content := string(data)
	if strings.Contains(content, gateLine) {
		return nil
	}

	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += gateLine + "\n"
	return writeFile(client, remotePath, content)
}
