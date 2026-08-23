package shellintegration

import (
	"fmt"
	"strconv"
	"strings"
)

// Stage-1: what the loader sources, and the only thing that ever touches the
// per-epoch capability on the far host (design §5.2, work package P2).
//
// # Why stage-1 exists at all
//
// The remote command is capped at MaxCarrierLen and carries no payload and no
// secret (carrier.go). Everything the secret delivery needs — read a
// length-framed payload, validate it against this session, mktemp, open two
// descriptors, unlink the name, write, and name a terminal outcome on every
// failure — does not credibly fit in 1 KiB of portable shell, and asserting
// that it does would be the same undescribed transport one level lower. So
// stage-1 is itself a FRAME: it is text on the channel, capped at
// MaxStageFrameLen, and the command commits to its digest.
//
// Being a frame rather than a command has two consequences worth stating,
// because they are what make this file readable where carrier.go cannot be:
// stage-1 may span many physical lines, and it may contain single quotes.
// Neither is true of the loader, which travels single-quoted inside a command
// a csh login shell may parse.
//
// # It runs on the main PTY channel, and it becomes the shell
//
// Ancestry is the whole reason (design §5.1): a separate channel would give a
// clean pipe with no tty discipline and a receiver that is a DIFFERENT child
// of sshd from the interactive shell, so a value could pass between them only
// through the carriers D4 forbids — argv, the environment, a named file.
// Stage-1 is sourced by the loader, so it IS the session's shell, and it hands
// the capability on by the one mechanism that needs no carrier at all: an
// inherited descriptor that survives its exec.
//
// # What it inherits, and what it must not redo
//
// It is sourced, so it inherits the loader's shell: the saved termios in T,
// the cleanup function R, the trap that reaches it, the stage descriptor
// StageFD, and the addressing arguments as positional parameters. Two rules
// follow and neither is a preference:
//
//   - stage-1 NEVER runs `stty -g` again. The loader already changed the
//     terminal, so a second save would record the loader's raw state as if it
//     were the user's, and the restore would leave the terminal raw.
//   - stage-1 NEVER emits LoaderReadyToken. It emits StageReadyToken, so the
//     backend always knows which of the two handshakes it received.
//
// Cleanup EXTENDS the loader's rather than replacing it. Q releases what
// stage-1 itself created — the two capability descriptors and the temp name —
// and then calls R, which stays the sole owner of the termios restore, the
// outcome line and the native login shell. Redefining R here would be a second
// owner of one behaviour; Q is a wrapper, and the trap is re-pointed at it.
//
// # The filesystem ordering is the part that decides whether a secret leaks
//
// Design §5.2 enumerates six failure cases and one success, and the ordering
// they encode is: the temp file's NAME exists only between mktemp and unlink,
// and nothing secret is written in that window. In particular a FAILED UNLINK
// writes nothing at all — the capability never reaches a filesystem object
// that anything could open by name. The success path writes only after the
// name is gone, and BOOTSTRAP_ACCEPTED is emitted only after a successful
// write AND a successful close.
//
// # Why a probe precedes each `exec` redirection
//
// POSIX says a redirection error on `exec` with no command ENDS a
// non-interactive shell. That would end it before any trap could restore the
// terminal, which is exactly the outcome assertion 17 forbids. So each open is
// preceded by a subshell probe — `( : <"$G" )` — whose failure is an ordinary
// non-zero status the `||` arm catches. The residual window is a file that
// becomes unopenable between the probe and the exec, which is not one of the
// enumerated cases; the EXIT trap is the backstop for it, and it is why the
// trap list includes EXIT at all.
const (
	// CapabilityFD is the descriptor stage-1 leaves open across its exec:
	// the read end of the unlinked file holding the capability and the
	// fence. Its NUMBER is not a secret — it names nothing and
	// authenticates nobody — so it travels to the tiers in the
	// environment as CapabilityFDEnv.
	//
	// Design §5.2 says "its number travels in argv". The environment is
	// used instead for one reason, and it is a compatibility one: the
	// thing stage-1 execs is the INSTALLED launch carrier, whose argv
	// contract ($1 is the session id) belongs to whichever generation is
	// on the far host. A generation published before this change ignores a
	// new positional argument silently, while it passes the whole
	// environment through to the tier it execs. Neither carries the value
	// to an OLD tier's rcfile, which reads neither — but the environment
	// at least cannot be misread as something else. The number is not a
	// secret either way, which is what makes the choice free.
	CapabilityFD = 7
	// capabilityWriteFD is the write end. It is closed before the exec and
	// never travels anywhere.
	capabilityWriteFD = 8

	// CapabilityFDEnv names the descriptor for the tier rcfiles. They read
	// it once, close it, and assign non-exported variables (design §5.2
	// point 7); the variable is unset in the same breath, so no descendant
	// of the user's shell learns the number either.
	CapabilityFDEnv = "NOCX_CAP_FD"
	// BootstrapEnv marks the launch carrier as running under a bootstrap,
	// which is the only condition under which it may emit a terminal
	// outcome. Without it the carrier is silent, so a launch that did not
	// come from stage-1 cannot put protocol tokens on a user's terminal.
	// It is unset before the tier is exec'd.
	BootstrapEnv = "NOCX_BOOTSTRAP"

	// secretFrameSecret and secretFrameRefuse are frame 2's two kinds. A
	// SECRET frame carries the pair; a REFUSE frame carries a reason and
	// no bearer at all — it is what stage-1 receives when the lifecycle
	// channel could not be opened, because §6.1 forbids minting anything
	// that cannot be exercised.
	secretFrameSecret = "SECRET"
	secretFrameRefuse = "REFUSE"
)

