package shellintegration

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The carrier's acceptance criteria, stated as assertions (design §11,
// assertions 1, 2, 3 and 7's command surface). The loader's SHELL behaviour
// is asserted separately, as shell, in carrier_exec_test.go.

// carrierOpts is a fully-populated LaunchOptions: every addressing field
// set, and both secrets set to a taint canary. Everything a real session
// carries is present, so a size or grammar assertion measures the worst
// case rather than a convenient one.
func carrierOpts() LaunchOptions {
	return LaunchOptions{
		// The shapes the product actually generates — a 32-hex session id
		// (shellintegration.newSessionID), `lane-` and `dom-` plus 16 hex
		// (lifecyclechannel, lifecycle.Kernel) — with the epoch and the
		// port at their type maxima, so a size assertion measures the
		// worst case rather than a convenient one.
		SessionID:     "0123456789abcdef0123456789abcdef",
		Enhanced:      true,
		Lane:          "lane-0123456789abcdef",
		Domain:        "dom-0123456789abcdef",
		Epoch:         18446744073709551615,
		LifecyclePort: 65535,
		StageDigest:   strings.Repeat("ab", 32),
		Capability:    canaryCapability,
		Recovery:      canaryRecovery,
	}
}

// The taint canaries. They are not hex, so a canary that survives some
// encoding step is still recognisable, and they are long enough that an
// accidental substring match is not credible.
const (
	canaryCapability = "CANARY-CAPABILITY-a7f3c9d1e5b28406-DO-NOT-SHIP"
	canaryRecovery   = "CANARY-RECOVERY-b8e4da02f6c39517-DO-NOT-SHIP"
)

// carrierKinds is every ShellKind the product can hand the launcher. The
// unmapped-kind tripwire is asserted separately.
var carrierKinds = []ShellKind{ShellAuto, ShellBash, ShellZsh, ShellUnknown}

// ---------------------------------------------------------------------------
// Assertion 1 — grammar, not only length
// ---------------------------------------------------------------------------

// carrierScriptTokens is the ALLOWLIST: every word that may appear inside
// the loader script. Written out rather than derived from the script, which
// is the whole point — a derived list would accept whatever we emit, and
// the assertion exists to refuse a payload smuggled in as data. A base64 or
// hex blob short enough to fit 1 KiB still fails here, because its tokens
// are not on this list.
var carrierScriptTokens = map[string]bool{
	// shell keywords and builtins
	"exec": true, "read": true, "trap": true, "printf": true,
	// external commands, and the path components they are named through
	"stty": true, "rm": true, "mktemp": true, "dd": true, "wc": true,
	"sha256sum": true, "shasum": true, "bin": true, "sh": true,
	"dev": true, "null": true, "fd": true, "tmp": true,
	// option letters and their values
	"g": true, "raw": true, "echo": true, "l": true, "a": true,
	"bs": true, "count": true, "c": true, "n": true, "f": true,
	"r": true, "gt": true, "le": true, "eq": true, "s": true,
	// the loader's own function and variables
	"R": true, "T": true, "F": true, "H": true,
	"X": true, "L": true, "SHELL": true, "TMPDIR": true,
	// signals
	"HUP": true, "INT": true, "QUIT": true, "TERM": true,
	// the protocol vocabulary
	"NOCX1": true, "OUTCOME": true, "LOADER_READY": true,
	"nocx.XXXXXX": true,
	// the closed set of terminal outcomes, as their wire tokens
	"termios": true, "interrupted": true, "protocol": true,
	"too-large": true, "no-temp": true, "no-digest": true,
	"bad-digest": true, "no-fd": true, "no-source": true,
	// numeric literals: descriptor numbers and redirections, the frame
	// sequence, the digest tool's algorithm argument, the frame cap
	"0": true, "1": true, "2": true, "7": true, "9": true,
	"17": true, "256": true, "32768": true,
}

