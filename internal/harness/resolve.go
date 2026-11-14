package harness

import (
	"fmt"
	"os"
	"strings"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/shellenv"
)

// resolveExecutable shares detection's executable resolver. exec.Command must
// receive an already resolved path, since its lookup uses the backend's PATH.
func resolveExecutable(command string, env []string, dir string) (string, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	path, _ := shellenv.Path(env)
	executable, err := agentapproval.IdentityForExecutableInPath(command, path, dir)
	if err != nil {
		return "", fmt.Errorf("harness: resolve agent executable: %w", err)
	}
	return executable.Path, nil
}

// mergeEnvironment gives both executable lookup and exec the same last-value
// overrides, rather than relying on exec's private environment deduplication.
func mergeEnvironment(base, extra []string) []string {
	all := append(append([]string(nil), base...), extra...)
	seen := make(map[string]bool)
	keep := make([]bool, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		name, _, _ := strings.Cut(all[i], "=")
		if !seen[name] {
			seen[name] = true
			keep[i] = true
		}
	}
	out := make([]string, 0, len(all))
	for i, kv := range all {
		if keep[i] {
			out = append(out, kv)
		}
	}
	return out
}