// stage1Template is stage-1 itself. Every @TOKEN@ is substituted from a
// constant in this package, so the shell side and the Go side cannot drift.
//
// The order of statements is the contract:
//
//  1. save the addressing arguments, so the frame parse is free to use $1;
//  2. define Q and re-point the trap at it BEFORE anything is created —
//     including EXIT, so a shell-ending redirection error still restores the
//     terminal;
//  3. announce STAGE_READY only once the trap covers everything below it;
//  4. read frame 2 with the same fixed-width header and `dd bs=1 count=N` the
//     loader uses — a shell's `read` builtin may pull a whole buffer off a
//     terminal and hand back one line, which is what makes the reader
//     over-consume — with the stderr of EACH READ, and of nothing else, sent
//     to /dev/null;
//  5. validate the frame against THIS session, domain and epoch;
//
// # Why the silence is per-read here and not the loader's blanket one
//
// dd(1) writes a three-line summary of its record counts to stderr on every
// invocation, and on the far side stderr is the user's terminal: unsilenced,
// two reads put six lines of bookkeeping over the prompt of a bootstrap that
// worked, and this side logs each of them as an unexpected line while it waits
// for the outcome.
//
// The loader answers the same fact with `exec 2>/dev/null` for its whole body
// (carrier.go), and the reason it gives is byte economy — a blanket redirect
// buys the ~120 bytes that its eleven separate redirections would cost. That
// reasoning is the loader's, and applying it HERE reverses the answer, because
// stage-1's facts differ in both terms. Only two of its commands are noisy, so
// the blanket would save about nine bytes rather than a hundred and twenty;
// and stage-1 is a frame with 32 KiB to spend where the loader is a command
// with 1 KiB, so those nine bytes buy nothing that is scarce. Measured: the
// two redirections cost 24 bytes and took the largest rendered frame from
// 1,641 to 1,665 of the 32,768 cap. Nothing published moves — stage-1 travels
// on the channel and is not a bundle file — so publisher_measure_test.go's
// measuredMaxPublishBytes is untouched by this, and that is the ratchet
// agreeing rather than the ratchet being avoided.
//
// What the blanket would cost is the part that is not cosmetic. Stage-1's
// remaining commands are the ones whose failure it must name — mktemp, the two
// probes, rm, the write to the capability descriptor, stty — and their stderr
// is the only DIAGNOSIS a user or an operator gets for why a named outcome
// happened. The outcome line itself is a printf on stdout and no stderr
// redirect could swallow it, but "the outcome is still named" is a lower bar
// than "the reason is still visible", and a blanket clears the first while
// failing the second. A per-read redirect also cannot outlive the reads: it is
// scoped to one command each and needs no restore, so no path out of stage-1 —
// neither Q's nor the exec below — has to remember to undo it in order to hand
// the user's shell a working stderr.
//
// THE BODY IS READ BEFORE ANY FILESYSTEM WORK, and that ordering is
// load-bearing rather than incidental. Every refusal here execs a native login
// shell, and anything still queued on the terminal becomes that shell's typed
// input — so a refusal that happened with the body unread would put the
// CAPABILITY AND THE FENCE into the user's shell as a command, and from there
// into its history. The loader had exactly this shape one refusal earlier and
// it was measured under dash (see carrierLoaderTemplate); here the stakes are
// a bearer rather than a script, so the read comes first and nothing between
// STAGE_READY and it may refuse.
//
//  6. the filesystem sequence of §5.2, unlink before the first write;
//  7. export what the tiers read, restore the exact termios, and exec.
const stage1Template = `_S=$1
_L=$2
_D=$3
_E=$4
_P=$5
G=
C=
W=
A=
Q(){ if [ -n "$W" ]; then exec @CAPW@>&-; W=; fi
if [ -n "$C" ]; then exec @CAPR@<&-; C=; fi
if [ -n "$G" ]; then rm -f "$G"; G=; fi
trap - EXIT
R "$1"
}
trap "Q @INTERRUPTED@" HUP INT QUIT TERM EXIT
printf "@STAGEREADY@\n"
X=$(dd bs=1 count=@HDRLEN@ 2>/dev/null)
[ -n "$X" ] || Q @INTERRUPTED@
[ "${X#@MAGIC@ @SEQ@ }" != "$X" ] || Q @PROTOCOL@
L=${X##* }
[ "$L" -gt 0 ] || Q @PROTOCOL@
[ "$L" -le @MAXSECRET@ ] || Q @SECRETTOOLARGE@
S=$(dd bs=1 count=$L 2>/dev/null)
[ ${#S} -eq $((L - 1)) ] || Q @INTERRUPTED@
NL='
'
H=${S%%"$NL"*}
if [ "$H" = "@MAGIC@ @KSECRET@ $_S $_D $_E" ]; then
case "$S" in *"$NL"*"$NL"*) ;; *) Q @INTERRUPTED@ ;; esac
Y=${S#*"$NL"}
CP=${Y%%"$NL"*}
FN=${Y#*"$NL"}
S=
Y=
case "$CP" in ''|*[!0-9a-f]*) Q @BADSECRET@ ;; esac
case "$FN" in *[!0-9a-f]*) Q @BADSECRET@ ;; esac
G=$(mktemp "${TMPDIR:-/tmp}/nocx.XXXXXX") || Q @NOTEMP@
[ -n "$G" ] || Q @NOTEMP@
( : <"$G" ) || Q @NOCAPFD@
exec @CAPR@<"$G"
C=1
( : >>"$G" ) || Q @NOCAPFD@
exec @CAPW@>"$G"
W=1
rm -f "$G" || Q @NOUNLINK@
[ ! -e "$G" ] || Q @NOUNLINK@
G=
printf "%s\n%s\n" "$CP" "$FN" >&@CAPW@ || Q @NOCAPWRITE@
exec @CAPW@>&-
W=
CP=
FN=
A=1
else
S=
[ "${H#@MAGIC@ @KREFUSE@ }" != "$H" ] || Q @WRONGFRAME@
fi
exec @STAGEFD@<&-
[ -x "$HOME/@LAUNCHPATH@" ] || Q @NOGENERATION@
if [ -n "$A" ]; then
@CAPFDENV@=@CAPR@
NOCX_LIFECYCLE_LANE=$_L
NOCX_LIFECYCLE_DOMAIN=$_D
NOCX_LIFECYCLE_EPOCH=$_E
NOCX_LIFECYCLE_PORT=$_P
export @CAPFDENV@ NOCX_LIFECYCLE_LANE NOCX_LIFECYCLE_DOMAIN NOCX_LIFECYCLE_EPOCH NOCX_LIFECYCLE_PORT
fi
@BOOTENV@=1
export @BOOTENV@
stty "$T"
exec 2>&1
exec "$HOME/@LAUNCHPATH@" "$_S" "@PIN@"
`

