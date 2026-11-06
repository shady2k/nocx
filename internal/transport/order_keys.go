package transport

import (
	"github.com/shady2k/nocx/internal/operation/catalog"
)

// CANONICAL RESOURCE KEYS (ADR-8eej3e66 section 5).
//
// A key identifies one real resource, across connections: the session, the
// open collection — never the binding, never the frame. The catalogue already
// declares which resources an operation orders on. Two kinds of name fill it
// in:
//
//   - the declaration's own: `store:content`, `collection:workspace-layout`.
//     These are constant, so they are read off the table at dispatch and no
//     method restates them.
//   - the invocation's: `session:<id>`, `collection:<handle>`. The table
//     cannot know them, so the method's key extractor names them from the
//     validated params.
//
// The composition root gives each domain's FIFO the prefixes it owns
// (resourceOrders), so a session key never reaches the content FIFO.
func orderKeysFor(decl catalog.Declaration, runtime []string) []string {
	keys := make([]string, 0, len(runtime)+2)
	seen := make(map[string]bool, len(runtime)+2)
	add := func(key string) {
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}
	for _, r := range decl.Resources() {
		if r.Mode != catalog.ModeKey || r.Name == "" {
			continue
		}
		add(string(r.Kind) + ":" + r.Name)
	}
	for _, key := range runtime {
		add(key)
	}
	return keys
}

// sessionKey names one session's ordering key.
func sessionKey(id string) string {
	if id == "" {
		return ""
	}
	return "session:" + id
}

// collectionKey names one open collection's ordering key. The handle is the
// collection's identity for as long as it is open (apicoll answers one handle
// per folder, whichever connection opened it), so it is the real resource and
// not a per-connection binding.
func collectionKey(handle string) string {
	if handle == "" {
		return ""
	}
	return "collection:" + handle
}
