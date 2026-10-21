package shellintegration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE PLUGIN STACK AN ORDINARY ZSH HAS (nocx-1ebrs.27).
//
// `fast-syntax-highlighting`, `zsh-syntax-highlighting` and
// `zsh-autosuggestions` are among the most-installed zsh plugins that exist,
// and every one of them WRAPS zle widgets: it saves the implementation it
// finds and registers its own function that calls through it. That is the
// widget ecosystem's convention, not an abuse of it, and an integration script
// that installs wrappers of its own has to compose with it in EITHER order — a
// person sources their plugins wherever their startup files put them.
//
// WHY THESE ARE REQUIRED AND NOT OPTIONAL. The bead this test belongs to was
// filed from a measured bisect on the shipped 0.6.0 that came down to one of
// these plugins, and the failure mode was a pane that never integrated. A test
// that SKIPPED when the plugins are absent would report that stack as covered
// on every machine that has none — every CI machine until this test's
// provisioning existed — so it fails instead, the way requireIntegrationShell
// already does (nocx-gd84). A skip is a silent success, and this is the class
// the silent success already cost us once.

// zshZlePlugin is one widget-wrapping plugin: its name, the file a user
// sources, and the install directories a package manager puts it in.
type zshZlePlugin struct {
	name  string
	entry string
	dirs  []string
}

var (
	zshPluginFastSyntax = zshZlePlugin{
		name:  "fast-syntax-highlighting",
		entry: "fast-syntax-highlighting.plugin.zsh",
		dirs: []string{
			"share/zsh-fast-syntax-highlighting",
			"share/fast-syntax-highlighting",
			"opt/zsh-fast-syntax-highlighting/share/zsh-fast-syntax-highlighting",
		},
	}
	zshPluginSyntax = zshZlePlugin{
		name:  "zsh-syntax-highlighting",
		entry: "zsh-syntax-highlighting.zsh",
		dirs: []string{
			"share/zsh-syntax-highlighting",
			"opt/zsh-syntax-highlighting/share/zsh-syntax-highlighting",
			"share/zsh/plugins/zsh-syntax-highlighting",
		},
	}
	zshPluginAutosuggestions = zshZlePlugin{
		name:  "zsh-autosuggestions",
		entry: "zsh-autosuggestions.zsh",
		dirs: []string{
			"share/zsh-autosuggestions",
			"opt/zsh-autosuggestions/share/zsh-autosuggestions",
			"share/zsh/plugins/zsh-autosuggestions",
		},
	}
)

// zshPluginsEnv names the directory scripts/install-zsh-zle-plugins.sh puts
// its PINNED checkouts in. It is consulted first and it is what CI sets: the
// pins are the measurement, and a Homebrew upgrade must not silently change
// what this test proves.
const zshPluginsEnv = "NOCX_ZSH_PLUGINS"

// zshPluginRoots is where one plugin may be, most authoritative first: the
// pinned checkout, then Homebrew's two prefixes, then the system prefixes
// Debian and Ubuntu use for the two plugins they package.
func zshPluginRoots(p zshZlePlugin) []string {
	var roots []string
	if dir := os.Getenv(zshPluginsEnv); dir != "" {
		roots = append(roots, filepath.Join(dir, p.name))
	}
	for _, prefix := range []string{os.Getenv("HOMEBREW_PREFIX"), "/opt/homebrew", "/usr/local", "/usr"} {
		if prefix == "" {
			continue
		}
		for _, d := range p.dirs {
			roots = append(roots, filepath.Join(prefix, d))
		}
	}
	return roots
}

// requireZshZlePlugin resolves one plugin's entry file and FAILS — never
// skips — when it is absent.
func requireZshZlePlugin(t *testing.T, p zshZlePlugin) string {
	t.Helper()
	for _, root := range zshPluginRoots(p) {
		path := filepath.Join(root, p.entry)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	t.Fatalf("%s is required by this test and is not installed.\n"+
		"A skip here would report an ordinary zsh setup as integrated on every\n"+
		"machine that has none. Provision the PINNED checkouts and re-run:\n"+
		"  scripts/install-zsh-zle-plugins.sh <dir>\n"+
		"  NOCX_ZSH_PLUGINS=<dir> go test -race -count=1 -run '%s' ./internal/shellintegration/\n"+
		"On macOS the same three are Homebrew formulae, for a drift-tolerant\n"+
		"local install: brew install zsh-fast-syntax-highlighting zsh-syntax-highlighting zsh-autosuggestions",
		p.name, t.Name())
	return ""
}

// writePluginFixtureHome materialises a fixture $HOME whose startup files load
// all three plugins in ONE order, chosen by the phase they are sourced in:
//
//   - ".zshrc" is BEFORE nocx. The launcher replays the user's startup phases
//     and installs nocx last, so this is where a plugin lands for everybody who
//     sources it the usual way: underneath nocx's own wrapper, which must call
//     through to it.
//   - ".zlogin" is AFTER nocx. zsh runs /etc/zlogin and ~/.zlogin after the
//     .zshrc phase, and nocx's hooks exist by then, so this is the HARSHER
//     direction: the plugin re-registers `accept-line` over nocx's and nocx's
//     wrapper survives only by being called through. No nocx-shaped hook is
//     involved — it is a plain user startup phase, which is what an ordinary
//     setup has.
//
// The body also PRINTS what it found, because the assertions below are worth
// nothing on a fixture whose sourcing failed: MARKER is the activation marker
// as the user's own startup file sees it, WRAPPED is the widget that really
// implements `accept-line` at that moment, and PLUGINS_LOADED names the order.
// Anything but a `_zsh_highlight_widget_*` wrapper means the plugin did not
// bind, and the test says so instead of passing.
func writePluginFixtureHome(t *testing.T, file, order string) string {
	t.Helper()
	body := strings.Join([]string{
		"# An ordinary zsh setup: the widget-wrapping plugins most people have.",
		`print -r -- "MARKER=${NOCX_SHELL_INTEGRATION:-unset}"`,
		". " + ShellQuote(requireZshZlePlugin(t, zshPluginFastSyntax)),
		". " + ShellQuote(requireZshZlePlugin(t, zshPluginSyntax)),
		". " + ShellQuote(requireZshZlePlugin(t, zshPluginAutosuggestions)),
		`print -r -- "WRAPPED=${widgets[accept-line]-none}"`,
		`print -r -- "PLUGINS_LOADED=` + order + `"`,
		"",
	}, "\n")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, file), []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", file, err)
	}
	return home
}

