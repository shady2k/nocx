// Package providertest supplies shared HTTP provider fixtures for tests.
package providertest

import (
	"io"
	"net/http"
)

// ConsumeRequestBeforeResponse keeps fake completions behind the same boundary as
// real providers: the complete POST arrives before its answer. net/http does
// not drain it for the handler when Connection: close is set; answering first
// can otherwise close the socket while the client is still writing.
func ConsumeRequestBeforeResponse(w http.ResponseWriter, r *http.Request) bool {
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "could not read provider request\n")
		return false
	}
	return true
}
