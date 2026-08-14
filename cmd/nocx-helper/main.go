// Command nocx-helper is the remote-helper binary: it is launched on a
// remote host over a single pty-less SSH exec channel and speaks the frame
// protocol over stdin/stdout (design §4). It serves the git service; files,
// ports and session are reserved names.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"

	"github.com/shady2k/nocx/internal/git/hostsvc"
	"github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/host"
)

func main() {
	// stdout is the wire; every diagnostic goes to stderr (D22).
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	exe, err := os.Executable()
	if err != nil {
		log.Error("executable", "err", err)
		os.Exit(1)
	}
	contentHash, err := hashFile(exe)
	if err != nil {
		log.Error("content hash", "err", err)
		os.Exit(1)
	}
	instanceID, err := randomID()
	if err != nil {
		log.Error("instance id", "err", err)
		os.Exit(1)
	}

	factory := local.NewFactory()
	defer factory.Stop()
	h := host.New(os.Stdin, os.Stdout, contentHash, instanceID, log)
	h.Register(hostsvc.New(factory))
	if err := h.Serve(context.Background()); err != nil {
		if errors.Is(err, host.ErrVersionMismatch) {
			os.Exit(host.ExitVersionMismatch)
		}
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

// hashFile hashes the running binary's bytes; the content hash travels in
// the hello-ok so the backend can verify the installed helper is the one it
// deployed (D7).
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 — the path is the running binary, from os.Executable()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// randomID mints the instance id that distinguishes one helper run from
// another.
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