// carrierTokenRE is how the script is broken into words for the allowlist
// check: a maximal run of identifier characters, which is exactly the shape
// an encoded payload would appear as.
var carrierTokenRE = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_.-]*`)

// carrierArgRE is the per-position grammar of the addressing arguments. The
// digest is the only long one, and it is 64 lowercase hex characters or
// empty — never arbitrary bytes.
var carrierArgRE = []*regexp.Regexp{
	regexp.MustCompile(`^[A-Za-z0-9._:-]{0,64}$`), // session id
	regexp.MustCompile(`^[A-Za-z0-9._:-]{0,64}$`), // lane
	regexp.MustCompile(`^[A-Za-z0-9._:-]{0,64}$`), // domain
	regexp.MustCompile(`^[0-9]{1,20}$`),           // epoch
	regexp.MustCompile(`^[0-9]{1,5}$`),            // lifecycle port
	regexp.MustCompile(`^[0-9]$`),                 // stage descriptor
	regexp.MustCompile(`^(|[0-9a-f]{64})$`),       // stage-1 digest
}

const carrierPrefix = `/usr/bin/env -u BASH_ENV /bin/sh -c '`

// assertCarrierGrammar checks the emitted command against the allowlist
// grammar: the fixed prefix, a single-quoted script whose every token is on
// the allowlist, and exactly the declared addressing arguments, each
// matching its position's pattern.
func assertCarrierGrammar(t *testing.T, cmd string) {
	t.Helper()
	if !strings.HasPrefix(cmd, carrierPrefix) {
		t.Fatalf("command does not begin with the pinned prefix %q:\n%s", carrierPrefix, cmd)
	}
	rest := cmd[len(carrierPrefix):]
	end := strings.Index(rest, "'")
	if end < 0 {
		t.Fatalf("the script region is not closed by a single quote:\n%s", cmd)
	}
	script, tail := rest[:end], rest[end+1:]

	if strings.ContainsAny(script, "'\n") {
		t.Errorf("the script region must be one physical line with no single quote")
	}
	for _, tok := range carrierTokenRE.FindAllString(script, -1) {
		if !carrierScriptTokens[tok] {
			t.Errorf("token %q in the loader script is not on the allowlist — "+
				"either it is a payload, or the allowlist needs a deliberate entry", tok)
		}
		if len(tok) > 26 {
			t.Errorf("token %q is %d bytes; nothing in the loader is longer than "+
				"its longest outcome name (26)", tok, len(tok))
		}
	}

	args, ok := carrierArgs(tail)
	if !ok {
		t.Fatalf("the argument region is not `nocx-loader` followed by quoted words: %q", tail)
	}
	if len(args) != len(carrierArgRE) {
		t.Fatalf("command carries %d addressing arguments, want %d: %q", len(args), len(carrierArgRE), args)
	}
	for i, re := range carrierArgRE {
		if !re.MatchString(args[i]) {
			t.Errorf("addressing argument %d = %q does not match %s", i, args[i], re)
		}
	}
}

// carrierArgs splits the argument region — ` nocx-loader 'a' 'b' …` — into
// its quoted words. It is deliberately strict: anything that is not the
// literal name followed by single-quoted words with no embedded quote is a
// grammar failure, not something to parse around.
func carrierArgs(tail string) ([]string, bool) {
	const name = " nocx-loader"
	if !strings.HasPrefix(tail, name) {
		return nil, false
	}
	rest := tail[len(name):]
	var args []string
	for rest != "" {
		if !strings.HasPrefix(rest, " '") {
			return nil, false
		}
		rest = rest[2:]
		i := strings.Index(rest, "'")
		if i < 0 {
			return nil, false
		}
		args = append(args, rest[:i])
		rest = rest[i+1:]
	}
	return args, true
}

func TestCarrier_GrammarAndSizeForEveryShellKind(t *testing.T) {
	for _, kind := range carrierKinds {
		t.Run(string(kind), func(t *testing.T) {
			cmd, reason, ok := NewRemoteLauncher().StartCommand(kind, carrierOpts())
			if !ok {
				t.Fatalf("carrier refused for %s: reason=%q", kind, reason)
			}
			if reason != ReasonNone {
				t.Errorf("reason = %q, want none", reason)
			}
			if len(cmd) >= MaxCarrierLen {
				t.Errorf("command is %d bytes, cap is %d", len(cmd), MaxCarrierLen)
			}
			assertCarrierGrammar(t, cmd)
			t.Logf("%s: %d bytes", kind, len(cmd))
		})
	}
}

// TestCarrier_IsUnconditional pins §4.1's decision: the command tests
// nothing about the far side's installation state. The guard it replaces —
// `[ -x "$HOME/.nocx/launch" ]` first — loses a race against the concurrent
// publish, so the session degraded while the publish succeeded.
func TestCarrier_IsUnconditional(t *testing.T) {
	for _, kind := range carrierKinds {
		cmd, _, ok := NewRemoteLauncher().StartCommand(kind, carrierOpts())
		if !ok {
			t.Fatalf("carrier refused for %s", kind)
		}
		for _, forbidden := range []string{".nocx", "launch", "-x ", "manifest"} {
			if strings.Contains(cmd, forbidden) {
				t.Errorf("%s: the command mentions %q — it must not test far-side "+
					"installation state:\n%s", kind, forbidden, cmd)
			}
		}
	}
}

