package shellintegration

import (
	"fmt"
	"os"
	"strings"

	"github.com/shady2k/nocx/internal/log"
)

// CwdInfo carries the validated cwd payload defined by AD-5.
// The backend owns local-vs-remote and validates the host;
// the frontend supplies the desired path (already percent-decoded
// by the VT frontend via parser.registerOscHandler).
type CwdInfo struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

// ShellIntegration is the OSC 7/133 substrate contract (Tier A shell hooks
// now; Tier B remote-helper seam later). Per AD-6 the backend never parses
// OSC — the VT frontend surfaces OSC events and the backend only validates
// the results. AD-5 splits ownership: backend owns host validation and
// local-vs-remote; the frontend supplies the desired path.
//
// NOT CALLED YET, and that is a decision rather than an oversight — do not
// delete it as dead code. Every session today is local, so the host arrives
// empty and validating it would mean an IPC round trip per `cd` to confirm
// an empty string. The call gets wired when SSH lands and the host starts
// coming from someone else's shell, which is the case AD-5 was written for
// (nocx-3p1).
type ShellIntegration interface {
	// ValidateCwd validates a frontend-supplied host and path from an
	// OSC 7 event. For local sessions, host must be empty or "localhost";
	// for remote sessions (Phase 2) host is the remote hostname.
	// Returns the validated CwdInfo or an error.
	ValidateCwd(host string, path string) (CwdInfo, error)
}

// Stub is the MVP implementation: local sessions only, host must be
// localhost or empty.
type Stub struct {
	log    log.Logger
	isHost func(host string) bool
}

// NewStub returns a Stub wired with os.Hostname as the hostname check.
func NewStub(logger log.Logger) *Stub {
	return &Stub{
		log:    logger,
		isHost: isLocalHost,
	}
}

func (s *Stub) ValidateCwd(host string, path string) (CwdInfo, error) {
	if !s.isHost(host) {
		return CwdInfo{}, fmt.Errorf("shellintegration: host %q is not localhost", host)
	}
	if path == "" {
		return CwdInfo{}, fmt.Errorf("shellintegration: path must not be empty")
	}
	if !strings.HasPrefix(path, "/") && path != "~" {
		return CwdInfo{}, fmt.Errorf("shellintegration: path %q is not absolute", path)
	}
	s.log.Debug("shellintegration: cwd validated", "host", host, "path", path)
	return CwdInfo{Host: host, Path: path}, nil
}

// isLocalHost returns true when host is empty, "localhost", or
// the machine's own hostname.
func isLocalHost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	hn, err := os.Hostname()
	if err != nil {
		return false
	}
	return host == hn
}