// TestLocalZshPane_ThePluginStackIntegratesInEitherOrder is nocx-1ebrs.27's
// acceptance, driven through the launch the product makes: a REAL zsh on a
// REAL pty, a REAL lifecycle channel on the inherited descriptor, and the
// three widget-wrapping plugins loaded before nocx and after it.
//
// What it watches a user do: open a tab on a machine whose zsh loads the
// plugins almost everybody loads, and get the pane they expected — a session
// that reports itself integrated, and a command that becomes a block —
// whichever line of their startup files sourced the plugins.
//
// The three things it asserts for each order are the three the bead names: the
// LIFECYCLE HELLO ARRIVES (hello and prompt_ready, the frames the integration
// badge waits on), a COMMAND BECOMES A BLOCK (start and complete on the channel,
// OSC 133 A on the render, the command's own output after it), and the
// nocx-1ebrs.8 DELIVERY still holds (nothing typed, no descriptor path in the
// transcript, the activation marker in the startup files' environment, and the
// OSC 636 session hello the command snapshot rides on).
func TestLocalZshPane_ThePluginStackIntegratesInEitherOrder(t *testing.T) {
	zsh := requireIntegrationShell(t, "zsh")

	for _, tc := range []struct {
		name  string
		file  string
		order string
	}{
		{name: "plugins-before-nocx", file: ".zshrc", order: "BEFORE"},
		{name: "plugins-after-nocx", file: ".zlogin", order: "AFTER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := writePluginFixtureHome(t, tc.file, tc.order)
			unsetZDOTDIR(t)

			s := startLocalPane(t, zsh, ShellZsh, home, localTestOpts())

			// The fixture really is the case this test names.
			s.run(`echo "PLUGIN_ORDER_DONE"`)
			out := strings.ReplaceAll(s.output(), "\r", "")
			if !strings.Contains(out, "PLUGINS_LOADED="+tc.order) {
				t.Fatalf("the fixture never reached its plugins (%s order): %q", tc.order, out)
			}
			if !strings.Contains(out, "WRAPPED=user:_zsh_highlight_widget_") {
				t.Fatalf("fast-syntax-highlighting did not bind `accept-line`, so this test is not exercising a wrapping plugin (%s order): %q",
					tc.order, out)
			}

			// 1. The lifecycle hello arrives, and the shell is at a ready
			//    prompt: the two frames a pane's integration badge waits on.
			if s.kernel.count("hello") == 0 {
				t.Fatalf("the lifecycle hello never arrived; the pane waits on its integration badge forever. accepted=%v output=%q",
					s.kernel.events(), out)
			}
			if s.kernel.count("prompt_ready") == 0 {
				t.Fatalf("no prompt_ready after the hello; accepted=%v output=%q", s.kernel.events(), out)
			}

			// 2. A command becomes a block.
			if s.kernel.count("start") == 0 || s.kernel.count("complete") == 0 {
				t.Fatalf("the command produced no start/complete pair, so no block is drawn; accepted=%v output=%q",
					s.kernel.events(), out)
			}
			if !strings.Contains(out, "\x1b]133;A") {
				t.Errorf("no OSC 133 prompt marker — the session is not integrated: %q", out)
			}
			if !strings.Contains(out, "PLUGIN_ORDER_DONE") {
				t.Errorf("the command's own output never rendered: %q", out)
			}

			// 3. The nocx-1ebrs.8 delivery, unchanged by any of this.
			if n := strings.Count(out, "/dev/fd/"); n > 0 {
				t.Errorf("a descriptor path is printed in the user's transcript %d time(s), so the delivery still has something to type: %q", n, out)
			}
			if !strings.Contains(out, "MARKER=1") {
				t.Errorf("the activation marker was not in the environment of the shell's startup files: %q", out)
			}
			if !strings.Contains(out, "\x1b]636;H;") {
				t.Errorf("no OSC 636 session hello — the renderer would never accept a command snapshot: %q", out)
			}
		})
	}
}