// TestCarrier_NeverEmitsStageReady: LOADER_READY is the loader's, STAGE_READY
// is stage-1's, and the backend must always know which one it received.
func TestCarrier_NeverEmitsStageReady(t *testing.T) {
	cmd, _, _ := NewRemoteLauncher().StartCommand(ShellAuto, carrierOpts())
	if !strings.Contains(cmd, LoaderReadyToken) {
		t.Errorf("the command never emits %q:\n%s", LoaderReadyToken, cmd)
	}
	if strings.Contains(cmd, StageReadyToken) {
		t.Errorf("the loader emits %q, which belongs to stage-1:\n%s", StageReadyToken, cmd)
	}
}

// ---------------------------------------------------------------------------
// Assertion 7 (command surface) — the taint canary
// ---------------------------------------------------------------------------

func TestCarrier_SecretsAppearNowhereInTheCommand(t *testing.T) {
	for _, kind := range carrierKinds {
		cmd, _, ok := NewRemoteLauncher().StartCommand(kind, carrierOpts())
		if !ok {
			t.Fatalf("carrier refused for %s", kind)
		}
		if strings.Contains(cmd, canaryCapability) {
			t.Errorf("%s: the capability canary appears in the emitted command", kind)
		}
		if strings.Contains(cmd, canaryRecovery) {
			t.Errorf("%s: the recovery-fence canary appears in the emitted command", kind)
		}
		// Not only verbatim: no fragment long enough to reconstruct either
		// secret may survive, which is what an encoding step would leave.
		for _, frag := range []string{"a7f3c9d1e5b28406", "b8e4da02f6c39517", "CANARY"} {
			if strings.Contains(cmd, frag) {
				t.Errorf("%s: fragment %q of a secret appears in the emitted command", kind, frag)
			}
		}
	}
}

// TestCarrier_TheCanaryDetectorCanFail is the contrast that makes the
// assertion above evidence rather than a tautology: a canary check that
// cannot fail proves nothing about the command it passes on.
//
// It used to take that contrast from the launcher this carrier replaced,
// which really did put both secrets in the command — 92,204 bytes of it, both
// bearers verbatim. That launcher is gone from the repository (ADR-0035, P4),
// so the contrast is stated directly instead: the same detector, run over a
// command shaped like the old one, still fires. What is lost with the old
// code is only that the sample is now a fixture rather than a live artefact;
// what the assertion above needs from it — "this detector distinguishes" —
// is unchanged.
func TestCarrier_TheCanaryDetectorCanFail(t *testing.T) {
	opts := carrierOpts()
	// The shape the retired launcher emitted: an rcfile substituted into
	// the command text, with both bearers inside it.
	asItWas := "/usr/bin/env -u BASH_ENV /bin/sh -c 'NOCX_CAPABILITY=" + opts.Capability +
		"; NOCX_RECOVERY=" + opts.Recovery + "; exec bash --rcfile /dev/fd/3 -i'"
	if !strings.Contains(asItWas, canaryCapability) {
		t.Error("the detector does not find a capability that is plainly there")
	}
	if !strings.Contains(asItWas, canaryRecovery) {
		t.Error("the detector does not find a recovery fence that is plainly there")
	}
}

// ---------------------------------------------------------------------------
// Assertion 2 — a recorder that stores the exec request as one field
// ---------------------------------------------------------------------------

// execRequestRecorder is the shape of the contract we are stating, not a
// mock of any particular product: a consumer of an SSH exec request has to
// serialize the command as ONE field of ONE record, so it carries the whole
// value or none of it. Ours therefore has a per-field limit and refuses
// anything past it, which is what a component with such a limit does.
type execRequestRecorder struct {
	limit    int
	accepted []string
}

func (r *execRequestRecorder) record(execRequest string) error {
	if len(execRequest) > r.limit {
		return fmt.Errorf("exec request field is %d bytes, limit is %d", len(execRequest), r.limit)
	}
	r.accepted = append(r.accepted, execRequest)
	return nil
}

