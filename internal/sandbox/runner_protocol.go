package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	RunnerPlanVersion  = 1
	RunnerReadyVersion = 1
	RunnerFDPlan       = 5
	RunnerFDReady      = 6
	RunnerFDError      = 7
	RunnerFDRoots      = 8
	MaxRunnerPlanBytes = 128 << 10
	MaxRunnerArgs      = 64
	MaxRunnerArgsBytes = 16 << 10
	MaxRunnerKeepFDs   = 2
)

var ErrInvalidRunnerPlan = errors.New("sandbox runner: invalid launch plan")

// RunnerPlan is private launch input carried only on inherited FD5. Descriptor
// numbers are ephemeral transport data and are never part of Policy or its digest.
type RunnerPlan struct {
	Version     int      `json:"version"`
	Policy      Policy   `json:"policy"`
	Digest      string   `json:"digest"`
	Args        []string `json:"args"`
	RootFDs     []int    `json:"rootFds"`
	WorkspaceFD int      `json:"workspaceFd"`
	KeepFDs     []int    `json:"keepFds"`
	ProbePath   string   `json:"probePath,omitempty"`   // Private startup denial probe, never policy authority.
	ProbeSocket string   `json:"probeSocket,omitempty"` // Host-created endpoint under workspace RW.
}

type RunnerReady struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
}

type RunnerFailure struct {
	Version int    `json:"version"`
	Code    string `json:"code"`
}

// RunnerStatus is one FD6 SOCK_SEQPACKET record; failure records identify the
// bounded reason while a successful record confirms native policy installation.
type RunnerStatus struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
}

// ReadRunnerPlan strictly decodes the bounded document inherited on FD5.
func ReadRunnerPlan(fd int) (RunnerPlan, error) {
	var plan RunnerPlan
	f := os.NewFile(uintptr(fd), "runner-plan")
	if f == nil {
		return plan, runnerErr("plan-fd")
	}
	defer func() { _ = f.Close() }()
	limited, err := io.ReadAll(io.LimitReader(f, MaxRunnerPlanBytes+1))
	if err != nil || len(limited) == 0 || len(limited) > MaxRunnerPlanBytes {
		return plan, runnerErr("plan-size")
	}
	if err := rejectDuplicateJSONFields(limited); err != nil {
		return plan, runnerErr("plan-duplicate-field")
	}
	dec := json.NewDecoder(bytes.NewReader(limited))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return plan, runnerErr("plan-decode")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return plan, runnerErr("plan-trailing")
	}
	if err := validateRunnerPlan(plan); err != nil {
		return RunnerPlan{}, err
	}
	return plan, nil
}

// EncodeRunnerPlan validates before encoding; callers write these bytes to FD5.
func EncodeRunnerPlan(plan RunnerPlan) ([]byte, error) {
	if err := validateRunnerPlan(plan); err != nil {
		return nil, err
	}
	data, err := json.Marshal(plan)
	if err != nil || len(data) > MaxRunnerPlanBytes {
		return nil, runnerErr("plan-size")
	}
	return data, nil
}

func validateRunnerPlan(plan RunnerPlan) error {
	if plan.Version != RunnerPlanVersion {
		return runnerErr("plan-version")
	}
	encoded, digest, err := EncodePolicy(plan.Policy)
	if err != nil || len(encoded) > MaxPolicyBytes || len(plan.Digest) != 64 || plan.Digest != digest {
		return runnerErr("policy-digest")
	}
	if !supportedRunnerBackend(plan.Policy.Backend, plan.Policy.BackendVersion) {
		return runnerErr("unsupported-backend")
	}
	if !canonicalRunnerPath(plan.Policy.Shell) || !canonicalRunnerPath(plan.Policy.Runner) || !canonicalRunnerPath(plan.Policy.WorkspaceRoot) {
		return runnerErr("policy-path")
	}
	for _, root := range plan.Policy.Roots {
		if !canonicalRunnerPath(root.Path) {
			return runnerErr("root-path")
		}
	}
	if plan.Policy.Backend == MacOSSeatbelt {
		if !canonicalRunnerPath(plan.ProbePath) || len(plan.ProbePath) > 80 {
			return runnerErr("probe-path")
		}
		if !canonicalRunnerPath(plan.ProbeSocket) || len(plan.ProbeSocket) >= 104 || !contained(plan.Policy.WorkspaceRoot, plan.ProbeSocket) {
			return runnerErr("probe-socket")
		}
		for _, root := range plan.Policy.Roots {
			if root.Access == ReadWrite && contained(root.Path, plan.ProbePath) {
				return runnerErr("probe-authority")
			}
		}
	} else if plan.ProbePath != "" || plan.ProbeSocket != "" {
		return runnerErr("probe-path")
	}
	for i := 1; i < len(plan.Policy.Roots); i++ {
		a, b := plan.Policy.Roots[i-1], plan.Policy.Roots[i]
		less := false
		switch {
		case a.Path != b.Path:
			less = a.Path < b.Path
		case a.Access != b.Access:
			less = a.Access < b.Access
		case a.Kind != b.Kind:
			less = a.Kind < b.Kind
		case a.Provenance != b.Provenance:
			less = a.Provenance < b.Provenance
		case a.Identity.Device != b.Identity.Device:
			less = a.Identity.Device < b.Identity.Device
		default:
			less = a.Identity.Inode < b.Identity.Inode
		}
		if !less {
			return runnerErr("root-order")
		}
	}
	for _, root := range plan.Policy.Roots {
		if root.Identity.Inode == 0 {
			return runnerErr("root-identity")
		}
	}
	if len(plan.RootFDs) != len(plan.Policy.Roots) {
		return runnerErr("root-fds")
	}
	for i, fd := range plan.RootFDs {
		if fd != RunnerFDRoots+i {
			return runnerErr("root-fds")
		}
	}
	if plan.WorkspaceFD != RunnerFDRoots+len(plan.RootFDs) {
		return runnerErr("workspace-fd")
	}
	if len(plan.Args) == 0 || len(plan.Args) > MaxRunnerArgs {
		return runnerErr("arguments")
	}
	n := 0
	for _, arg := range plan.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return runnerErr("arguments")
		}
		n += len(arg)
		if n > MaxRunnerArgsBytes {
			return runnerErr("arguments")
		}
	}
	if plan.Args[0] != plan.Policy.Shell {
		return runnerErr("arguments")
	}
	if len(plan.KeepFDs) > MaxRunnerKeepFDs {
		return runnerErr("keep-fds")
	}
	keep := map[int]bool{}
	for _, fd := range plan.KeepFDs {
		if fd != 3 && fd != 4 || keep[fd] {
			return runnerErr("keep-fds")
		}
		keep[fd] = true
	}
	return nil
}

func supportedRunnerBackend(backend Backend, version int) bool {
	return backend == LinuxLandlock && version == LinuxBaselineVersion ||
		backend == MacOSSeatbelt && version == MacOSBaselineVersion
}

func canonicalRunnerPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\r\n")
}

func rejectDuplicateJSONFields(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return runnerErr("plan-json")
				}
				if _, exists := seen[key]; exists {
					return runnerErr("plan-duplicate-field")
				}
				seen[key] = struct{}{}
				if err := value(); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return runnerErr("plan-json")
			}
		case '[':
			for dec.More() {
				if err := value(); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return runnerErr("plan-json")
			}
		default:
			return runnerErr("plan-json")
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return runnerErr("plan-trailing")
	}
	return nil
}

func runnerErr(code string) error { return fmt.Errorf("%w: %s", ErrInvalidRunnerPlan, code) }
