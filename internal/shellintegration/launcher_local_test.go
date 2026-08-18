package shellintegration

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestLocalBashRcfile_SecretRidesTextNotEnv is the nocx-u7uh.21 acceptance
// assertion in its strongest form: the rendered LOCAL rcfile must carry the
// capability and the recovery fence as substituted TEXT (@CAP@/@RECOVERY@),
// and the environment block inside it must NOT — a value in the env block
// reaches /proc/<pid>/environ of every child, which is exactly the leak
// ADR-0024 decision 2 exists to prevent.
func TestLocalBashRcfile_SecretRidesTextNotEnv(t *testing.T) {
	opts := LaunchOptions{
		SessionID:   "sid-local-1",
		Enhanced:    true,
		Capability:  strings.Repeat("ab", 32), // 64 hex chars
		Recovery:    strings.Repeat("cd", 32),
		Lane:        "lane-1",
		Domain:      "dom-1",
		Epoch:       7,
		LifecycleFD: 3,
	}
	rc, err := LocalBashRcfile(opts)
	if err != nil {
		t.Fatalf("LocalBashRcfile: %v", err)
	}
	// The secrets are in the rcfile text, assigned exactly once.
	if !strings.Contains(rc, "__nocx_cap='"+opts.Capability+"'") {
		t.Fatalf("rcfile must carry the capability as substituted text")
	}
	if !strings.Contains(rc, "__nocx_lc_recovery='"+opts.Recovery+"'") {
		t.Fatalf("rcfile must carry the recovery fence as substituted text")
	}
	// The env block (exported NOCX_* lines) must never contain them.
	env := launcherEnvBlock(opts)
	if strings.Contains(env, opts.Capability) {
		t.Fatalf("capability leaked into the exported environment block")
	}
	if strings.Contains(env, opts.Recovery) {
		t.Fatalf("recovery fence leaked into the exported environment block")
	}
	// The non-secret addressing does travel in the env block.
	for _, want := range []string{"NOCX_LIFECYCLE_LANE='lane-1'", "NOCX_LIFECYCLE_DOMAIN='dom-1'", "NOCX_LIFECYCLE_EPOCH=7", "NOCX_LIFECYCLE_FD=3", "NOCX_SESSION_ID='sid-local-1'"} {
		if !strings.Contains(env, want) {
			t.Fatalf("env block must carry %s, got:\n%s", want, env)
		}
	}
}

// TestLocalBashRcfile_RequiresEnhancedSession pins the precondition: a
// conventional (non-enhanced) session has no session id anchor and no
// lifecycle config to embed; refusing beats rendering a rcfile that claims
// an authenticated channel that cannot exist.
func TestLocalBashRcfile_RequiresEnhancedSession(t *testing.T) {
	if _, err := LocalBashRcfile(LaunchOptions{Enhanced: false}); err == nil {
		t.Fatal("a conventional session must not render a lifecycle rcfile")
	}
}

// TestWriteLocalRcfile_MatchesSelfDeleteGuard pins the file naming: the bash
// rcfile template self-deletes on a BASH_SOURCE matching */nocx-bash.??????
// (exactly six characters). A longer random suffix would never be removed
// and every session would leave a file containing the capability in TMPDIR.
// The file is created 0600 with O_EXCL from the start.
func TestWriteLocalRcfile_MatchesSelfDeleteGuard(t *testing.T) {
	path, err := writeLocalRcfileIn("# test rcfile\n", t.TempDir())
	if err != nil {
		t.Fatalf("writeLocalRcfileIn: %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	name := filepath.Base(path)
	if !regexp.MustCompile(`^nocx-bash\.[0-9a-f]{6}$`).MatchString(name) {
		t.Fatalf("rcfile name %q must match the template's */nocx-bash.?????? self-delete guard", name)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("rcfile mode = %o, want 0600 (it carries the capability)", st.Mode().Perm())
	}
}