func TestCarrier_FitsARecorderThatTodaysCommandDoesNot(t *testing.T) {
	rec := &execRequestRecorder{limit: MaxCarrierLen}
	for _, kind := range carrierKinds {
		cmd, _, ok := NewRemoteLauncher().StartCommand(kind, carrierOpts())
		if !ok {
			t.Fatalf("carrier refused for %s", kind)
		}
		if err := rec.record(cmd); err != nil {
			t.Errorf("%s: the recorder refused the carrier: %v", kind, err)
		}
	}
	if len(rec.accepted) != len(carrierKinds) {
		t.Errorf("recorder accepted %d commands, want %d", len(rec.accepted), len(carrierKinds))
	}

	// And the contrast, without which the assertion above is satisfied by
	// any recorder with a generous limit: the SAME recorder refuses a
	// command of the size this design retired. 92,204 bytes is the measured
	// ShellAuto form of the self-installing launcher, taken from ADR-0035
	// before it was deleted; the number is kept here because it is what
	// makes the recorder's limit mean something.
	const retiredCommandBytes = 92_204
	if err := rec.record(strings.Repeat("x", retiredCommandBytes)); err == nil {
		t.Errorf("the recorder accepted a %d-byte command; it must refuse it", retiredCommandBytes)
	} else {
		t.Logf("the command this design retired: %d bytes — %v", retiredCommandBytes, err)
	}
}

// ---------------------------------------------------------------------------
// The tripwire survives the change of what StartCommand returns
// ---------------------------------------------------------------------------

func TestCarrier_UnmappedShellKindStillRefused(t *testing.T) {
	cmd, reason, ok := NewRemoteLauncher().StartCommand(ShellKind("fish"), carrierOpts())
	if ok {
		t.Fatalf("unmapped kind accepted; got command %q", cmd)
	}
	if reason != ReasonUnsupportedShell {
		t.Errorf("reason = %q, want %q", reason, ReasonUnsupportedShell)
	}
	if cmd != "" {
		t.Errorf("command = %q, want empty", cmd)
	}
}

// TestCarrier_EnhancedRequiresSessionID keeps the pinned precondition: a
// marker-only session with no id cannot anchor the ownership protocol, so
// the carrier fails closed rather than emitting one that half-works.
func TestCarrier_EnhancedRequiresSessionID(t *testing.T) {
	opts := carrierOpts()
	opts.SessionID = ""
	cmd, reason, ok := NewRemoteLauncher().StartCommand(ShellAuto, opts)
	if ok {
		t.Fatalf("Enhanced with no SessionID accepted; got %q", cmd)
	}
	if reason != ReasonUnsupportedShell {
		t.Errorf("reason = %q, want %q", reason, ReasonUnsupportedShell)
	}
}

// ---------------------------------------------------------------------------
// The frame protocol, as the Go writer will meet it
// ---------------------------------------------------------------------------

func TestFrameHeader_RoundTripsThroughTheLoadersParse(t *testing.T) {
	h, err := FrameHeader(FrameStageSeq, 1234)
	if err != nil {
		t.Fatalf("FrameHeader: %v", err)
	}
	if h != "NOCX1 1     1234\n" {
		t.Errorf("header = %q, want %q", h, "NOCX1 1     1234\n")
	}
	if len(h) != FrameHeaderLen {
		t.Errorf("header is %d bytes, want the fixed %d", len(h), FrameHeaderLen)
	}
	if _, err := FrameHeader(FrameStageSeq, MaxStageFrameLen+1); err == nil {
		t.Error("a stage frame past the cap must be refused by the writer too")
	}
	if _, err := FrameHeader(FrameSecretSeq, MaxSecretFrameLen+1); err == nil {
		t.Error("a secret frame past its own cap must be refused")
	}
	if _, err := FrameHeader(7, 1); err == nil {
		t.Error("an unknown frame sequence must be refused")
	}
}

func TestStageDigest_IsLowercaseHexOfTheFrameBytes(t *testing.T) {
	got := StageDigest([]byte("stage-1 payload"))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got) {
		t.Fatalf("digest %q is not 64 lowercase hex characters", got)
	}
	if StageDigest([]byte("stage-1 payloae")) == got {
		t.Error("the digest does not depend on the bytes")
	}
}

// ---------------------------------------------------------------------------
// The bound is watched, not merely declared
// ---------------------------------------------------------------------------

