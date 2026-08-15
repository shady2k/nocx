# nocx shell integration for zsh
# Activated when NOCX_SHELL_INTEGRATION is set.
# Emits OSC 133 (A/B/C/D) command markers and OSC 7 (cwd).

if [[ -z "${NOCX_SHELL_INTEGRATION:-}" ]]; then
    return 2>/dev/null || exit 0
fi

if [[ -n "${__nocx_loaded:-}" ]]; then
    return 2>/dev/null || exit 0
fi
__nocx_loaded=1


# --- Authenticated lifecycle channel (ADR-0024, docs/lifecycle-protocol.md) ---
# The command lifecycle rides a channel that is not the tty; every envelope
# is authenticated by the per-epoch capability. The capability reaches the
# shell substituted into the bootstrap script text (the launcher rcfile's
# @CAP@, or the first line of the in-band raw-mode stream); it is NEVER in
# the environment, never exported, and never written to a file. A shell
# without a capability, a transport or an accept is a conventional terminal:
# the native prompt stays visible and no lifecycle event is sent (ADR-0024
# decisions 3 and 9).
#
# The envelope addresses lane, domain and epoch explicitly — they are names,
# not secrets, and arrive via the launcher environment (NOCX_LIFECYCLE_*) or
# the in-band dispatcher. The transport is either an inherited descriptor
# (NOCX_LIFECYCLE_FD, local) or a loopback TCP port (NOCX_LIFECYCLE_PORT,
# remote and in-band).
__nocx_cap="${__nocx_cap:-}"
# zsh has no `export -n`: `typeset +x` removes the export attribute. A
# user rc running under `set -a` would otherwise auto-export the bootstrap's
# assignment, publishing the capability in /proc/<pid>/environ.
# NOTE: zsh's `typeset -n` is a nameref — never use it for this.
typeset +x __nocx_cap 2>/dev/null
__nocx_lc_lane="${NOCX_LIFECYCLE_LANE:-}"
__nocx_lc_dom="${NOCX_LIFECYCLE_DOMAIN:-}"
__nocx_lc_epoch="${NOCX_LIFECYCLE_EPOCH:-}"
__nocx_lc_fd="${NOCX_LIFECYCLE_FD:-}"
__nocx_lc_port="${NOCX_LIFECYCLE_PORT:-}"
if [[ "${NOCX_LIFECYCLE_TIMEOUT_MS:-}" =~ ^[0-9]+$ ]] && (( NOCX_LIFECYCLE_TIMEOUT_MS >= 1 )); then
    # zsh `read -t` takes integer seconds; ceil the millisecond override.
    __nocx_lc_timeout_s=$(( (NOCX_LIFECYCLE_TIMEOUT_MS + 999) / 1000 ))
else
    # Matches the kernel's hello_timeout (protocol doc §5): a shell that
    # gives up before the kernel's own budget would strand an accept that
    # arrives late, leaving an Established domain with no consumer.
    __nocx_lc_timeout_s=10
fi
__nocx_lc_active=0
__nocx_lc_seq=0
__nocx_lc_attempt_open=0
__nocx_lc_attempt_n=0
__nocx_lc_attempt_id=''
__nocx_lc_last_completed_id=''
__nocx_lc_last_completed_code=''
__nocx_lc_desynced=0
__nocx_lc_frame=''
__nocx_lc_lane_esc=''
__nocx_lc_dom_esc=''

