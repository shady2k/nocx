package assistant

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var errInvalidToolCallID = errors.New("tool call ID must contain non-whitespace text and at most 128 Unicode characters")

// ValidateToolCallID admits the exact provider identity that can later be
// answered through agent.approve. Adapters validate a whole response before
// executing any sibling call; invocation and approval use this same boundary.
// IDs are never trimmed, truncated or regenerated.
func ValidateToolCallID(id string) error {
	if strings.TrimSpace(id) == "" || utf8.RuneCountInString(id) > 128 {
		return errInvalidToolCallID
	}
	return nil
}
