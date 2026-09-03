package client_test

import (
	"io"
	"log/slog"

	"github.com/shady2k/nocx/internal/helper/host"
)

// hostFor builds a helper host over one connection's ends. hostPeer already
// does this for a peer that registers exactly one service and nothing else;
// this is the same construction for a caller that needs the host itself —
// because the session service is bound TO the host (it writes data frames and
// notifications through it), which hostPeer's closed shape has no way to
// express.
func hostFor(in io.Reader, out io.Writer, log *slog.Logger) *host.Host {
	return host.New(in, out, "testhash", "instance-1", log)
}
