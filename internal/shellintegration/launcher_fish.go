package shellintegration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Fish has no pre-config command-line hook. The verified native prelude's first
// possible user autoloads are these two conditional helpers and the guaranteed
// interactive theme helper. A public function-path shim consumes the private
// pipe before forwarding the first one. Normal Fish, not --no-config, still owns
// history, universals, configuration ordering and the universal-change notifier.
// See ADR-0099. An unknown prelude gets no private descriptors at all.
var fishPreconfigEdges = [...]string{
	"__fish_set_locale", "__fish_macos_set_env", "__fish_theme_migrate",
}

// The Fish 4.9.3 prelude through its interactive theme call, with blank/comment
// lines and indentation removed. This is a compatibility/security gate, not a
// version guess: changing the prelude requires proving its earlier autoload
// edges before admitting it. Comments alone do not retire a working adapter.
var verifiedFishPrelude = [32]byte{
	0xf9, 0xdb, 0xcb, 0xb6, 0xbb, 0xb1, 0x7d, 0x39,
	0x93, 0x9c, 0xa9, 0x26, 0xf2, 0xc7, 0x0d, 0x7c,
	0x6c, 0x18, 0xf5, 0xd4, 0xda, 0x4e, 0xc5, 0xfd,
	0xdb, 0x97, 0xc0, 0x9b, 0xad, 0x45, 0x7a, 0x7f,
}

func fishPreludeSupported(src string) bool {
	var prefix strings.Builder
	for line := range strings.Lines(src) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix.WriteString(line)
		prefix.WriteByte('\n')
		if line == "__fish_theme_migrate" {
			return sha256.Sum256([]byte(prefix.String())) == verifiedFishPrelude
		}
	}
	return false
}

// Public discovery uses only reserved builtins and does not read user startup
// or universals. Older Fish keeps config.fish on disk; current Fish embeds it.
func fishNativeConfig(shellPath string, overrides []string) (config, version string, err error) {
	const query = `builtin printf '%s\n' "$version"
if builtin test -r "$__fish_data_dir/config.fish"
    builtin printf 'p%s' "$__fish_data_dir/config.fish"
else
    builtin printf e
    builtin status get-file config.fish
end`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shellPath, "--no-config", "--command", query) //nolint:gosec // the resolved account shell, fixed public discovery program
	if len(overrides) != 0 {
		cmd.Env = append(os.Environ(), overrides...)
	}
	out, err := cmd.Output()
	version, entrypoint, hasVersion := strings.Cut(string(out), "\n")
	// Version is diagnostic only. Restrict it to a short printable token before
	// carrying it to the UI; no version can authorize an unverified prelude.
	if len(version) > 64 || strings.IndexFunc(version, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("._+-", r))
	}) >= 0 {
		version = ""
	}
	if err != nil || !hasVersion || len(entrypoint) == 0 {
		return "", version, fmt.Errorf("shellintegration: cannot discover native fish startup")
	}
	switch entrypoint[0] {
	case 'p':
		contents, readErr := os.ReadFile(entrypoint[1:]) //nolint:gosec // the target Fish names its own public startup file
		return string(contents), version, readErr
	case 'e':
		return entrypoint[1:], version, nil
	default:
		return "", version, fmt.Errorf("shellintegration: native fish startup discovery returned no entrypoint")
	}
}

