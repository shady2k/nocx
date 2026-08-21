package app

// The grant's bootstrap must fit in a lifecycle frame — nocx-beib.
//
// The composed child line travels as the grant's opaque bootstrap, and a
// frame is bounded by lifecycle.MaxFrameBytes. A line that exceeds it is not
// truncated and not refused: the frame is simply never delivered, the parent
// waits out its 5-second grant timeout, loses the channel and runs the
// command conventionally — five seconds of nothing with no diagnostic. The
// publishing launcher embedded the whole bundle and measured ~77 KiB, which
// is exactly how this was found, and why the bound was raised to 256 KiB.
//
// The line is now the bounded carrier plus the user's own words (ADR-0035),
// so the margin is four orders rather than three — but the assertion stays,
// because what it guards is the composer, and a composer that started
// embedding again would be caught here first.
import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

func TestSSHChildBootstrap_FitsInALifecycleFrame(t *testing.T) {
	carrier, _, ok := shellintegration.NewRemoteLauncher().StartCommand(
		shellintegration.ShellAuto,
		shellintegration.LaunchOptions{
			SessionID: "aabbccddeeff00112233445566778899", Enhanced: true,
			Lane: "lane-0123456789abcdef", Domain: "dom-0123456789abcdef", Epoch: 1,
			LifecyclePort: 40123, StageDigest: strings.Repeat("a", 64),
		})
	if !ok {
		t.Fatal("the carrier declined the transient start command")
	}
	wrap := ssh.TypedWrap{MuxOptions: []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/nocx-mux-1000/m-%C",
		"-o", "ControlPersist=no",
	}}
	inv := ssh.TypedInvocation{
		Host: "some.rather.long.hostname.example.com",
		User: "a-long-user-name",
		Port: 22022,
		Opts: []string{"-i", "/home/a-long-user-name/.ssh/id_ed25519", "-J", "bastion.example.com"},
	}
	line := composeSSHLine(wrap, []string{"-t", "-R", "127.0.0.1:40123:127.0.0.1:37777"}, inv, carrier)
	if len(line) >= lifecycle.MaxFrameBytes {
		t.Errorf("composed child line is %d bytes, which does not fit a %d-byte frame: "+
			"the grant is never delivered and the parent hangs until its grant timeout",
			len(line), lifecycle.MaxFrameBytes)
	}
	t.Logf("composed child line: %d bytes against a %d-byte frame", len(line), lifecycle.MaxFrameBytes)
}
