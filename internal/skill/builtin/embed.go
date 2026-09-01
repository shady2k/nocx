// Package builtin holds the skills shipped in the binary. They are our own
// bytes, which is why they are the one provenance exempt from the scan.
package builtin

import "embed"

//go:embed skill-authoring
var files embed.FS

// FS is the builtin root's filesystem, rooted so that each entry is a skill
// directory — the same shape a directory root has.
var FS = files
