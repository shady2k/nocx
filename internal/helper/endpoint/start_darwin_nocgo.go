//go:build darwin && !cgo

package endpoint

// Darwin's production launcher requires libSystem. Fail at build time instead
// of shipping a coordinator that cannot disclaim helper responsibility.
var _ Darwin_helper_spawn_requires_CGO_ENABLED_1
