package update

import (
	"fmt"
	"sync"
)

// Channel names which signed manifest a check fetches (ADR-0097). Stable
// follows GitHub's `latest` redirect — the behaviour every release before the
// nightly channel had, and the default an install keeps until the person
// opts in. Nightly follows the moving `nightly` prerelease tag the nightly
// workflow republishes each night main moved.
type Channel int

const (
	// ChannelStable is the default: the latest stable release's manifest.
	ChannelStable Channel = iota
	// ChannelNightly is the opt-in: tonight's build, signed by the same key.
	ChannelNightly
)

// String is the wire and settings spelling — the value the persisted setting
// stores and the window binding accepts.
func (c Channel) String() string {
	switch c {
	case ChannelStable:
		return "stable"
	case ChannelNightly:
		return "nightly"
	default:
		return fmt.Sprintf("Channel(%d)", int(c))
	}
}

// ParseChannel is the single reader of that spelling: the settings
// declaration and the window binding both hand strings across a boundary, and
// one parser means an unknown value is refused everywhere the same way.
func ParseChannel(s string) (Channel, error) {
	switch s {
	case "stable":
		return ChannelStable, nil
	case "nightly":
		return ChannelNightly, nil
	default:
		return ChannelStable, fmt.Errorf("unknown update channel %q — stable and nightly are the two there are", s)
	}
}

// The manifest base URLs. Stable rides the `latest` redirect, which excludes
// prereleases by GitHub's own rule, so a nightly publication can never leak
// into a stable check. Nightly rides the moving `nightly` tag, so the URL is
// stable while what it serves changes every night.
const (
	stableManifestBaseURL  = "https://github.com/shady2k/nocx/releases/latest/download"
	nightlyManifestBaseURL = "https://github.com/shady2k/nocx/releases/download/nightly"
)

// ManifestBaseURL is where this channel's manifest.json and manifest.json.sig
// live.
func (c Channel) ManifestBaseURL() string {
	if c == ChannelNightly {
		return nightlyManifestBaseURL
	}
	return stableManifestBaseURL
}

// channelState is the fetcher's channel selection, switchable while checks
// are in flight. The window binding flips it when the person changes the
// setting; the next check — the one the renderer is about to make — fetches
// the channel the store now names.
type channelState struct {
	mu      sync.Mutex
	channel Channel
}

func (s *channelState) get() Channel {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.channel
}

func (s *channelState) set(c Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channel = c
}
