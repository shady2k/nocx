//go:build release

package log

import "log/slog"

// releaseBuild — see build_dev.go.
const releaseBuild = true

// DefaultLevel — see build_dev.go. A shipped build logs at info.
func DefaultLevel() slog.Level { return slog.LevelInfo }
