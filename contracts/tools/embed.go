// Package tools is the agent tools' params schemas, embedded into the binary
// so the shipped bundle never depends on a contracts/ tree existing next to
// the process (nocx-jtz3q): the schemas reach the binary or the build is
// broken loudly — an assistant whose registry assembled empty would declare
// no tools to any run and fail silently, which is how a feature that does
// not exist survives a release.
package tools

import "embed"

// Schemas is the contracts/tools directory as an fs.FS: one file per tool's
// params schema, the same files the registry assembles from and the model is
// shown. The embed pattern keeps the set honest — a schema added to the
// directory is embedded, a schema deleted is gone.
//
//go:embed *.json
var Schemas embed.FS
