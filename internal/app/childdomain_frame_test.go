package app

// The grant's bootstrap must fit in a lifecycle frame — nocx-beib.
//
// The composed child line travels as the grant's opaque bootstrap, and a
// frame is bounded by max_frame (lifecycle.MaxFrameBytes, 64 KiB). A line
// that exceeds it is not truncated and not refused: the frame is simply
// never delivered, the parent waits out its 5-second grant timeout, loses
// the channel and runs the command conventionally — five seconds of nothing
// with no diagnostic. The publishing launcher embeds the whole bundle and
// measures ~77 KiB, which is exactly how this was found — and why the bound
// is now 256 KiB (lifecycle.MaxFrameBytes) rather than 64.
import (
	"encoding/hex"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/shellintegration"
)

func TestSSHChildBootstrap_FitsInALifecycleFrame(t *testing.T) {
	cap64 := hex.EncodeToString(make([]byte, 32))
	cmd, _, ok := shellintegration.FullBootstrapCommand(
		shellintegration.ShellAuto,
		shellintegration.LaunchOptions{
			SessionID: "aabbccddeeff00112233445566778899", Enhanced: true,
			Lane: "lane-0123456789abcdef", Domain: "dom-0123456789abcdef", Epoch: 1,
			LifecyclePort: 40123, Capability: cap64, Recovery: cap64,
		})
	if !ok {
		t.Fatal("launcher declined the transient start command")
	}
	line := composeSSHChildLine(cmd, 40123, 37777, lifecyclepub.GrantRequest{
		Env: "ssh", Host: "some.rather.long.hostname.example.com", User: "a-long-user-name", Port: 22022,
	})
	if len(line) >= lifecycle.MaxFrameBytes {
		t.Errorf("composed child line is %d bytes, which does not fit a %d-byte frame: "+
			"the grant is never delivered and the parent hangs until its grant timeout",
			len(line), lifecycle.MaxFrameBytes)
	}
}
