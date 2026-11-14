package pi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/shady2k/nocx/internal/log"
)

func sessionConfigPattern() string {
	return fmt.Sprintf("session-%d-*.json", os.Getpid())
}

// A config can outlive an abruptly killed backend before pi consumes it. Only
// reclaim files whose backend owner is proven dead; live owners may be starting
// a child that has not read its config yet. Unknown names and PID reuse retain
// files conservatively. Sweep once at connector startup, never per incarnation.
func sweepOrphanConfigs(ctx context.Context, dataDir string) {
	dir := filepath.Join(dataDir, "pi", "configs")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.From(ctx).Warn("pi orphan config scan failed", "error", err)
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name, ok := strings.CutPrefix(entry.Name(), "session-")
		if !ok || !strings.HasSuffix(name, ".json") {
			continue
		}
		owner, _, ok := strings.Cut(name, "-")
		pid, err := strconv.Atoi(owner)
		if !ok || err != nil || pid <= 0 {
			continue
		}
		if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.From(ctx).Warn("pi orphan config removal failed", "error", err)
		}
	}
}
