package shellintegration

import _ "embed"

//go:embed scripts/nocx.zsh
var zshScript string

//go:embed scripts/nocx.bash
var bashScript string

// version is the integration script version. Bump when scripts change;
// EnsureInstalled/EnsureInstalledRemote compare this against the installed
// VERSION file and rewrite scripts when they differ.
const version = "6"

// promptModeEnvVar is the env var that selects the prompt mode.
const promptModeEnvVar = "NOCX_PROMPT_MODE"

// promptModeMarkerOnly is the marker-only prompt mode value.
const promptModeMarkerOnly = "marker-only"

// sessionIDEnvVar is the env var for the nocx session identifier.
const sessionIDEnvVar = "NOCX_SESSION_ID"

// dirName is the directory name inside the user's home.
const dirName = ".nocx"

// versionFile is the marker file written alongside the scripts.
const versionFile = "VERSION"

// activationEnvVar is the env var the shell rc gate checks.
const activationEnvVar = "NOCX_SHELL_INTEGRATION"

// gateLineZsh is appended to ~/.zshrc to load the integration.
const gateLineZsh = `# nocx terminal shell integration
[[ -n "$NOCX_SHELL_INTEGRATION" ]] && source "$HOME/.nocx/shell-integration.zsh"`

// gateLineBash is appended to ~/.bashrc to load the integration.
const gateLineBash = `# nocx terminal shell integration
[[ -n "$NOCX_SHELL_INTEGRATION" ]] && source "$HOME/.nocx/shell-integration.bash"`

// scripts maps shell rc filename → embedded script content.
var scripts = map[string]string{
	"shell-integration.zsh":  zshScript,
	"shell-integration.bash": bashScript,
}

// rcGate maps rc filename → gate line to append.
var rcGate = map[string]string{
	".zshrc":  gateLineZsh,
	".bashrc": gateLineBash,
}
