//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

// The sandboxed shell is launched through a re-exec of os.Executable() as an
// internal helper (design spec §8.2): the helper decodes the bounded policy,
// sets PR_SET_NO_NEW_PRIVS, applies strict Landlock path restrictions, and
// only then unix.Exec's the real shell — without an intermediate shell. The
// parent waits on a readiness pipe before the session is registered, so a
// session never exists before enforcement is in place.
//
// FD protocol (ExtraFiles order):
//   - fd 3 (helperPolicyFD): the unlinked mode-0600 policy payload (memfd),
//     written and rewound by the parent before start.
//   - fd 4 (helperStatusFD): the write end of the readiness pipe. The helper
//     writes 0x00 after restriction succeeds, or 0x01 plus a reason string
//     before exiting 126 on any failure.
const (
	helperArg       = "__sandbox-landlock-exec"
	helperPolicyFD  = 3
	helperStatusFD  = 4
	helperExitSetup = 126
)

// helperPayload is the FD protocol document: the common policy plus the
// ordinary shell command the helper must exec after enforcing. It is
// internal to the helper handshake — it is not the common policy contract.
type helperPayload struct {
	Policy  *Policy     `json:"policy"`
	Command CommandSpec `json:"command"`
}

// MaybeHelper runs the sandbox helper when this process was re-executed with
// the helper marker as argv[1], and returns true (the process has exited).
// It must be called before app startup (design spec §8.2).
func MaybeHelper() bool {
	if len(os.Args) < 2 || os.Args[1] != helperArg {
		return false
	}
	code := helperMain(helperPolicyFD, helperStatusFD)
	os.Exit(code)
	return true
}

// helperMain is the helper body. policyFD/statusFD are the raw descriptors
// from the FD protocol; production uses the constants above, tests inject
// their own.
func helperMain(policyFD, statusFD int) int {
	policyFile := os.NewFile(uintptr(policyFD), "policy")
	defer func() { _ = policyFile.Close() }()

	payload, err := readPayload(policyFile)
	if err != nil {
		return helperReport(statusFD, "decode policy: "+err.Error())
	}
	if err := ValidatePolicy(payload.Policy); err != nil {
		return helperReport(statusFD, "invalid policy: "+err.Error())
	}

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return helperReport(statusFD, "prctl(PR_SET_NO_NEW_PRIVS): "+err.Error())
	}

	cfg := strictConfig(landlockABIVersion())
	rules := buildRules(payload.Policy)
	if err := cfg.RestrictPaths(rules...); err != nil {
		return helperReport(statusFD, "landlock: "+err.Error())
	}

	statusFile := os.NewFile(uintptr(statusFD), "status")
	if _, err := statusFile.Write([]byte{0}); err != nil {
		_ = statusFile.Close()
		return helperExitSetup
	}
	_ = statusFile.Close()

	// The policy is enforced; replace the process with the real shell.
	env := stripHelperEnv(payload.Command.Env)
	args := append([]string{payload.Command.Path}, payload.Command.Args...)
	if err := unix.Exec(payload.Command.Path, args, env); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox helper: exec %s: %v\n", payload.Command.Path, err)
		return helperExitSetup
	}
	return helperExitSetup // unreachable
}

// readPayload reads and decodes the bounded FD payload.
func readPayload(r io.Reader) (*helperPayload, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPolicyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPolicyBytes {
		return nil, fmt.Errorf("policy payload exceeds %d bytes", maxPolicyBytes)
	}
	var payload helperPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if payload.Policy == nil {
		return nil, fmt.Errorf("policy document missing")
	}
	if payload.Command.Path == "" {
		return nil, fmt.Errorf("command path missing")
	}
	return &payload, nil
}

// buildRules converts the common policy into Landlock path rules. The policy
// keeps surfaced writable roots separate from its finite device allowlist, so
// the latter cannot accidentally become a broad write grant. Executable files
// are granted through ROFiles; directories through RODirs.
func buildRules(p *Policy) []landlock.Rule {
	rules := make([]landlock.Rule, 0, 5)
	if len(p.ReadOnlyRoots) > 0 {
		rules = append(rules, landlock.RODirs(p.ReadOnlyRoots...))
	}
	if len(p.WritableRoots) > 0 {
		rules = append(rules, landlock.RWDirs(p.WritableRoots...))
	}
	if len(p.WritableDirs) > 0 {
		rules = append(rules, landlock.RWDirs(p.WritableDirs...))
	}
	if len(p.ReadOnlyFiles) > 0 {
		rules = append(rules, landlock.ROFiles(p.ReadOnlyFiles...))
	}
	if len(p.WritableFiles) > 0 {
		rules = append(rules, landlock.RWFiles(p.WritableFiles...))
	}
	return rules
}

// landlockABIVersion returns the ABI the go-landlock enforcement uses
// internally (errata-aware). zero means no usable Landlock.
func landlockABIVersion() int {
	abi, err := detectABI()
	if err != nil || abi < 1 {
		return 0
	}
	return abi
}

// stripHelperEnv removes helper-internal variables so they never reach the
// shell (design spec §8.2 step 6).
func stripHelperEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, helperEnvPrefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// helperReport writes the failure byte plus a reason to the status pipe and
// returns the helper exit code. Best-effort: a dead pipe is not a second
// failure worth surfacing.
func helperReport(statusFD int, detail string) int {
	f := os.NewFile(uintptr(statusFD), "status")
	defer func() { _ = f.Close() }()
	_, _ = f.Write([]byte{1})
	if detail != "" {
		_, _ = io.WriteString(f, detail)
	}
	return helperExitSetup
}
