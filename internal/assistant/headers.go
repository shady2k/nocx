package assistant

// One helper for the endpoint's custom headers (bead nocx-lyyk), shared by
// the completion path (engine.go) and the connection check (connection.go):
// both need the canonical header map to PUT on the request AND the canonical
// names to TAG on the context — the guard's redirect rule drops exactly the
// tagged names on an origin change (httpguard.go), so the two must always be
// built together or a header could be sent yet survive a redirect.

import "net/http"

// headerMap canonicalizes the resolved header list into the request header
// map, returning the canonical names in the same order. Canonicalization is
// the single point that makes storage, request and redirect-tag agree: Go's
// http stack canonicalizes on Set, and the guard deletes by the same
// canonical names.
func headerMap(headers []Header) (map[string]string, []string) {
	if len(headers) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(headers))
	names := make([]string, len(headers))
	for i, h := range headers {
		key := http.CanonicalHeaderKey(h.Name)
		m[key] = h.Value
		names[i] = key
	}
	return m, names
}
