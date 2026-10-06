package runner

import "embed"

//go:embed all:bin/linux-amd64
var runnerFS embed.FS
