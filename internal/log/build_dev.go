//go:build !release

package log

import "log/slog"

// releaseBuild is what the sensitive-value tests assert against, so each
// states the behaviour of the build it was compiled into rather than being
// skipped in one of them.
const releaseBuild = false

// DefaultLevel is the level a build logs at when nothing says otherwise.
//
// A DEV BUILD SAYS EVERYTHING. Every Debug line in this codebase used to be
// unreachable without an environment variable, which meant the person who
// needed one had to know it existed, know its name, and restart the thing they
// were watching. The failure that bought this rule (nocx-4l2a5) was diagnosed
// from a log with a thirty-second hole in it, on a stand that had been running
// for three minutes and could not be asked to say more without being restarted.
//
// A release build keeps info: a person's own machine writes this file all day,
// and per-call lines there are a disk cost with no reader.
func DefaultLevel() slog.Level { return slog.LevelDebug }
