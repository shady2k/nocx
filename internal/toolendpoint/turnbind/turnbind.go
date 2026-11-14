// Package turnbind owns the explicit frame for a new assistant turn bearer.
package turnbind

import (
	"fmt"
	"io"

	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
)

// TokenEnvVar carries only a turn bearer to its MCP bridge.
const TokenEnvVar = "NOCX_TURN_TOKEN" // #nosec G101 -- environment key, not a credential

// TokenMagic cannot start a JSON request or a legacy pane token.
const TokenMagic = "NXTB1:"

// EncodeToken renders a turn bearer, not a pane's existing wire frame.
func EncodeToken(token string) ([]byte, error) {
	line, err := panebind.EncodeToken(token)
	if err != nil {
		return nil, err
	}
	return append([]byte(TokenMagic), line...), nil
}

// ReadToken reads exactly one turn frame without consuming request bytes.
func ReadToken(r io.Reader) (string, error) {
	magic := make([]byte, len(TokenMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return "", fmt.Errorf("%w: %v", panebind.ErrShortToken, err)
	}
	if string(magic) != TokenMagic {
		return "", panebind.ErrNotAToken
	}
	return panebind.ReadToken(r)
}