// TestCarrier_MarginAgainstTheStatedBound watches the GAP, not the ceiling.
// The repo has been here before: maxFullLauncherLen was measured at 98.2%
// full with nobody noticing, because the assertion was "under the cap" and
// erosion is silent until the day a line of shell cannot be added. A margin
// assertion reports it while there is still room to act.
//
// carrierOpts is the worst case the product can generate — the id shapes it
// actually mints, with the epoch and the port at their type maxima — so this
// is the real headroom, not a convenient one.
func TestCarrier_MarginAgainstTheStatedBound(t *testing.T) {
	const wantMargin = 64
	for _, kind := range carrierKinds {
		cmd, _, ok := NewRemoteLauncher().StartCommand(kind, carrierOpts())
		if !ok {
			t.Fatalf("carrier refused for %s", kind)
		}
		if margin := MaxCarrierLen - len(cmd); margin < wantMargin {
			t.Errorf("%s: command is %d bytes, leaving %d of %d — under the %d-byte "+
				"margin. Either shorten the loader or move the bound deliberately; "+
				"do not let the gap close quietly.",
				kind, len(cmd), margin, MaxCarrierLen, wantMargin)
		} else {
			t.Logf("%s: %d bytes, %d spare", kind, len(cmd), margin)
		}
	}
}

// TestCarrier_RefusesRatherThanExceedTheBound: the bound is a contract, so
// an addressing value long enough to breach it refuses the whole command. It
// is never truncated into a command that means something else.
func TestCarrier_RefusesRatherThanExceedTheBound(t *testing.T) {
	opts := carrierOpts()
	opts.SessionID = strings.Repeat("x", MaxCarrierLen)
	cmd, reason, ok := NewRemoteLauncher().StartCommand(ShellAuto, opts)
	if ok {
		t.Fatalf("a %d-byte command was emitted; the bound must refuse it", len(cmd))
	}
	if reason == ReasonNone {
		t.Error("the refusal carries no reason; a degrade must stay visible in the product")
	}
	if cmd != "" {
		t.Errorf("command = %q, want empty", cmd)
	}
}

// TestCarrier_MalformedStageDigestDoesNotTravel: anything that is not 64
// lowercase hex characters is normalised to empty rather than embedded, and
// the loader then names stage-digest-mismatch — the correct answer to "there
// is nothing to verify against", with no new vocabulary invented for it.
func TestCarrier_MalformedStageDigestDoesNotTravel(t *testing.T) {
	for _, bad := range []string{"", "not-a-digest", strings.Repeat("AB", 32), strings.Repeat("ab", 33)} {
		opts := carrierOpts()
		opts.StageDigest = bad
		cmd, _, ok := NewRemoteLauncher().StartCommand(ShellAuto, opts)
		if !ok {
			t.Fatalf("carrier refused for digest %q", bad)
		}
		if bad != "" && strings.Contains(cmd, bad) {
			t.Errorf("malformed digest %q travelled in the command", bad)
		}
		assertCarrierGrammar(t, cmd)
	}
}

