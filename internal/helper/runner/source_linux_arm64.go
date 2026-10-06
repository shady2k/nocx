package runner

import "embed"

//go:embed all:bin/linux-arm64
var runnerFS embed.FS