# JSON-escape one string into __nocx_lc_json_escaped. Backslash, quote and
# the C0/DEL bytes JSON forbids are escaped; raw UTF-8 passes through.
# Deliberately NOT under LC_ALL=C: zsh's character-class pattern matching
# misbehaves in the C locale (verified), and the byte-counting callers
# (__nocx_lc_send/__nocx_lc_read_frame) scope LC_ALL=C themselves.
__nocx_lc_json_escape() {
    local s="$1" out i c code hex
    out=${s//\\/\\\\}
    out=${out//\"/\\\"}
    out=${out//$'\n'/\\n}
    out=${out//$'\t'/\\t}
    out=${out//$'\r'/\\r}
    out=${out//$'\b'/\\b}
    out=${out//$'\f'/\\f}
    # Remaining C0 (0x01-0x08, 0x0b, 0x0c, 0x0e-0x1f) and DEL break a JSON
    # string; the common escapes above already took \t \n \r \b \f. zsh
    # strings are 1-indexed, so the loop runs 1..length.
    if [[ "$out" == *[$'\001'-$'\010'$'\013'$'\014'$'\016'-$'\037'$'\177']* ]]; then
        for ((i = 1; i <= ${#out}; i++)); do
            c=${out[i]}
            code=$(( #c ))
            if (( code >= 0 && (code < 32 || code == 127) )); then
                hex=${(l:2::0:)$(( [##16] code ))}
                out="${out[1,i-1]}\\u00${hex}${out[i+1,${#out}]}"
            fi
        done
    fi
    __nocx_lc_json_escaped=$out
}

# Send one envelope: 4-byte big-endian length prefix then the JSON bytes
# (protocol doc §6). Every envelope carries the full addressing tuple and
# the bearer capability; the sequence increments per envelope (doc §11).
# LC_ALL=C so ${#json} counts bytes, not characters.
__nocx_lc_send() {
    # $1 = event kind; $2 = extra JSON fields (leading comma) or empty
    local __evt="$1" __extra="${2:-}" __json __len __b0 __b1 __b2 __b3 LC_ALL=C
    __nocx_lc_seq=$(( __nocx_lc_seq + 1 ))
    __json="{\"v\":1,\"lane\":\"${__nocx_lc_lane_esc}\",\"dom\":\"${__nocx_lc_dom_esc}\",\"epoch\":${__nocx_lc_epoch},\"seq\":${__nocx_lc_seq},\"cap\":\"${__nocx_cap}\",\"evt\":\"${__evt}\"${__extra}}"
    __len=${#__json}
    __b0=$(( (__len >> 24) & 0xff )); __b1=$(( (__len >> 16) & 0xff ))
    __b2=$(( (__len >> 8) & 0xff )); __b3=$(( __len & 0xff ))
    builtin printf "\\$(printf '%03o' "$__b0")\\$(printf '%03o' "$__b1")\\$(printf '%03o' "$__b2")\\$(printf '%03o' "$__b3")%s" "$__json" >&"$__nocx_lc_fd" 2>/dev/null
}

# Read one length-prefixed JSON frame into __nocx_lc_frame. zsh's `read -k`
# is binary-safe (unlike bash's), so the NUL-containing prefix is read
# directly; the length bytes are parsed through od. Any framing failure
# (EOF, garbage, oversize) returns non-zero and the caller fails open.
# The frame bound: one declaration for the hello and for the length check
# (nocx-beib). Keep in step with lifecycle.MaxFrameBytes.
__nocx_lc_max_frame=262144

__nocx_lc_read_frame() {
    # $1, when given, is the per-read timeout in seconds (the refresh poll
    # bounds the prompt); it defaults to the handshake timeout.
    local __t="${1:-$__nocx_lc_timeout_s}" __hdr __hex __len LC_ALL=C
    if ! read -t "$__t" -k 4 -u "$__nocx_lc_fd" __hdr 2>/dev/null; then
        return 1
    fi
    __hex=$(printf %s "$__hdr" | od -An -tx1 | tr -d ' \n')
    [[ "$__hex" =~ ^[0-9a-f]{8}$ ]] || return 1
    __len=$(( 16#$__hex ))
    (( __len > 0 && __len <= __nocx_lc_max_frame )) || return 1
    if ! read -t "$__t" -k "$__len" -u "$__nocx_lc_fd" __nocx_lc_frame 2>/dev/null; then
        return 1
    fi
    return 0
}

# Answer a pending refresh_request with an authenticated snapshot (protocol
# doc §10, ADR-0024 decision 7) — the zsh twin of the bash tier's
# __nocx_lc_ans_refresh. The kernel demands this when a framing gap
# desynchronized the domain; ONLY a snapshot answering the request restores
# authority, so this runs at every prompt and must not lose the request.
#
# The poll is prompt-boundary. It is non-blocking in the common case: zsh's
# `read` cannot probe without consuming a byte (unlike bash's `read -N 0`),
# so the readiness check is zselect -r, which consumes nothing. When
# zsh/zselect is unavailable the poll degrades to the bounded frame read
# below — a stall guard, not a working budget.
#
# The shell names its own attempts: it mints an id per command at start —
# the app mints its own and no outbound envelope carries one back (protocol
# §8) — and the kernel learns the shell's id at attach, resolving it as a
# per-attempt alias. The snapshot reports last_completed — the attempt the
# shell just finished, with the REAL exit status — whenever one exists, so a
# completion the gap swallowed still reconciles to its real status instead
# of to unknown. active_attempt is never reported: the shell answers only
# from a prompt, where nothing is running. shell_state is at_prompt because
# this runs from a prompt; next_seq is the shell's next sequence, strictly
# greater than the snapshot's own (the kernel rejects `next_seq <= seq`).
#
# On success marks the domain desynced: the prompt-boundary arm restores a
# visible prompt (decision 9) — a suppressed marker-only prompt over a
# Desynchronized domain would be invisible raw input.
__nocx_lc_ans_refresh() {
    local __rid
    if zmodload zsh/zselect 2>/dev/null; then
        zselect -t 0 -r "$__nocx_lc_fd" 2>/dev/null || return 1
    fi
    # A frame is buffered (the kernel writes each envelope in one write);
    # the short bound is a stall guard, not a working budget.
    __nocx_lc_read_frame 1 || return 1
    case "$__nocx_lc_frame" in
        *'"evt":"refresh_request"'*) : ;;
        *) return 1 ;; # not a refresh; leave it buffered for the next prompt
    esac
    __rid="${__nocx_lc_frame#*\"request\":\"}"
    __rid="${__rid%%\"*}"
    # Kernel-minted shape: req-<16 hex>. Anything else is not a request we
    # can answer — quoting it into the JSON would forge one.
    [[ "$__rid" =~ ^req-[0-9a-f]{16}$ ]] || return 1
    # The snapshot's own seq is seq+1 after this call; next_seq must be
    # strictly greater than it, and is the sequence the NEXT envelope will
    # carry — so it is seq+2 in pre-send terms.
    #
    # last_completed is the shell's own view (its id + the real exit
    # status), recorded by __nocx_precmd before the refresh can preempt the
    # complete. When no command just finished — the shell genuinely has
    # nothing to report — the field is omitted and the kernel reconciles
    # open attempts as unknown, never success.
    if [[ -n "${__nocx_lc_last_completed_id:-}" ]]; then
        __nocx_lc_send snapshot ',"request":"'"$__rid"'","shell_state":"at_prompt","last_completed":{"attempt":"'"$__nocx_lc_last_completed_id"'","exit_code":'"$__nocx_lc_last_completed_code"'},"next_seq":'"$(( __nocx_lc_seq + 2 ))"
    else
        __nocx_lc_send snapshot ',"request":"'"$__rid"'","shell_state":"at_prompt","next_seq":'"$(( __nocx_lc_seq + 2 ))"
    fi
    __nocx_lc_desynced=1
    return 0
}

# The render fence: 32 random bytes (64 hex chars) the shell generates when
# a command finishes and writes to the pty after the output. It is a
# rendezvous for render ordering and carries NO authority (protocol doc §8);
# when /dev/urandom is unavailable a session-scoped pseudo-random fallback
# is honest for a nonce whose only job is matching.
__nocx_lc_fence() {
    local f i
    f="$(od -An -N32 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n')"
    if [[ -z "$f" ]]; then
        # zsh printf has no -v; the [##16] arithmetic flag builds hex with
        # no fork. RANDOM is 15 bits, so four digits per draw, 16 draws.
        for ((i = 0; i < 16; i++)); do
            f+="${(l:4::0:)$(( [##16] RANDOM ))}"
        done
    fi
    f="${f:0:64}"
    if [[ "$f" =~ ^[0-9a-f]{64}$ ]]; then
        __nocx_lc_fence_hex=$f
        return 0
    fi
    return 1
}

# Establish the channel: connect (or use the inherited descriptor), send
# hello (sequence 1), and wait — bounded — for accept. Only after accept may
# the shell suppress its prompt or emit lifecycle events (decision 3). Any
# failure leaves a conventional terminal with a visible native prompt.
__nocx_lc_init() {
    local __cfg_ok=0
    __nocx_lc_active=0
    if [[ -n "$__nocx_cap" ]] && [[ "$__nocx_cap" =~ ^[0-9a-f]{64}$ ]] \
        && [[ -n "$__nocx_lc_lane" ]] && [[ -n "$__nocx_lc_dom" ]] \
        && [[ "$__nocx_lc_epoch" =~ ^[0-9]+$ ]] \
        && [[ -n "$__nocx_lc_fd" || -n "$__nocx_lc_port" ]]; then
        __cfg_ok=1
    fi
    if [[ "$__cfg_ok" != "1" ]]; then
        return 1
    fi
    if [[ -n "$__nocx_lc_port" ]]; then
        # Remote / in-band transport: zsh's ztcp. The bind address is the
        # literal 127.0.0.1, never localhost (ADR-0024). A FIXED high
        # descriptor, like the bash tier: zsh's ztcp allocates into REPLY
        # and would collide with the inherited-fd path otherwise.
        if ! zmodload zsh/net/tcp 2>/dev/null; then
            return 1
        fi
        if ! ztcp 127.0.0.1 "$__nocx_lc_port" 2>/dev/null; then
            return 1
        fi
        __nocx_lc_fd=$REPLY
    fi
    __nocx_lc_json_escape "$__nocx_lc_lane"
    __nocx_lc_lane_esc=$__nocx_lc_json_escaped
    __nocx_lc_json_escape "$__nocx_lc_dom"
    __nocx_lc_dom_esc=$__nocx_lc_json_escaped
    # The bundle this shell was brought up from — see the same block in
    # nocx.bash for why only the far side can name it and why it is escaped.
    __nocx_lc_json_escape "${NOCX_GENERATION-}"
    __nocx_lc_gen_esc=$__nocx_lc_json_escaped
    __nocx_lc_send hello ',"shell":"zsh","max_frame":'"$__nocx_lc_max_frame"',"gen":"'"$__nocx_lc_gen_esc"'"'
    if ! __nocx_lc_read_frame; then
        return 1
    fi
    # Two independent substring checks, not one ordered pattern: the
    # envelope's field order is the adapter's, and a case pattern like
    # *evt*cap* would silently reject a valid accept whose cap field
    # precedes evt.
    case "$__nocx_lc_frame" in
        *'"evt":"accept"'*) : ;;
        *) return 1 ;;
    esac
    case "$__nocx_lc_frame" in
        *'"cap":"'"$__nocx_cap"'"'*) : ;;
        *) return 1 ;;
    esac
    __nocx_lc_active=1
    return 0
}
__nocx_lc_init

# --- Nested environments (nocx-u7uh.28, the zsh tier of nocx-u7uh.11) ---
# A nested environment (sudo -i, su -, ssh host) is a DIFFERENT shell than
# the parent, and protocol doc §9 gives it its own authenticated domain.
# The parent requests the child over this channel (domain_request), reads
# the grant (the kernel's answer, carrying the opaque bootstrap the parent
# executes), sends domain_suspended, and launches the child. The child owns
# the channel stream from its exec until it exits; the parent resumes at its
# next prompt boundary and sends domain_activated — the only way a suspended
# parent returns. A child that never establishes (refused bootstrap, sudo
# policy, no forwarding) still ends with the parent's activation: the
# stillborn interval (§9) is the expected path, not an error.
#
# zsh's interception differs from bash's by one mechanism, and the choice is
# made here: bash skips the entering command from a DEBUG trap (shopt -s
# extdebug); zsh's DEBUG trap CANNOT suppress a command. The zsh mechanism
# is the accept-line WIDGET (registered at the bottom of this script): it
# runs when the user accepts a line, BEFORE the line is executed, and can
# consume the line instead. A preexec re-dispatch was rejected: zsh's
# preexec hook fires after acceptance, immediately before execution, and has
# no supported veto — re-dispatching from there would race the original
# command. The widget replaces the accept-line widget itself (not a
# keybinding) so every accept path — ^M, ^J, vi mode, other widgets that
# call accept-line — routes through it, and the normal path chains to the
# previous accept-line (a user/framework wrapper, or the builtin).
__nocx_nested_active=0
__nocx_nested_n=0
__nocx_nested_env=
__nocx_nested_host=
__nocx_nested_user=
__nocx_nested_port=0
__nocx_grant_bootstrap=
__nocx_nested_rc=
# Bounds the grant wait: the grant is composed synchronously by the backend
# pump (or refused with an empty bootstrap), so a few seconds is generous
# and a dead channel fails open without holding the user's command.
# Declared once per shell, not once per source: the rcfile deliberately
# re-sources the embedded script over an installer-era install, and a
# readonly cannot be re-declared.
if [[ -z "${__nocx_lc_grant_timeout_s:-}" ]]; then
    readonly __nocx_lc_grant_timeout_s=5
fi

# JSON unescape (zsh): the grant's bootstrap is a JSON string on the wire
# (the frame is one JSON document); the shell extracts and decodes it. A
# decoding failure corrupts the rcfile, which makes the child conventional —
# the safe direction — so the decoder is best-effort by construction.
#
# zsh escaping differs from bash's in one load-bearing way, measured on
# 5.9.2 (nocx-u7uh.28): inside ${s//pat/repl} the pattern is matched
# LITERALLY — a backslash in the pattern text is a literal backslash, and
# parse-time double-quote processing has already happened on the SOURCE,
# never on ${...} expansion results. So ONE ${bs} in the pattern matches
# ONE literal backslash in the text; a pattern spelled \\n in the source
# matches backslash-n only because the source's \\ collapsed at parse
# time, and two ${bs} match TWO backslashes, never one. (bash collapses
# the same way, so its three-backslash forms match a different class
# here.)
__nocx_lc_json_unescape() {
    local s="$1" hexc octc n
    local bs=$'\\' dq='"' nl=$'\n' tab=$'\t' cr=$'\r' bb=$'\b' ff=$'\f'
    # Protect literal backslashes (\\ in JSON) before the single-char
    # escapes: a literal backslash must not be consumed by the \" or \n
    # passes below. The marker is DOMAIN-scoped: the payload being decoded
    # is itself shell source that CONTAINS this function's text, so any
    # fixed marker (like the payload's own __NOCX_BS__ references) would
    # collide with itself; the runtime domain value appears nowhere in the
    # payload's static text.
    s="${s//$bs$bs/__NOCX_BS_${__nocx_lc_dom}__}"
    s="${s//$bs$dq/$dq}"
    s="${s//$bs"n"/$nl}"
    s="${s//$bs"t"/$tab}"
    s="${s//$bs"r"/$cr}"
    s="${s//$bs"b"/$bb}"
    s="${s//$bs"f"/$ff}"
    s="${s//$bs"/"/"/"}"
    # \uXXXX is rare in the rcfile (printable ASCII dominates); convert it
    # to octal so the (g:o:) pass below decodes it with the rest. The ERE
    # names the backslash as \\ (two characters — an ERE \\ is one literal
    # backslash), while the replacement glob names it as one ${bs}. The
    # loop is bounded: every iteration consumes the match (the glob always
    # replaces what the regex found), and a 64 KiB frame cannot hold more
    # than ~10k six-byte \uXXXX sequences — the bound converts any future
    # regression from a hang to a fail-open.
    n=0
    while [[ "$s" =~ ${bs}${bs}"u"([0-9a-fA-F]{4}) ]]; do
        if (( n++ > 20000 )); then
            # Pathological (cannot happen in a 64 KiB frame): fail open to
            # an EMPTY bootstrap, which the launch treats as the refusal —
            # the child runs conventionally, never a hang.
            __nocx_lc_json_unescaped=
            return 1
        fi
        hexc="${match[1]}"
        # THREE octal digits, always — the padding is load-bearing, not
        # cosmetic. \NNN consumes up to three digits, so an unpadded escape
        # eats the character after it whenever that character is a digit:
        # `>` is > on the wire (Go escapes <, > and & by default), which
        # gives \76, and `>0` then reads as \760 — 496, truncated to the byte
        # 0xF0. Measured exactly that: a payload's `select(...)>0` reached the
        # child as `select(...)\xF0 ? 0 : 1` and perl refused to parse it
        # (nocx-aupk). `2>&1` corrupts the same way — & gives \46, and
        # `&1` reads as \461. bash's twin has always used %03o; this is the
        # zsh side catching up.
        octc=$(( [##8] 16#$hexc ))
        octc="${(l:3::0:)octc}"
        s="${s//${bs}"u"$hexc/${bs}${octc}}"
    done
    # The zsh twin of bash's printf %b pass: (g:o:) processes backslash
    # escapes in place — no fork, and no trailing-newline loss (a $(...)
    # substitution would strip them).
    s="${(g:o:)s}"
    s="${s//__NOCX_BS_${__nocx_lc_dom}__/$bs}"
    __nocx_lc_json_unescaped="$s"
}

# Read the grant answering request $1 (bounded). Sets __nocx_grant_env and
# __nocx_grant_bootstrap; non-grant frames (a refresh demand, a stale grant
# from an earlier request) are handled or skipped. Returns non-zero on
# timeout or a dead channel — the parent then runs its command
# conventionally.
__nocx_lc_read_grant() {
    local __rid="$1" __t=0 __env __bootstrap
    __nocx_grant_bootstrap=
    __nocx_grant_env=
    while (( __t < __nocx_lc_grant_timeout_s )); do
        if zmodload zsh/zselect 2>/dev/null; then
            # zsh's read cannot probe without consuming a byte (no bash
            # `read -N 0`), so the readiness probe is zselect -r, which
            # consumes nothing — the same probe the refresh poll uses. A
            # silent channel still advances the bound below.
            if ! zselect -t 0 -r "$__nocx_lc_fd" 2>/dev/null; then
                sleep 1
                __t=$(( __t + 1 ))
                continue
            fi
        fi
        __nocx_lc_read_frame 1 || return 1
        case "$__nocx_lc_frame" in
            *'"evt":"domain_grant"'*) : ;;
            *'"evt":"refresh_request"'*) __nocx_lc_ans_refresh || true; continue ;;
            *) continue ;; # a stale frame (e.g. a late accept): skip it
        esac
        case "$__nocx_lc_frame" in
            *'"request":"'"$__rid"'"'*) : ;;
            *) continue ;; # a grant for a different request: skip (stale)
        esac
        __env="${__nocx_lc_frame#*\"env\":\"}"
        __env="${__env%%\"*}"
        # The bootstrap is the grant's LAST field (the wire order is pinned
        # by the codec): everything after its opening quote, minus the
        # closing quote and brace. Its own escaped quotes are untouched.
        # SHORTEST match, deliberately: ## scans the whole frame for the
        # LAST occurrence, which on a ~78 KiB grant costs seconds of CPU in
        # the shell — measured 1.65 s for one such expansion against 1 ms
        # for this one, and the ssh child's grant hit several of them, which
        # is where the eleven seconds before the ssh prompt went (nocx-beib).
        # It is also the more correct match: the field precedes its own
        # value, so the FIRST occurrence is the real one, while ## would
        # prefer a lookalike inside the bootstrap text.
        __bootstrap="${__nocx_lc_frame#*\"bootstrap\":\"}"
        __bootstrap="${__bootstrap%?}"
        __bootstrap="${__bootstrap%\"}"
        __nocx_lc_json_unescape "$__bootstrap"
        __nocx_grant_bootstrap="$__nocx_lc_json_unescaped"
        __nocx_grant_env="$__env"
        return 0
    done
    return 1
}

# Classify a typed line as a nested environment. Conservative by design:
# anything ambiguous is NOT nested and runs conventionally — the honest
# fallback, never a guessed launch. Sets __nocx_nested_env (sudo|su|ssh)
# plus the ssh destination parts.
__nocx_nested_detect() {
    __nocx_nested_env=
    __nocx_nested_host=
    __nocx_nested_user=
    __nocx_nested_port=0
    [[ "${__nocx_lc_active:-0}" == "1" ]] || return 1
    local __line="$1"
    # sudo: -i/--login/-s/--shell with NO command — a login/shell session,
    # not `sudo -i ls` (which is a command, not a nested shell).
    if [[ "$__line" =~ ^sudo[[:space:]]+(-i|--login|-s|--shell)[[:space:]]*$ ]]; then
        __nocx_nested_env=sudo
        return 0
    fi
    # su: no -c (a command), no shell metacharacters, only known flags and
    # at most one username. The metachar check is a case pattern, not a
    # bracket expression: an unquoted backtick inside [[ ]] would be
    # command substitution in zsh.
    if [[ "$__line" =~ ^su([[:space:]]+.*)?$ ]]; then
        case "$__line" in
            *';'*|*'|'*|*'&'*|*'<'*|*'>'*|*'`'*) return 1 ;;
        esac
        [[ "$__line" =~ -c([[:space:]]|$) ]] && return 1
        local __rest="${__line#su}" __tok __users=0
        for __tok in ${=__rest}; do
            case "$__tok" in
                -|-l|--login|-p|-m|--preserve-environment) : ;;
                -*) return 1 ;; # an option we do not model: refuse
                *) __users=$(( __users + 1 )); (( __users > 1 )) && return 1 ;;
            esac
        done
        __nocx_nested_env=su
        return 0
    fi
    # ssh: a simple interactive login — known flags, exactly one
    # destination, no remote command. The frontend's classifier is the
    # authority for editor lines; this is the conservative shell fallback.
    if [[ "$__line" =~ ^ssh([[:space:]]+.*)?$ ]]; then
        case "$__line" in
            *';'*|*'|'*|*'&'*|*'<'*|*'>'*|*'`'*) return 1 ;;
        esac
        local -a __toks
        __toks=(${=__line})
        local __i __tok __dest="" __skip=0 __want_port=0
        # zsh arrays are 1-indexed; element 1 is "ssh".
        for ((__i = 2; __i <= ${#__toks}; __i++)); do
            __tok="${__toks[$__i]}"
            if (( __skip )); then
                (( __want_port )) && __nocx_nested_port="$__tok"
                __want_port=0
                __skip=0
                continue
            fi
            case "$__tok" in
                -t|-tt|-4|-6|-v|-C|-x|-X) : ;;
                -p) __skip=1; __want_port=1 ;;
                -l|-o|-i|-F|-J|-e|-b|-c|-m) __skip=1 ;;
                -*) return 1 ;; # an option we do not model: refuse
                *) [[ -n "$__dest" ]] && return 1; __dest="$__tok" ;;
            esac
        done
        [[ -z "$__dest" ]] && return 1
        if [[ "$__dest" =~ ^([A-Za-z0-9._-]+@)?[A-Za-z0-9._-]+(:[0-9]+)?$ ]]; then
            local __h="${__dest##*@}"
            __nocx_nested_host="${__h%%:*}"
            [[ "$__dest" == *@* ]] && __nocx_nested_user="${__dest%%@*}"
            [[ "$__h" == *:* ]] && __nocx_nested_port="${__h##*:}"
            __nocx_nested_env=ssh
            return 0
        fi
        return 1
    fi
    return 1
}

# sudo's descriptor-preservation flag is not portable: older sudo builds and
# compatible substitutes reject the long option. Probe the executable itself,
# not a version table, and do it BEFORE preexec/start/domain_request: a
# negative answer returns "not nested", so the original accept-line chain runs
# the untouched command exactly once. `--help` needs no authentication; the
# option spelling is stable even when the surrounding prose is localized.
__nocx_sudo_supports_preserve_fds() {
    local __help
    __help="$(LC_ALL=C command env -u BASHOPTS sudo --help 2>&1)" || true
    case "$__help" in
        *--preserve-fds*) return 0 ;;
        *) return 1 ;;
    esac
}

# Request the child domain and launch it. Runs from the accept-line widget
# (which returns without accepting the line only when this returns 0). The
# launch BLOCKS here for the child's whole lifetime — the parent shell is
# inside this call while the child owns the stream, which is exactly the
# handoff interval §9 names.
#
# Return codes, three outcomes: 0 the line was consumed (child ran, or the
# conventional fallback after a suspend); 1 NOT nested — the widget chains
# to accept-line and the preexec hook fires normally; 2 nested but failed
# open BEFORE the suspend (no grant, dead transport) — the C marker and the
# start were already emitted, so the widget runs the command itself rather
# than accepting the line (which would double-fire the preexec hook).
__nocx_nested_launch() {
    local __line="$1" __rid __extra
    __nocx_nested_detect "$__line" || return 1
    if [[ "$__nocx_nested_env" == "sudo" ]] && ! __nocx_sudo_supports_preserve_fds; then
        return 1
    fi
    # The C marker and the start event are emitted HERE, mirroring the bash
    # tier's DEBUG-trap ordering (preexec before launch): the widget never
    # accepts the line, so zsh's preexec hook will not fire for it — this
    # call is the only start the kernel sees for the nested line.
    __nocx_preexec "$__line"
    __rid="r-$__nocx_lc_dom-$(( __nocx_nested_n++ ))"
    __extra='"request":"'"$__rid"'","env":"'"$__nocx_nested_env"'"'
    if [[ "$__nocx_nested_env" == "ssh" ]]; then
        __extra+=',"host":"'"$__nocx_nested_host"'"'
        [[ -n "$__nocx_nested_user" ]] && __extra+=',"user":"'"$__nocx_nested_user"'"'
        (( __nocx_nested_port != 0 )) && __extra+=',"port":'"$__nocx_nested_port"
    fi
    __nocx_lc_send domain_request ",$__extra" || return 2
    if ! __nocx_lc_read_grant "$__rid"; then
        return 2 # no grant: channel dead or refused without a reply
    fi
    # The child's hello requires the parent Suspended (§9) — never exec the
    # child before this frame is written.
    __nocx_lc_send domain_suspended
    __nocx_nested_active=1
    local __nocx_stage_ok=0 __nocx_boot_fd=0
    if [[ "$__nocx_nested_env" == "ssh" ]]; then
        # The bootstrap is the backend-composed rewritten line (ADR-0022):
        # the -R reverse forward plus the in-band payload piped into ssh -t.
        # The </dev/tty is load-bearing: zle runs a widget's commands with
        # stdin at /dev/null (measured, nocx-u7uh.28), so without the bind
        # the ssh line's `cat` bridge would see EOF and the in-band child
        # would get no keyboard.
        eval "$__nocx_grant_bootstrap" </dev/tty
        __nocx_nested_rc=$?
    elif [[ -n "$__nocx_grant_bootstrap" ]]; then
        # Same machine: stage the child's rcfile into a preserved descriptor
        # and launch. The child reads it via --rcfile /dev/fd/N (ADR-0024's
        # preferred answer: the capability never enters a filesystem
        # object); fd 3 is the inherited lifecycle channel, preserved so the
        # child speaks over the SAME transport as the parent.
        #
        # zsh's descriptor staging, measured on 5.9.2 (nocx-u7uh.28):
        #   - `exec {var}<file` allocates the FIRST FREE fd >= 10 and does
        #     NOT set close-on-exec — /proc/self/fdinfo flags 0100000/00,
        #     and the fd survives a real fork+exec. This DIFFERS from bash,
        #     whose coproc and {var} fds are close-on-exec.
        #   - zsh's coproc gives no COPROC array (empty, measured) — the
        #     fds are unreachable, so coproc cannot stage the child's
        #     descriptor the way bash's does.
        #   - process substitution <(...) yields a plain (non-CLOEXEC) read
        #     end, but zsh exposes NO writer PID ($! stays 0, measured —
        #     bash records the substitution PID), so the parent cannot wait
        #     for the writer. The guarantee is therefore the size bound:
        #     the payload (the child's rcfile) is ~25 KB — under the 64 KiB
        #     frame cap and the Linux pipe buffer, so the single fast
        #     printf completes before the child could read. On a 16 KB
        #     pipe-buffer platform a truncated read fails open — the child
        #     is a conventional shell and the parent re-activates at its
        #     next prompt (§9's stillborn interval), never a hung session —
        #     exactly the bash tier's documented bash-3.2 fallback.
        if exec {__nocx_boot_fd}< <(builtin printf '%s' "$__nocx_grant_bootstrap"); then
            __nocx_stage_ok=1
        fi
        if (( __nocx_stage_ok == 1 )); then
            # su has no --preserve-fds: the whole launch rests on the rcfile
            # descriptor surviving su's own exec. Measured/verified 2026-08-09
            # (nocx-u7uh.30): util-linux su (v2.42.2, su-common.c run_shell)
            # and shadow su (4.19.4, execve_shell) end in a plain
            # execv/execve with no fd sweep, BSD/macOS su (FreeBSD lineage)
            # is the same, and the real shadow su on this host preserved fd 7
            # through the exact launcher line — but NONE of them promise
            # preservation in a man page; it is an incidental property of
            # plain exec. The fallback when one does not preserve: the child
            # bash cannot read its rcfile, starts as a conventional shell
            # (measured: bash silently ignores the unreadable --rcfile),
            # never establishes, and the parent stillborn-activates at its
            # next prompt — asserted by the fd-closed su test.
            if [[ "$__nocx_nested_env" == "sudo" ]]; then
                env -u BASHOPTS sudo --preserve-fds=3,$__nocx_boot_fd -i env -u BASH_ENV -u BASHOPTS bash --rcfile /dev/fd/$__nocx_boot_fd -i </dev/tty
            else
                env -u BASHOPTS su -l -c 'env -u BASH_ENV -u BASHOPTS bash --rcfile /dev/fd/'"$__nocx_boot_fd"' -i' </dev/tty
            fi
            __nocx_nested_rc=$?
            # The child closes its own copy after reading the rcfile (the
            # bootstrap's closing line); the parent's permanent copy must
            # not linger for the next nested launch.
            exec {__nocx_boot_fd}<&- 2>/dev/null
        else
            # Cannot stage: run the command conventionally — the child is a
            # plain sudo/su session and the parent still activates at its
            # next prompt.
            eval "$__line" </dev/tty
            __nocx_nested_rc=$?
        fi
    else
        # The grant refused (empty bootstrap): run conventionally.
        eval "$__line" </dev/tty
        __nocx_nested_rc=$?
    fi
    return 0
}

# Run the prompt-boundary hooks the way zle would after a normally
# accepted line. The widget consumed a line without accepting it, and zle
# does NOT run the precmd hooks for that (measured, nocx-u7uh.28) — but
# the nested launch just closed the child, which IS a prompt boundary, and
# §9's domain_activated must ride THIS boundary, not the user's next
# command. The hooks are the same chain a real boundary runs (capture,
# __nocx_precmd, the marker-only prompt armer), so the second boundary the
# user eventually produces finds nested_active already cleared and sends
# nothing further.
__nocx_widget_prompt_boundary() {
    local __nocx_f
    for __nocx_f in $precmd_functions; do
        "$__nocx_f"
    done
    zle reset-prompt
}

# The accept-line widget (see the mechanism note at the top of this block).
__nocx_accept_line() {
    local __line="$BUFFER" __rc
    __nocx_nested_launch "$__line"
    __rc=$?
    if (( __rc == 0 )); then
        # The launch consumed the line (the child ran inside the widget to
        # completion); the original command must not execute. The child
        # closing is the parent's next prompt boundary — run the hooks now.
        BUFFER=''
        __nocx_widget_prompt_boundary
        return 0
    fi
    if (( __rc == 2 )); then
        # Nested but failed open (no grant, dead transport): the C marker
        # and the start were already emitted, so accepting the line would
        # double-fire the preexec hook. Run it here instead — with the tty
        # as stdin (the same EOF hazard as the launch commands).
        eval "$__line" </dev/tty
        BUFFER=''
        __nocx_widget_prompt_boundary
        return 0
    fi
    # Not nested: chain to the previous accept-line (a user/framework
    # wrapper, or the builtin); the preexec hook fires normally.
    if [[ -n "$__nocx_old_accept_line" ]]; then
        zle "$__nocx_old_accept_line"
    else
        zle .accept-line
    fi
}

autoload -Uz add-zsh-hook

__nocx_exit_code=0
__nocx_first_prompt=

__nocx_encode_url() {
    local s="$1"
    s="${s// /%20}"
    s="${s//$'\t'/%09}"
    s="${s//$'\n'/%0a}"
    builtin printf '%s' "$s"
}

# Capture the just-finished command's exit status. This must run before any
# other precmd hook can clobber $?, so it is forced to the front of
# precmd_functions below; it re-returns the status so later hooks still see it.
__nocx_capture_status() {
    __nocx_exit_code=$?
    return $__nocx_exit_code
}

# Emit one OSC 133 lifecycle marker — \e]133;A, \e]133;B, \e]133;C or
# \e]133;D[;<exit>]. A/B partition prompt bytes from output bytes for
# rendering; C/D are the standard's command-boundary markers, kept for
# third-party interop (ADR-0024 decision 1 leaves the decision open: nocx
# no longer consumes them, but any other tool reading the stream still can,
# and they carry no authority here).
__nocx_marker() {
    local __kind="$1" __code="${2:-}"
    if [[ -n "$__code" ]]; then
        builtin printf '\e]133;%s;%s\a' "$__kind" "$__code"
    else
        builtin printf '\e]133;%s\a' "$__kind"
    fi
}

# The lifecycle channel died mid-session: a send failed at a prompt
# boundary. Clear the active latch — the domain is lost and nothing more may
# be emitted over the dead transport — and mark the session recovered so the
# marker-only prompt hook restores a visible native prompt with the one-shot
# recovery fence (ADR-0024 decision 8). nocx matches that fence and
# acknowledges the restoration; until it lands, the session is neither an
# authenticated terminal nor a usable conventional one.
__nocx_lc_recover() {
    __nocx_lc_active=0
    __nocx_lc_recovered=1
}

__nocx_precmd() {
    # Authenticated channel first: refresh, complete (with the exit status
    # and a fresh fence nonce), write the SAME nonce to the pty after the
    # command's output (the render-order rendezvous, decision 1 carve-out),
    # then prompt_ready. The complete carries no attempt id; the kernel
    # resolves the domain's single open attempt.
    # A nested child whose command the widget consumed leaves __nocx_exit_code
    # = the widget's own last status (the launch's assignments clobbered $?);
    # the child's REAL status was captured right after the launch and
    # overrides here, exactly as the bash tier's __nocx_nested_rc does.
    if [[ -n "${__nocx_nested_rc:-}" ]]; then
        __nocx_exit_code=$__nocx_nested_rc
        __nocx_nested_rc=
    fi
    if [[ "${__nocx_lc_active:-0}" == "1" ]]; then
        # The child closed (the parent was blocked inside the accept-line
        # widget's launch); the parent owns the stream again. Activation MUST
        # precede the complete — a completion for a suspended domain is
        # rejected, and only an authenticated activation restores the parent
        # (§9).
        if [[ "${__nocx_nested_active:-0}" == "1" ]]; then
            __nocx_lc_send domain_activated
            __nocx_nested_active=0
        fi
        # Record the just-finished command's completion BEFORE the refresh
        # can preempt the complete: the snapshot reports what the shell
        # actually knows — its own attempt id and the real exit status — so
        # a completion the gap swallowed reconciles to its real status
        # rather than to unknown.
        if [[ "${__nocx_lc_attempt_open:-0}" == "1" ]]; then
            __nocx_lc_last_completed_id="$__nocx_lc_attempt_id"
            __nocx_lc_last_completed_code="$__nocx_exit_code"
        fi
        # A framing gap may have desynchronized the domain while the shell
        # was busy; the kernel's refresh_request is buffered. Answer it
        # FIRST — only a snapshot answering it restores authority (decision
        # 7), and while the domain is desynchronized the complete and
        # prompt_ready below would be quarantined anyway.
        if __nocx_lc_ans_refresh; then
            __nocx_lc_attempt_open=0
        elif [[ "${__nocx_lc_attempt_open:-0}" == "1" ]]; then
            if __nocx_lc_fence; then
                if __nocx_lc_send complete ',"exit_code":'"$__nocx_exit_code"',"fence":"'"$__nocx_lc_fence_hex"'"'; then
                    builtin printf '\e]1337;NOCX_FENCE;%s\a' "$__nocx_lc_fence_hex"
                else
                    __nocx_lc_recover
                fi
            fi
            __nocx_lc_attempt_open=0
        fi
        # A failed send means the transport is dead — the domain is lost,
        # the visible native prompt must be restored (decision 8), and no
        # further send is attempted this boundary (recover cleared active).
        if [[ "${__nocx_lc_active:-0}" == "1" ]]; then
            __nocx_lc_send prompt_ready || __nocx_lc_recover
        fi
    fi
    if [[ -n "$__nocx_first_prompt" ]]; then
        __nocx_marker D "$__nocx_exit_code"
    fi
    __nocx_marker A
    builtin printf '\e]7;file://%s%s\a' \
        "$(__nocx_encode_url "${HOST%%.*}")" \
        "$(__nocx_encode_url "$PWD")"
    __nocx_first_prompt=1
    # Command-existence snapshot (OSC 636): the enumeration started in the
    # background at source time, and this is where its payload reaches the
    # terminal. It has to be a prompt — the shell is the sole writer to the
    # tty here, so the payload can never interleave with command output — and
    # it is deliberately the LAST thing this hook does, after the A marker
    # and before zsh renders the prompt itself, so a freshly opened tab is
    # marked before the first prompt is usable. See the snapshot section at
    # the bottom of this file for the protocol and the bounds.
    __nocx_snapshot_pump
}

__nocx_preexec() {
    # zsh's preexec hook receives the full command line as $1.
    __nocx_marker C
    if [[ "${__nocx_lc_active:-0}" == "1" ]]; then
        # Shell-originated start, named with the shell's own attempt id: the
        # shell mints one per command because it never learns the app-minted
        # id (protocol §8 — no outbound envelope carries one back), and its
        # id is the only name it can report in a snapshot. The kernel
        # attaches to a pending app attempt (recording the id as a
        # per-attempt alias) or creates a shell-originated attempt under
        # this id. The id carries the domain (s-<dom>-<counter>): PID spaces
        # are not shared across domains, so s-$$-<n> collides whenever a
        # docker exec / ssh shell shares a low PID with another domain's
        # shell, and the kernel's global id table would reject the second
        # domain's first command. The domain is the disambiguator; the
        # per-shell counter keeps ids unique within it. The command text is
        # truncated to the kernel's command budget (4096 bytes); a longer
        # line loses its tail, never its frame.
        local __cmd="${1:-}" LC_ALL=C
        __cmd="${__cmd:0:4000}"
        __nocx_lc_json_escape "$__cmd"
        __nocx_lc_attempt_id="s-$__nocx_lc_dom-$(( __nocx_lc_attempt_n++ ))"
        __nocx_lc_send start ',"attempt":"'"$__nocx_lc_attempt_id"'","command":"'"$__nocx_lc_json_escaped"'"'
        __nocx_lc_attempt_open=1
    fi
}

add-zsh-hook precmd __nocx_capture_status
add-zsh-hook precmd __nocx_precmd
add-zsh-hook preexec __nocx_preexec

# Force the status capture to the front of precmd_functions so a precmd hook the
# user registered earlier (oh-my-zsh, plugins, sourced before our gate) cannot
# clobber $? before we read it. Dedupe first so re-sourcing stays idempotent.
precmd_functions=(__nocx_capture_status ${precmd_functions:#__nocx_capture_status})

# Non-printing B marker (zsh %{...%} so it takes zero prompt width).
__nocx_b_marker=$'%{\e]133;B\a%}'


if [[ "${NOCX_PROMPT_MODE:-}" == "marker-only" ]]; then
    # Nested-session gate (nocx-4ff.13): a shell that inherits a
    # NOCX_SESSION_ID it did not create (__nocx_owned_session already
    # exported by a parent) keeps a visible prompt.
    if [[ -n "${__nocx_owned_session:-}" ]]; then
        # Nested shell — do NOT arm the marker-only overlay.
        :
    else
        __nocx_owned_session="${NOCX_SESSION_ID:-}"
        export __nocx_owned_session
        # Enhanced mode: reassert a marker-only prompt AFTER frameworks run, every
        # prompt. Kept last in precmd_functions so a framework precmd that rewrote
        # PS1 cannot win. Do NOT touch PS2/PS3 (continuation/secondary stay native).
        __nocx_marker_only_prompt() {
            # Suppress the prompt only when the authenticated channel is
            # live: a suppressed prompt without a live domain is the phishing
            # primitive decision 9 forbids. Not live, the framework's prompt
            # stands visible, with the render-only B partition marker
            # appended exactly as baseline mode wraps it (the marker
            # suppresses nothing by itself).
            if [[ "${__nocx_lc_active:-0}" == "1" ]]; then
                PROMPT="$__nocx_b_marker"
                RPROMPT=''
                RPS1=''
            elif [[ "${__nocx_lc_recovered:-0}" == 1 ]]; then
                # The channel died mid-session (a send failed): a visible
                # native prompt stands, never a suppressed one taking raw
                # input (decision 8). The one-shot recovery fence rides
                # exactly the FIRST prompt's bytes — nocx matches it and
                # acknowledges the restoration; afterwards PROMPT is rebuilt
                # without it, so the nonce reaches the terminal once and is
                # never reused.
                __nocx_native_mode
                if [[ "${__nocx_lc_recovery_emitted:-0}" != 1 ]] && [[ -n "${__nocx_lc_recovery:-}" ]]; then
                    PROMPT="${PROMPT}"$'%{\e]1337;NOCX_RECOVERY;'"$__nocx_lc_recovery"$'\a%}'
                    __nocx_lc_recovery_emitted=1
                fi
                PROMPT="${PROMPT}$__nocx_b_marker"
            else
                # PROMPT and PS1 are the SAME parameter in zsh — assigning
                # both would append the marker twice.
                PROMPT="${PROMPT}$__nocx_b_marker"
            fi
        }
        add-zsh-hook precmd __nocx_marker_only_prompt
        # Force it last, deduped, on every source.
        precmd_functions=(${precmd_functions:#__nocx_marker_only_prompt} __nocx_marker_only_prompt)
    fi
elif [[ -z "${__nocx_prompt_wrapped:-}" ]]; then
    PS1="${PS1:-}"$'%{\e]133;B\a%}' 
    __nocx_prompt_wrapped=1
fi

# Restore a visible native prompt. Real caller: the marker-only prompt
# hook's recovered branch (ADR-0024 decision 8) — after the lifecycle channel
# dies mid-session, the user must never be left at a suppressed prompt taking
# raw input, which is the worst of both. The older nocx-4ff.9 "user hits
# escape" attribution had no caller and is deleted: the escape surface it
# described no longer exists.
__nocx_native_mode() {
    add-zsh-hook -d precmd __nocx_marker_only_prompt 2>/dev/null
    unset NOCX_PROMPT_MODE
    PROMPT='%~ %# '
    PS1='%~ %# '
}

#   OSC 636 ; S ; <nonce> ; <names> ST          snapshot; <names> is
#                                               `;`-joined and hex-escaped
#                                               (\\ for backslash, \xHH for
#                                               control/C1 bytes and ';')
#   OSC 636 ; H ; <nonce> ST                    session hello — the FIRST 636
#                                               message, before any command
#
# The zsh tier of the command-existence snapshot (nocx-qduc). The protocol is
# the bash tier's, unchanged and deliberately so: the frontend cannot know a
# shell's aliases, functions, builtins and PATH, so it asks the shell, and one
# wire format with two dialects is already one more than AD-8 wants. What
# differs below is mechanism, and each difference says why.
#
# The nonce is a per-session secret generated here: any process can print an
# OSC — a command's own output can forge a snapshot — so the frontend discards
# any payload that does not carry the established nonce. It is emitted at
# source time, before the first prompt, when no user command has run; the
# frontend accepts exactly one hello, so a forged re-hello cannot re-anchor it.
#
# The enumeration is a background job started at SOURCE time, not at the first
# prompt: a fresh tab must mark commands before the user runs anything, and a
# PATH on NFS makes the scan cost seconds — it must never sit in front of the
# prompt. The payload is emitted from a prompt, the only moment the shell is
# the sole writer to the tty. One snapshot per session; staleness is
# deliberately a later problem.
#
# It is staged in a mktemp file whose name carries no secret — the nonce must
# never appear in a path, in any argv, or exported — and mode 600 from
# creation. The final name only exists after the atomic mv, so a prompt can
# never read a partial payload, and the exit hook removes both files even when
# the shell exits before the snapshot was emitted.
__nocx_gen_nonce() {
    local n i
    n="$(od -An -N16 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n')"
    if [[ -z "$n" ]]; then
        # RANDOM is 15 bits, so four hex digits per draw; eight draws keep the
        # fallback at the kernel-RNG path's 32-hex width rather than half of
        # it. Lower-cased because zsh's [##16] arithmetic flag renders hex in
        # UPPER case, and every other producer and consumer of this nonce
        # writes it lower.
        for ((i = 0; i < 8; i++)); do
            n+="${(L)${(l:4::0:)$(( [##16] RANDOM ))}}"
        done
    fi
    builtin printf '%s' "$n"
}

# ONCE PER SHELL, NOT ONCE PER SOURCE — the launcher's .zshrc unsets
# __nocx_loaded and sources this script a second time so the session's
# authenticated copy installs over the installer-era one the user's ~/.zshrc
# already sourced. That is EVERY enhanced zsh session, not an edge case, and
# a second pass that minted a fresh nonce would announce a second hello:
# command-snapshot.ts keeps the FIRST one on purpose (accepting a re-hello is
# exactly the re-anchoring its forgery defence exists to prevent), so the
# snapshot would then carry a nonce the store discards and completion would
# be dead for the session with both frames well-formed. That is the bash
# tier's nocx-cbtc, and it costs the same here.
#
# The latch is the PID, not merely the nonce's presence. "Is the variable set"
# cannot tell a RE-SOURCE from an INHERITED value, and the two need opposite
# answers: a re-source must stay quiet, a child shell must announce itself. A
# nonce that leaked into the environment — a user rc under `set -a`
# auto-exports every assignment, which is the hazard the `typeset +x` below
# exists for — would otherwise silence a legitimately new session for good,
# with no hello and so no snapshot ever accepted. That is a fail-CLOSED
# degrade, the wrong direction everywhere in this file. $$ is the shell's own
# pid and differs in any child.
if [[ "${__nocx_snapshot_owner:-}" != "$$" ]] || [[ -z "${__nocx_snapshot_nonce:-}" ]]; then
    __nocx_snapshot_nonce="$(__nocx_gen_nonce)"
    __nocx_snapshot_owner=$$
    __nocx_snapshot_announce=1
else
    __nocx_snapshot_announce=0
fi
# zsh has no `export -n`: `typeset +x` drops the export attribute (and
# `typeset -n` is a nameref — never use it here). The nonce is the whole
# forgery defence of OSC 636, so it must not reach /proc/<pid>/environ or any
# child process.
typeset +x __nocx_snapshot_owner 2>/dev/null
typeset +x __nocx_snapshot_nonce 2>/dev/null

# Staged once per shell for the same reason as the nonce, and this half
# matters independently: a second mktemp would repoint __nocx_snap_file at a
# name the FIRST pass's background job is never going to write, so even a
# correctly-nonced snapshot would never be found at a prompt — and the first
# staging file would leak, because the exit hook only knows the latest name.
if (( __nocx_snapshot_announce )); then
    __nocx_snap_staging="$(mktemp "${TMPDIR:-/tmp}/nocx-snap.XXXXXX" 2>/dev/null)"
    __nocx_snap_file="${__nocx_snap_staging:+${__nocx_snap_staging}.snap}"
    __nocx_snapshot_done=0
fi

# How long the FIRST prompt waits for the source-time job before degrading to
# the later-prompt schedule. 250 ms: fast when the PATH is local, bounded when
# it is not; the prompt never waits longer, and only once per session.
# NOCX_SNAPSHOT_WAIT_MS overrides it, and exists for the tests — 250 ms is a
# budget for a HUMAN's prompt, so a test that asserts the mechanism would
# otherwise be asserting that a loaded runner finished inside a UX deadline.
#
# Declared once per shell, not once per source: a readonly can be neither
# unset nor re-declared, and the deliberate second source would print
# "readonly variable" into the user's terminal as the first thing they see
# (the bash tier's nocx-u7uh.22).
if [[ -z "${__nocx_snapshot_wait_ms:-}" ]]; then
    if [[ "${NOCX_SNAPSHOT_WAIT_MS:-}" =~ ^[0-9]+$ ]]; then
        readonly __nocx_snapshot_wait_ms="$NOCX_SNAPSHOT_WAIT_MS"
    else
        readonly __nocx_snapshot_wait_ms=250
    fi
fi

# Hex-escape the names into the `;`-joined payload on stdout, capped at 8192
# names and 65536 encoded characters. Names arrive as ARGUMENTS (the bash twin
# reads stdin because its producer is a pipeline; zsh's is an array, and a
# pipeline here would be two processes on the path a fresh tab waits for).
# Returns non-zero when the list is empty: an empty snapshot must never reach
# the frontend — "every command is unknown" is the same lie as "every command
# exists", pointing the other way.
#
# Bytes that would break or fake the OSC sequence are escaped as \xHH;
# backslash is \\ (VS Code's scheme). Printable ASCII and raw UTF-8 pass
# through — the terminal decodes the byte stream, and escaping every byte
# would double the payload for no safety.
#
# Deliberately NOT under LC_ALL=C, unlike the bash twin: zsh's character-class
# pattern matching misbehaves in the C locale (the same finding
# __nocx_lc_json_escape records). In a UTF-8 locale ${#s} counts characters
# where bash counts bytes, so the 65536 cap is a slightly different bound in
# the two tiers — it is a bound either way, and the frontend enforces its own.
#
# The fast path is a strict subset of the loop: printable ASCII passes through
# unchanged either way, and the two bytes with meaning in the payload are
# excluded from it explicitly. It is not an optimisation for its own sake —
# the bash tier measured the per-character loop at ~85 ms of a ~104 ms
# pipeline, in front of a grace period there is no second shot at.
__nocx_snapshot_build() {
    local __out='' __name __esc __c __hex
    local -i __n=0 __i __code
    for __name in "$@"; do
        if [[ "$__name" == *[![:print:]]* || "$__name" == *'\'* || "$__name" == *';'* ]]; then
            __esc=''
            for ((__i = 1; __i <= ${#__name}; __i++)); do
                __c=${__name[__i]}
                if [[ "$__c" == '\' ]]; then
                    __esc+='\\'
                elif [[ "$__c" == ';' ]]; then
                    __esc+='\x3b'
                else
                    __code=$(( #__c ))
                    if (( __code < 32 || (__code >= 127 && __code <= 159) )); then
                        __hex="${(L)${(l:2::0:)$(( [##16] __code ))}}"
                        __esc+="\\x$__hex"
                    else
                        __esc+="$__c"
                    fi
                fi
            done
            __name=$__esc
        fi
        (( ${#__out} + ${#__name} + 1 > 65536 )) && break
        __out+="$__name;"
        __n+=1
        (( __n >= 8192 )) && break
    done
    [[ -n "$__out" ]] || return 1
    builtin printf '%s' "$__out"
}

# The background job's body: enumerate, encode, stage, publish atomically.
__nocx_snapshot_write() {
    local -a __names
    # The tables that answer "what can this shell run". bash asks `compgen -c`
    # for all of them at once; zsh keeps one parameter per table, so the list
    # is their union — and the union is the point, not a detail: a tier that
    # enumerated three of the five would still ship a well-formed snapshot
    # under a matching nonce while the editor marked the user's own alias as a
    # command that does not exist. Reading the whole `commands` parameter is
    # what forces zsh to hash every PATH directory, which is the equivalent of
    # compgen's PATH scan and the reason this runs in the background.
    __names=( ${(k)commands} ${(k)builtins} ${(k)reswords} ${(k)functions} ${(k)aliases} )
    # (o) sorts, (u) dedupes — in the shell, where the bash twin pipes through
    # `sort -u`. Two fewer processes on the path a fresh tab is waiting for.
    __nocx_snapshot_build "${(@ou)__names}" >| "$__nocx_snap_staging" 2>/dev/null \
        && mv -f "$__nocx_snap_staging" "$__nocx_snap_file"
}

# Emit the finished snapshot once and remove the staging files. Only ever
# called from __nocx_precmd — the shell is the sole writer to the tty there.
# `$(<file)` reads the payload without a fork.
__nocx_snapshot_emit() {
    __nocx_snapshot_done=1
    builtin printf '\e]636;S;%s;%s\a' "$__nocx_snapshot_nonce" "$(<"$__nocx_snap_file")"
    rm -f "$__nocx_snap_staging" "$__nocx_snap_file"
}

# The first prompt's bounded grace period. The bound is on ELAPSED TIME, never
# on a count of sleeps: the bash tier bounded this by counting ten passes of
# `sleep 0.025` and held a first prompt for 1.319 s on CI, because `sleep` is
# not a builtin and on a loaded machine the fork dominates. zsh can do both
# halves without a fork — zsh/datetime reads the clock, zsh/zselect sleeps —
# so the poll costs the prompt nothing beyond the wait itself.
__nocx_snapshot_wait() {
    local __deadline
    if zmodload zsh/datetime 2>/dev/null && zmodload zsh/zselect 2>/dev/null; then
        __deadline=$(( EPOCHREALTIME + __nocx_snapshot_wait_ms / 1000.0 ))
        while (( EPOCHREALTIME < __deadline )); do
            if [[ -f "$__nocx_snap_file" ]]; then
                __nocx_snapshot_emit
                return 0
            fi
            # Hundredths of a second, and no file descriptors: zselect with a
            # timeout alone is a sleep that does not fork.
            zselect -t 2 2>/dev/null
        done
    else
        # Neither module: spend the whole budget in ONE sleep rather than
        # forking one per pass. Less responsive, bounded by construction —
        # which is the property that matters.
        sleep $(( __nocx_snapshot_wait_ms / 1000.0 )) 2>/dev/null
    fi
    [[ -f "$__nocx_snap_file" ]] && __nocx_snapshot_emit
    return 0
}

# Called at every prompt boundary. The FIRST prompt gives the source-time job
# a bounded grace period so a freshly opened tab is marked immediately — the
# owner's test is typing a nonexistent command before running anything, and a
# snapshot that arrives only after an unrelated command reads as a broken
# feature. On timeout the payload is left for a later prompt rather than
# delaying this one; the wait applies once per session.
__nocx_snapshot_pump() {
    [[ -n "${__nocx_snap_staging:-}" ]] || return 0
    [[ "${__nocx_snapshot_done:-0}" != "1" ]] || return 0
    if [[ -f "$__nocx_snap_file" ]]; then
        __nocx_snapshot_emit
        return 0
    fi
    [[ "${__nocx_snapshot_waiting:-0}" != "1" ]] || return 0
    __nocx_snapshot_waiting=1
    __nocx_snapshot_wait
}

# Nothing may survive the shell: a session that exits before the snapshot was
# emitted must leave no file behind. zsh's exit HOOK ARRAY is the chaining
# mechanism — bash saves the user's EXIT trap and re-runs it, while
# add-zsh-hook appends, so there is nothing here to clobber.
#
# Kill the enumeration first, then remove the files, and the ARGUMENT ORDER of
# that `rm` is what closes the rename window: a job killed mid-flight could
# otherwise mv the staging file into the final name after the final name was
# removed, and a `wait` cannot help — the job was disowned, so zsh no longer
# has it to wait for (measured: `wait` returns 127). Removing the SOURCE first
# means a pending mv fails with ENOENT and can recreate nothing.
#
# The job is disowned, so `jobs -p` cannot vouch for it either; the kill is
# guarded by a process-identity check instead — the pid plus the start time
# captured at spawn. A reaped child's pid may have been reused, and a reused
# pid has a different start time, so it is never killed.
__nocx_snapshot_cleanup() {
    if [[ -n "${__nocx_snap_job:-}" ]] && [[ -n "${__nocx_snap_lstart:-}" ]] \
        && [[ "$(ps -o lstart= -p "$__nocx_snap_job" 2>/dev/null | tr -s ' ')" == "$__nocx_snap_lstart" ]]; then
        kill -- -"$__nocx_snap_job" 2>/dev/null || kill "$__nocx_snap_job" 2>/dev/null
    fi
    if [[ -n "${__nocx_snap_staging:-}" ]]; then
        rm -f "$__nocx_snap_staging" "$__nocx_snap_file"
    fi
}
add-zsh-hook zshexit __nocx_snapshot_cleanup

# Both the job and the hello are gated on the once-per-shell latch: a second
# source must not start a second enumeration, and must not announce a session
# that has already been announced.
#
# The hello is NOT gated on the staging file. A machine whose $TMPDIR cannot
# be written to gets no snapshot, and that is all it gets: the session is
# still announced, the prompt still works, and the frontend reports command
# names as unavailable rather than reporting them wrongly.
if (( __nocx_snapshot_announce )); then
    if [[ -n "$__nocx_snap_staging" ]]; then
        # `&!` is zsh's background-and-disown in one token. Without the
        # disown zsh announces the finished job at the next prompt — "[1]  +
        # done ..." followed by the job's own implementation — in the middle
        # of the user's output. bash spells this `& ; disown`.
        __nocx_snapshot_write &!
        __nocx_snap_job=$!
        __nocx_snap_lstart="$(ps -o lstart= -p "$__nocx_snap_job" 2>/dev/null | tr -s ' ')"
    fi
    # Announce the session nonce before the first prompt.
    builtin printf '\e]636;H;%s\a' "$__nocx_snapshot_nonce"
fi

# Nested interception registration (see the mechanism note at the nested
# block). The widget replaces the accept-line widget itself — not a
# keybinding — so every accept path (^M, ^J, vi mode, other widgets that
# call accept-line) routes through it. Save the previous definition first
# (a user/framework wrapper registered before our gate): the builtin prints
# nothing under `zle -lL`, a custom widget prints its registration line.
# The registration is interactive-only: sourcing this file from a
# non-interactive context (the exec tests) must not touch zle.
#
# `zle -lL accept-line` prints `zle -N accept-line <FUNCTION>`, and a function
# is not a widget: `zle "$function"` fails unless something happened to
# register a widget of the same name. This used to take that last field and
# call it directly, so on any machine with fast-syntax-highlighting,
# zsh-syntax-highlighting or zsh-autosuggestions — which is most zsh machines —
# pressing Enter printed "No such widget `_zsh_highlight_widget_orig-…'" and the
# command did not run (nocx-wwz0; latent until a local zsh session existed to
# reach it). The previous implementation is registered under a name nocx owns
# instead, which is the only way to invoke it as a widget.
#
# Three guards, each for a case the plain form gets wrong: the `zle -N` prefix
# match refuses a completion widget (`zle -C`), whose last field is a completer
# and not an implementation; the identity check refuses OUR OWN function, which
# the launcher's deliberate second source would otherwise chain to itself; and
# the function-existence check falls back to the builtin rather than registering
# a widget whose implementation cannot be called.
#
# The saved value SURVIVES a re-source — the launcher rcfile sources this file a
# second time on purpose, and by then the widget it would find registered is our
# own — but it is re-validated first. A generation installed in ~/.nocx before
# this fix leaves a FUNCTION name in the variable, and this session's copy would
# otherwise inherit and call it; validating against the live widget table means
# a stale value degrades to the builtin accept-line (Enter works, the
# framework's wrapper is skipped for this one session) instead of to an error
# where the command does not run at all.
__nocx_old_accept_line="${__nocx_old_accept_line:-}"
if [[ -o interactive ]]; then
    zmodload zsh/zle 2>/dev/null
    zmodload zsh/zleparameter 2>/dev/null
    if [[ -n "$__nocx_old_accept_line" ]] && (( ${+widgets} )) \
        && (( ! ${+widgets[$__nocx_old_accept_line]} )); then
        __nocx_old_accept_line=
    fi
    __nocx_acc_def="$(zle -lL accept-line 2>/dev/null)"
    __nocx_acc_impl=
    case "$__nocx_acc_def" in
        "zle -N accept-line "*) __nocx_acc_impl="${__nocx_acc_def##* }" ;;
    esac
    if [[ -n "$__nocx_acc_impl" ]] && [[ "$__nocx_acc_impl" != __nocx_accept_line ]] \
        && (( ${+functions[$__nocx_acc_impl]} )); then
        zle -N __nocx_prev_accept_line "$__nocx_acc_impl"
        __nocx_old_accept_line=__nocx_prev_accept_line
    fi
    unset __nocx_acc_def __nocx_acc_impl
    zle -N accept-line __nocx_accept_line
fi