// TestOutcomeTokens_AreATotalBijection: every outcome has a token, every
// token maps back, no two share one — and every member is named by the
// component that owns it. A missing entry would make a real refusal
// unreadable on the wire; a member named by nobody would be a refusal class
// nothing can produce.
//
// The three groups are the three places an outcome is decided, and the test
// asserts membership rather than accepting the union: the loader's are in the
// COMMAND, stage-1's are in a frame, and the backend's are named by the writer
// alone because a portable shell may not hold a timer (design §7). A stage-1
// outcome appearing in the loader would mean the command had grown a
// responsibility that belongs in a frame.
func TestOutcomeTokens_AreATotalBijection(t *testing.T) {
	loaderOutcomes := []Outcome{
		OutcomeLoaderTermiosUnavailable, OutcomeBootstrapInterrupted,
		OutcomeBootstrapProtocol, OutcomeStageTooLarge, OutcomeNoSecureTemp,
		OutcomeStageDigestUnavailable, OutcomeStageDigestMismatch,
		OutcomeStageFDUnavailable, OutcomeStageSourceFailed,
	}
	// Shared with the loader by design: stage-1 reads a frame with the same
	// protocol and creates a temp file with the same discipline, so the same
	// three facts have the same three names.
	stageOutcomes := []Outcome{
		OutcomeBootstrapInterrupted, OutcomeBootstrapProtocol, OutcomeNoSecureTemp,
		OutcomeSecretTooLarge, OutcomeSecretMalformed, OutcomeSecretNotForThisSession,
		OutcomeCapabilityFDUnavailable, OutcomeCapabilityUnlinkFailed,
		OutcomeCapabilityWriteFailed, OutcomeGenerationUnavailable,
	}
	// Named by the launch carrier, which is the only component that knows
	// whether the generation it is about to exec still proves out.
	carrierOutcomes := []Outcome{OutcomeBootstrapAccepted, OutcomeGenerationUnavailable}
	backendOutcomes := []Outcome{
		OutcomeReceiverUnready, OutcomeBootstrapTimeout, OutcomeChannelUnavailable,
		// §6.1's rule 1: a repeated or out-of-order token. Named by the
		// backend for the same reason the two deadlines are — the far side
		// emits each of its tokens once by construction, so only the
		// writer can observe a second one.
		OutcomeBootstrapOutOfOrder,
	}

	all := map[Outcome]bool{}
	for _, group := range [][]Outcome{loaderOutcomes, stageOutcomes, carrierOutcomes, backendOutcomes} {
		for _, o := range group {
			all[o] = true
		}
	}
	if len(all) != len(outcomeTokens) {
		t.Fatalf("%d outcomes listed here, %d in the table — one of them gained a "+
			"member without the other", len(all), len(outcomeTokens))
	}

	seen := map[string]bool{}
	for o := range all {
		tok := OutcomeToken(o)
		if tok == "" {
			t.Errorf("%q has no wire token", o)
			continue
		}
		if seen[tok] {
			t.Errorf("token %q is shared by two outcomes", tok)
		}
		seen[tok] = true
		if back, ok := OutcomeForToken(tok); !ok || back != o {
			t.Errorf("token %q maps back to %q, want %q", tok, back, o)
		}
	}

	stage, err := Stage1Frame(ShellAuto, carrierOpts())
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	texts := map[string]string{
		"loader":         carrierLoader(),
		"stage-1":        string(stage),
		"launch carrier": launchCarrier(),
	}
	named := func(who string, group []Outcome) {
		for _, o := range group {
			if !strings.Contains(texts[who], " "+OutcomeToken(o)) {
				t.Errorf("outcome %q (token %q) is never named by the %s", o, OutcomeToken(o), who)
			}
		}
	}
	named("loader", loaderOutcomes)
	named("stage-1", stageOutcomes)
	named("launch carrier", carrierOutcomes)
	// The backend's are the writer's alone: naming one on the far side
	// would mean a remote timer, which design §7 forbids.
	for _, o := range backendOutcomes {
		for who, text := range texts {
			if strings.Contains(text, " "+OutcomeToken(o)) {
				t.Errorf("backend-only outcome %q is named by the %s", o, who)
			}
		}
	}

	if _, ok := OutcomeForToken("not-ours"); ok {
		t.Error("a token that is not ours was accepted as an outcome")
	}
}

// The reader is built FROM the enumeration, so an enumeration that misses a
// member is a member no session can be refused for in words.
//
// What a user loses if this regresses: the far side names a real outcome on
// the terminal, outcomeInLine cannot resolve its token, the line is dropped as
// "not one of ours", and session.integrationChanged carries no reason at all —
// a session sitting at a native prompt with the product unable to say why.
// That is the silent degrade the whole outcome vocabulary exists to prevent,
// and it is one forgotten table entry away.
func TestOutcomeInLine_ResolvesEveryMemberTheEnumerationDeclares(t *testing.T) {
	outcomes := AllOutcomes()
	if len(outcomes) != len(outcomeTokens) {
		t.Fatalf("AllOutcomes enumerates %d of the table's %d members; the reader is built "+
			"from it and is blind to the rest", len(outcomes), len(outcomeTokens))
	}
	for _, o := range outcomes {
		line := OutcomePrefix + OutcomeToken(o)
		got, ok := outcomeInLine(line)
		if !ok {
			t.Errorf("the far side's line %q reads as no outcome at all; the session would "+
				"be refused with nothing to say", line)
			continue
		}
		if got != o {
			t.Errorf("line %q read as outcome %q, want %q", line, got, o)
		}
		// Twice, because the reader used to answer by ranging a Go map:
		// two members sharing one token resolved to either of them, and
		// the diagnosis a user was shown changed between reads of it.
		if again, _ := outcomeInLine(line); again != got {
			t.Errorf("line %q read as %q and then as %q; the reason a user is shown "+
				"must not depend on when it is read", line, got, again)
		}
	}
}