// shellPin maps a profile's pinned shell to the token stage-1 hands the
// launch carrier.
//
// This is where a profile pin becomes real again. The carrier is identical
// for every ShellKind — a shell name is not on design §4.1's list of what may
// travel in the command, and there is no room for it — so between the carrier
// landing and this file a user who pinned zsh got whatever the far host's
// $SHELL said. The pin travels in the stage-1 FRAME instead, which is where
// everything that wants to grow belongs, and the launch carrier prefers it
// over its own $SHELL dispatch.
//
// ShellAuto maps to the empty pin, which is not a missing value: it is the
// design's "the far host decides", and the launch carrier then dispatches on
// $SHELL exactly as it does for a session that pinned nothing.
func shellPin(shell ShellKind) (string, bool) {
	switch shell {
	case ShellBash:
		return "bash", true
	case ShellZsh:
		return "zsh", true
	case ShellUnknown:
		return "unknown", true
	case ShellAuto:
		return "", true
	default:
		return "", false
	}
}

// Stage1Frame renders frame 1 for one session: the payload the sender writes
// and whose digest the carrier commits to (StageDigest).
//
// It refuses rather than truncates, in both directions: an unmapped ShellKind
// is a decision and not a fallback, and a payload past MaxStageFrameLen is a
// frame the far side would refuse anyway — building it here would only move
// the failure to a place with less context.
func Stage1Frame(shell ShellKind, opts LaunchOptions) ([]byte, error) {
	pin, ok := shellPin(shell)
	if !ok {
		return nil, fmt.Errorf("shellintegration: no stage-1 pin for shell kind %q", shell)
	}
	body := strings.NewReplacer(
		"@CAPR@", strconv.Itoa(CapabilityFD),
		"@CAPW@", strconv.Itoa(capabilityWriteFD),
		"@STAGEFD@", strconv.Itoa(StageFD),
		"@CAPFDENV@", CapabilityFDEnv,
		"@BOOTENV@", BootstrapEnv,
		"@STAGEREADY@", StageReadyToken,
		"@MAGIC@", FrameMagic,
		"@SEQ@", strconv.Itoa(FrameSecretSeq),
		"@HDRLEN@", strconv.Itoa(FrameHeaderLen),
		"@MAXSECRET@", strconv.Itoa(MaxSecretFrameLen),
		"@KSECRET@", secretFrameSecret,
		"@KREFUSE@", secretFrameRefuse,
		"@LAUNCHPATH@", dirName+"/"+launchName,
		"@PIN@", pin,
		"@INTERRUPTED@", OutcomeToken(OutcomeBootstrapInterrupted),
		"@PROTOCOL@", OutcomeToken(OutcomeBootstrapProtocol),
		"@SECRETTOOLARGE@", OutcomeToken(OutcomeSecretTooLarge),
		"@BADSECRET@", OutcomeToken(OutcomeSecretMalformed),
		"@WRONGFRAME@", OutcomeToken(OutcomeSecretNotForThisSession),
		"@NOTEMP@", OutcomeToken(OutcomeNoSecureTemp),
		"@NOCAPFD@", OutcomeToken(OutcomeCapabilityFDUnavailable),
		"@NOUNLINK@", OutcomeToken(OutcomeCapabilityUnlinkFailed),
		"@NOCAPWRITE@", OutcomeToken(OutcomeCapabilityWriteFailed),
		"@NOGENERATION@", OutcomeToken(OutcomeGenerationUnavailable),
	).Replace(stage1Template)
	if strings.Contains(body, "@") {
		return nil, fmt.Errorf("shellintegration: stage-1 has an unsubstituted token")
	}
	if len(body) > MaxStageFrameLen {
		return nil, fmt.Errorf("shellintegration: stage-1 is %d bytes, over the %d cap",
			len(body), MaxStageFrameLen)
	}
	return []byte(body), nil
}