func localFishLaunch(shellPath string, opts LaunchOptions) (LocalLaunch, error) {
	conventional := LocalLaunch{Command: shellPath, Args: []string{"-i"}, Refusal: ReasonUnsupportedShell}
	if opts.AgentHelperPath == "" {
		return conventional, nil
	}
	config, version, err := fishNativeConfig(shellPath, opts.EnvOverrides)
	if err != nil || !fishPreludeSupported(config) {
		conventional.Refusal = ReasonUnsupportedFishStartup
		conventional.ShellVersion = version
		return conventional, nil
	}
	dir, err := os.MkdirTemp("", "nocx-fish-")
	if err != nil {
		return LocalLaunch{}, fmt.Errorf("shellintegration: fish startup directory: %w", err)
	}
	remove := func() { _ = os.RemoveAll(dir) }
	writePublic := func(name, text string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600)
	}
	if err = writePublic("__nocx_fish_preconfig.fish", `function __nocx_fish_preconfig
    builtin set --local fish_trace
    builtin source /dev/fd/3
end
`); err != nil {
		remove()
		return LocalLaunch{}, err
	}
	for _, name := range fishPreconfigEdges {
		text := "function " + name + "\n    __nocx_fish_preconfig\n    " + name + " $argv\nend\n"
		if err = writePublic(name+".fish", text); err != nil {
			remove()
			return LocalLaunch{}, err
		}
	}
	hooks := filepath.Join(dir, "nocx.fish")
	if err = writePublic("nocx.fish", localScript(fishScriptRaw, opts)); err != nil {
		remove()
		return LocalLaunch{}, err
	}
	// Only the public path is in argv/environment. The private header is fully
	// parsed from the pipe before any assignments or original helper execute.
	// Fish restores the inherited fd after scoped redirections, so do not
	// pretend to close it with Bash's exec syntax: it is an exhausted pipe,
	// never a seekable bearer file available to startup descendants.
	init := "builtin source " + fishQuote(hooks) + "; __nocx_fish_startup_cleanup"
	launch, err := pipeLaunch(shellPath, []string{"-i", "--init-command", init},
		[]string{"fish_function_path=" + dir}, fishPrivateBootstrap(dir, opts))
	if err != nil {
		remove()
		return LocalLaunch{}, err
	}
	abort := launch.Abort
	launch.Abort = func() {
		abort()
		remove()
	}
	return launch, nil
}

func fishPrivateBootstrap(dir string, opts LaunchOptions) string {
	var b strings.Builder
	for _, value := range []struct{ name, value string }{
		{"__nocx_cap", opts.Capability},
		{"__nocx_recovery", opts.Recovery},
		{"__nocx_agent_token", opts.AgentToolToken},
	} {
		b.WriteString("builtin set --global --unexport " + value.name + " " + fishQuote(value.value) + "\n")
	}
	forLauncherEnv(opts, func(name, value string) {
		b.WriteString("builtin set --global --export " + name + " " + fishQuote(value) + "\n")
	})
	b.WriteString("builtin set --global --unexport __nocx_fish_startup_dir " + fishQuote(dir) + "\n")
	b.WriteString(`function __nocx_fish_startup_cleanup --on-event fish_exit
    builtin set --local fish_trace
    command rm -rf -- "$__nocx_fish_startup_dir"
    builtin set --erase --global __nocx_fish_startup_dir
    builtin functions --erase __nocx_fish_startup_cleanup
end
`)
	// An inherited exported global must keep shadowing a universal, even when
	// empty. Only original absence permits erasing our global and revealing a
	// native universal. Fish imports this non-path environment variable as one
	// string, not a colon-split list.
	original, present := os.LookupEnv("fish_function_path")
	for _, entry := range opts.EnvOverrides {
		if value, ok := strings.CutPrefix(entry, "fish_function_path="); ok {
			original, present = value, true
		}
	}
	if present {
		b.WriteString("builtin set --global --export fish_function_path " + fishQuote(original) + "\n")
	} else {
		b.WriteString(`builtin set --erase --global fish_function_path
if not builtin set --query fish_function_path
    # Restore the default that the native prelude skipped while our path was
    # present. Fish has already computed all vendor directories itself.
    builtin set --global fish_function_path $__fish_config_dir/functions $__fish_sysconf_dir/functions $__fish_vendor_functionsdirs
end
`)
	}
	// Changing the path invalidates autoloaded shims without the tombstone
	// functions --erase would leave. The suspended shim then forwards through
	// the real user/default autoload, with its original arguments and status.
	return b.String()
}

var fishShellQuoter = strings.NewReplacer(`\`, `\\`, `'`, `\'`)

// Fish permits escaped backslashes and apostrophes inside single quotes;
// POSIX's close/quote/reopen idiom is a different grammar.
func fishQuote(s string) string {
	return "'" + fishShellQuoter.Replace(s) + "'"
}
