package runner

import "embed"

//go:embed all:bin/darwin-amd64
var runnerFS embed.FS