// secretFieldOK reports whether an addressing value may travel in a frame
// header. The header is compared by stage-1 as ONE string against values it
// received as positional parameters, so a field carrying a space or a newline
// would let two different tuples produce one header — and a non-ASCII byte
// would break the length check stage-1 uses to detect a truncated frame,
// because a shell counts characters in a UTF-8 locale and bytes in C.
func secretFieldOK(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// hexFieldOK reports whether a bearer is the shape stage-1 accepts: lowercase
// hex and nothing else. Validation lives here rather than only in the shell
// because this side has the types and the error path; stage-1 re-checks it
// anyway, since a frame is not trusted for being well-formed at the sender.
func hexFieldOK(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// SecretFrame builds frame 2 for one session: the header naming the session,
// domain and epoch this pair belongs to, then the capability and the fence,
// one per line.
//
// The header is what makes assertion 9 mechanical. Stage-1 compares it as a
// whole against the addressing arguments the COMMAND gave it, so a frame
// minted for another session, another domain or another epoch differs in the
// one string it is checked by, and is refused before a temp file exists. A
// replay is the same statement in time rather than in space: stage-1 reads
// frame 2 exactly once and then execs, so there is no reader left to
// recognise a second one.
//
// The trailing newline is load-bearing. Stage-1 reads the body with a command
// substitution, which strips trailing newlines, and checks that what survives
// is exactly one byte shorter than the header declared — so a frame that ends
// in exactly one newline detects a truncated body, and a frame that ends in
// several would look truncated.
func SecretFrame(opts LaunchOptions) ([]byte, error) {
	sid := ""
	if opts.Enhanced {
		sid = opts.SessionID
	}
	for _, f := range []string{sid, opts.Domain} {
		if !secretFieldOK(f) {
			return nil, fmt.Errorf("shellintegration: addressing value %q cannot travel in a frame header", f)
		}
	}
	if opts.Capability == "" || !hexFieldOK(opts.Capability) {
		return nil, fmt.Errorf("shellintegration: capability is not lowercase hex")
	}
	if !hexFieldOK(opts.Recovery) {
		return nil, fmt.Errorf("shellintegration: recovery fence is not lowercase hex")
	}
	body := fmt.Sprintf("%s %s %s %s %d\n%s\n%s\n",
		FrameMagic, secretFrameSecret, sid, opts.Domain, opts.Epoch,
		opts.Capability, opts.Recovery)
	if len(body) > MaxSecretFrameLen {
		return nil, fmt.Errorf("shellintegration: secret frame is %d bytes, over the %d cap",
			len(body), MaxSecretFrameLen)
	}
	return []byte(body), nil
}

// RefusalFrame builds the non-secret frame 2 (design §6.1): the lifecycle
// channel could not be opened, so nothing was minted and stage-1 is told so
// rather than left to time out. It carries a reason and no bearer.
//
// This is the shape the earlier draft got wrong: it delivered the secret and
// then discarded it when the lifecycle channel turned out to be refused, which
// hands a bearer across a boundary before establishing that it has any use.
// A refused session still reaches an integrated PROMPT — the tiers work
// without a capability, the session is simply conventional in the lifecycle
// sense — so the refusal frame execs the launcher exactly like the accepted
// one, minus the descriptor.
func RefusalFrame(reason Outcome) []byte {
	tok := OutcomeToken(reason)
	if tok == "" {
		tok = OutcomeToken(OutcomeChannelUnavailable)
	}
	return []byte(fmt.Sprintf("%s %s %s\n", FrameMagic, secretFrameRefuse, tok))
}
