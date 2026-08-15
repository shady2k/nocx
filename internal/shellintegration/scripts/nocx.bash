# nocx shell integration for bash
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
#
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
# A user rc running under `set -a` would auto-export the bootstrap's
# assignment, publishing the capability in /proc/<pid>/environ; drop the
# export attribute explicitly (the capability is assigned exactly once, so
# this sticks — same pattern as __nocx_snapshot_nonce).
export -n __nocx_cap 2>/dev/null
# The frame bound, declared ONCE and used both in the hello this shell sends
# and in the length check below. It was two literals, and raising only the
# advertised one left the reader rejecting frames the kernel was entitled to
# send: the ssh child's grant is a whole remote launcher (~77 KiB), so the
# read failed instantly and the parent ran the user's ssh conventionally with
# no diagnostic (nocx-beib). Keep in step with lifecycle.MaxFrameBytes.
__nocx_lc_max_frame=262144
__nocx_lc_lane="${NOCX_LIFECYCLE_LANE:-}"
__nocx_lc_dom="${NOCX_LIFECYCLE_DOMAIN:-}"
__nocx_lc_epoch="${NOCX_LIFECYCLE_EPOCH:-}"
__nocx_lc_fd="${NOCX_LIFECYCLE_FD:-}"
__nocx_lc_port="${NOCX_LIFECYCLE_PORT:-}"
if [[ "${NOCX_LIFECYCLE_TIMEOUT_MS:-}" =~ ^[0-9]+$ ]] && (( NOCX_LIFECYCLE_TIMEOUT_MS >= 1 )); then
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
# How this shell answers "is the channel readable right now, consuming
# nothing" — resolved ONCE by __nocx_lc_resolve_probe, never per call. See it
# for why the answer is per-bash-version and why it is cached.
__nocx_lc_probe_mode=''
__nocx_lc_probe_helper=''

# JSON-escape one string into __nocx_lc_json_escaped. Backslash, quote and
# the C0/DEL bytes JSON forbids are escaped; raw UTF-8 passes through (the
# byte length — not the character length — is what frames are sized by, so
# __nocx_lc_send computes it under LC_ALL=C).
__nocx_lc_json_escape() {
    local s="$1" i c code hex out LC_ALL=C
    out=${s//\\/\\\\}
    out=${out//\"/\\\"}
    out=${out//$'\n'/\\n}
    out=${out//$'\t'/\\t}
    out=${out//$'\r'/\\r}
    out=${out//$'\b'/\\b}
    out=${out//$'\f'/\\f}
    # Remaining C0 (0x01-0x08, 0x0b, 0x0c, 0x0e-0x1f) and DEL break a JSON
    # string; the common escapes above already took \t \n \r \b \f, so only
    # the rare bytes reach the loop. Everything else passes through.
    if [[ "$out" == *[$'\001'-$'\010'$'\013'$'\014'$'\016'-$'\037'$'\177']* ]]; then
        for ((i = 0; i < ${#out}; i++)); do
            c="${out:i:1}"
            printf -v code '%d' "'$c"
            if (( code < 32 || code == 127 )); then
                printf -v hex '%02x' "$code"
                out="${out:0:i}\\u00${hex}${out:i+1}"
            fi
        done
    fi
    __nocx_lc_json_escaped=$out
}

# Send one envelope: 4-byte big-endian length prefix then the JSON bytes
# (protocol doc §6). Every envelope carries the full addressing tuple and
# the bearer capability; the sequence increments per envelope (doc §11).
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

# Decide, once per shell, how to ask "is the channel readable right now?"
# without consuming a byte. Consuming is not allowed: the frame's 4-byte
# length prefix is mostly NULs, `read` cannot hold a NUL, and dd must see the
# prefix whole — a probe that took one byte would corrupt the frame it was
# probing for.
#
#   builtin  bash 4.1+: `read -t 0 -N 0` is exactly this operation.
#   helper   bash 3.2 (macOS's /bin/bash) has NO such operation at all —
#            `-N` is 4.1+ and `-t 0` in 3.2 is a zero-second timeout, not a
#            poll. perl's select() is, and perl is part of the macOS base
#            system. Measured at 2ms per call, against ~2ms for the fence's
#            own od+tr, and the child inherits the descriptor with no
#            redirection (both measured on a real 3.2).
#   none     neither: the channel cannot be read, so it is never activated
#            and the session is a conventional terminal with its native
#            prompt — the honest floor, reached on no machine this product
#            ships to, and present so the refusal is stated rather than
#            silent.
#
# Cached rather than re-tested per call because the shell's version is a
# constant for the life of the process, and the refresh poll runs at EVERY
# prompt — the user's critical path.
#
# This replaced the same `read -r -t 0 -N 0` inlined at three call sites,
# which is how bash 3.2 came to hang its handshake in three places at once
# (nocx-sw4p): the hello was sent, the accept could never be read, the domain
# never activated, and every macOS bash session was a conventional terminal.
__nocx_lc_resolve_probe() {
    if (( ${BASH_VERSINFO[0]:-0} > 4 || (${BASH_VERSINFO[0]:-0} == 4 && ${BASH_VERSINFO[1]:-0} >= 1) )); then
        __nocx_lc_probe_mode=builtin
        return 0
    fi
    local __cand
    for __cand in /usr/bin/perl perl; do
        if command -v "$__cand" >/dev/null 2>&1; then
            __nocx_lc_probe_helper="$__cand"
            __nocx_lc_probe_mode=helper
            return 0
        fi
    done
    __nocx_lc_probe_mode=none
    return 1
}

# Succeeds iff a byte is available on the channel right now. Consumes
# nothing, blocks never — both arms are non-blocking by construction.
#
# The helper arm interpolates the descriptor NUMBER into a perl program, so
# __nocx_lc_fd must be digits; __nocx_lc_init's config gate is what
# guarantees that, and it is a hard requirement of this function rather than
# a nicety.
__nocx_lc_probe_readable() {
    case "$__nocx_lc_probe_mode" in
        builtin)
            LC_ALL=C IFS= read -r -t 0 -N 0 <&"$__nocx_lc_fd" 2>/dev/null
            ;;
        helper)
            # The channel arrives on the helper's STDIN, via the same `<&$fd`
            # redirection dd uses two functions down — NOT by letting the
            # helper inherit the descriptor by number.
            #
            # That distinction is the whole correctness of this arm. A
            # redirection DUPS, and a dup clears close-on-exec for the copy;
            # plain inheritance does not, so a close-on-exec channel simply
            # is not there in the helper. select() on a descriptor that is not
            # open returns -1, which is indistinguishable from "no data" — the
            # probe reports empty forever on a channel that has a frame
            # waiting. Measured: the nested child's accept sat readable on fd
            # 3 (dd took its 0000009b header immediately) while this arm
            # answered "empty" four times a second (nocx-aupk).
            #
            # It also takes the descriptor number out of the helper's program
            # text, so the poll cannot be shaped by the environment at all.
            "$__nocx_lc_probe_helper" -e \
                'vec($r,0,1)=1; exit(select($r,undef,undef,0)>0 ? 0 : 1)' \
                <&"$__nocx_lc_fd" 2>/dev/null
            ;;
        *)
            return 1
            ;;
    esac
}

# Wait for the transport to become readable, bounded by the handshake
# timeout, consuming nothing. bash's `read` cannot hold NUL bytes, so the
# binary length prefix must be read by dd|od instead. The sleep loop is the
# bound, and a closed channel fails on the first probe instead of waiting it
# out (fail-open, decision 3).
__nocx_lc_wait_readable() {
    # $1 = timeout in seconds (defaults to the handshake timeout). The
    # refresh poll passes a short bound: the prompt is the user's critical
    # path, and a partial or stalled frame must never hold it.
    #
    # The loop probes at the TOP of every round, so it runs once more AFTER
    # the last sleep: a frame that arrived during the sleep is seen, and a
    # 1-second bound is one sleep plus the probes around it — not a probe,
    # a sleep, and a premature give-up.
    local __t="${1:-$__nocx_lc_timeout_s}" __round=0
    while :; do
        if __nocx_lc_probe_readable; then
            return 0 # data is available; the caller reads it
        fi
        (( __round >= __t )) && return 1 # the bound expired: fail open
        sleep 1
        __round=$(( __round + 1 ))
    done
}

# Read one length-prefixed JSON frame into __nocx_lc_frame. Any framing
# failure (EOF, garbage, oversize) returns non-zero and the caller fails
# open: a conventional terminal is the safe direction. $1, when given, is
# the per-read timeout in seconds (the refresh poll bounds the prompt).
__nocx_lc_read_frame() {
    local __hdr __len LC_ALL=C __t="${1:-}"
    __nocx_lc_wait_readable "${__t:-$__nocx_lc_timeout_s}" || return 1
    __hdr="$(dd bs=1 count=4 2>/dev/null <&"$__nocx_lc_fd" | od -An -tx1 | tr -d ' \n')"
    [[ "$__hdr" =~ ^[0-9a-f]{8}$ ]] || return 1
    __len=$(( 16#$__hdr ))
    (( __len > 0 && __len <= __nocx_lc_max_frame )) || return 1
    __nocx_lc_wait_readable "${__t:-$__nocx_lc_timeout_s}" || return 1
    __nocx_lc_frame="$(dd bs=1 count="$__len" 2>/dev/null <&"$__nocx_lc_fd")"
    (( ${#__nocx_lc_frame} == __len )) || return 1
    return 0
}

# Answer a pending refresh_request with an authenticated snapshot (protocol
# doc §10, ADR-0024 decision 7). The kernel demands this when a framing gap
# desynchronized the domain; ONLY a snapshot answering the request restores
# authority, so this runs at every prompt and must not lose the request.
#
# The shell can only speak from a prompt — an idle shell in readline runs no
# traps (nocx-z9s9.16) — so the poll is prompt-boundary. It is non-blocking
# in the common case (`read -t 0` probes, consuming nothing): no frame
# pending, the prompt costs nothing.
# The shell names its own attempts: it mints an id per command at start —
# the app mints its own and no outbound envelope carries one back
# (protocol §8) — and the kernel learns the shell's id at attach, resolving
# it as a per-attempt alias. The snapshot reports last_completed — the
# attempt the shell just finished, with the REAL exit status — whenever one
# exists, so a completion the gap swallowed still reconciles to its real
# status instead of to unknown. active_attempt is never reported: the shell
# answers only from a prompt, where nothing is running. shell_state is
# at_prompt because this runs from a prompt; next_seq is the shell's next
# sequence, strictly greater than the snapshot's own (the kernel rejects
# `next_seq <= seq`).
# On success restores a visible prompt: a Desynchronized domain is not live
# (decision 9), and the marker-only suppression would leave an invisible
# prompt taking raw input. The shell cannot observe the snapshot's
# acceptance (no ack envelope exists), so once desynced it keeps the visible
# prompt — a visible prompt is never the failure mode; an invisible one over
# a desynchronized domain is.
__nocx_lc_ans_refresh() {
    local __rid
    # Handoff gate (nocx-u7uh.11): while a nested child is live, the child
    # owns the channel stream — the parent reads nothing (§9). The parent
    # resumes at its next prompt boundary, which is exactly when the latch
    # clears.
    if [[ "${__nocx_nested_active:-0}" == "1" ]]; then
        return 1
    fi
    if ! __nocx_lc_probe_readable; then
        return 1
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
    # status), recorded by __nocx_prompt_command before the refresh can
    # preempt the complete. When no command just finished — the shell
    # genuinely has nothing to report — the field is omitted and the kernel
    # reconciles open attempts as unknown, never success.
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
        for ((i = 0; i < 16; i++)); do
            builtin printf -v f '%s%04x' "$f" "$RANDOM"
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
    # The descriptor and the port are DIGITS or they are nothing. Both come
    # from the environment, and both are interpolated into something that
    # executes: the descriptor into the probe helper's program text, the port
    # into a /dev/tcp path. Every other field here was already pinned to a
    # shape; these two were checked only for being non-empty.
    if [[ -n "$__nocx_cap" ]] && [[ "$__nocx_cap" =~ ^[0-9a-f]{64}$ ]] \
        && [[ -n "$__nocx_lc_lane" ]] && [[ -n "$__nocx_lc_dom" ]] \
        && [[ "$__nocx_lc_epoch" =~ ^[0-9]+$ ]] \
        && [[ -z "$__nocx_lc_fd" || "$__nocx_lc_fd" =~ ^[0-9]+$ ]] \
        && [[ -z "$__nocx_lc_port" || "$__nocx_lc_port" =~ ^[0-9]+$ ]] \
        && [[ -n "$__nocx_lc_fd" || -n "$__nocx_lc_port" ]]; then
        __cfg_ok=1
    fi
    if [[ "$__cfg_ok" != "1" ]]; then
        return 1
    fi
    # Before the first read of any kind — __nocx_lc_read_frame below is one.
    if ! __nocx_lc_resolve_probe; then
        return 1
    fi
    if [[ -n "$__nocx_lc_port" ]]; then
        # Remote / in-band transport: bash network redirection. The bind
        # address is the literal 127.0.0.1, never localhost (ADR-0024).
        #
        # A FIXED high descriptor, not bash's `exec {fd}<>...` dynamic
        # allocation: the {var} form is bash 4.1+, and macOS ships bash 3.2,
        # where the same text would open a file literally named "{var}".
        # fd 200 sits above the 3-9 range user scripts and POSIX sh use.
        # The 2>/dev/null is scoped to the GROUP: for `exec` every
        # redirection is permanent, and an unscoped one would redirect the
        # shell's stderr to /dev/null for the rest of the session —
        # readline writes the prompt to stderr, so the restored native
        # prompt (decisions 8/9) would be invisible.
        if ! { exec 200<>"/dev/tcp/127.0.0.1/$__nocx_lc_port"; } 2>/dev/null; then
            return 1
        fi
        __nocx_lc_fd=200
    fi
    __nocx_lc_json_escape "$__nocx_lc_lane"
    __nocx_lc_lane_esc=$__nocx_lc_json_escaped
    __nocx_lc_json_escape "$__nocx_lc_dom"
    __nocx_lc_dom_esc=$__nocx_lc_json_escaped
    # The bundle this shell was brought up from, so the backend can record
    # which hosts carry an installation without guessing. Only the far side
    # knows it: the publish prelude installs "v<version>" when it publishes
    # and ADOPTS whatever the manifest names when a newer bundle is already
    # there. Escaped rather than trusted — the launcher's own value is a
    # validated safe name, but NOCX_GENERATION is an ordinary environment
    # variable and anyone can set it before starting a shell.
    __nocx_lc_json_escape "${NOCX_GENERATION-}"
    __nocx_lc_gen_esc=$__nocx_lc_json_escaped
    __nocx_lc_send hello ',"shell":"bash","max_frame":'"$__nocx_lc_max_frame"',"gen":"'"$__nocx_lc_gen_esc"'"'
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

# --- Nested environments (nocx-u7uh.11) ---
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
__nocx_nested_active=0
__nocx_nested_n=0
__nocx_nested_env=
__nocx_nested_host=
__nocx_nested_user=
__nocx_nested_port=0
# The ssh options the detector collected, in the order they were typed
# (nocx-c6z0). An array, because each token is one argv entry on the user's
# side and must stay one on the far side of the wire.
__nocx_nested_opts=()
__nocx_grant_bootstrap=
__nocx_nested_rc=
# Bounds the grant wait: the grant is composed synchronously by the backend
# pump (or refused with an empty bootstrap), so a few seconds is generous
# and a dead channel fails open without holding the user's command.
# Declared once per shell, not once per source: the rcfile deliberately
# re-sources the embedded script over an installer-era install (the same
# guard __nocx_snapshot_wait_ms uses), and a readonly cannot be re-declared.
if [[ -z "${__nocx_lc_grant_timeout_s:-}" ]]; then
    readonly __nocx_lc_grant_timeout_s=5
fi

# JSON unescape (bash): the grant's bootstrap is a JSON string on the wire
# (the frame is one JSON document); the shell extracts and decodes it. A
# decoding failure corrupts the rcfile, which makes the child conventional —
# the safe direction — so the decoder is best-effort by construction.
__nocx_lc_json_unescape() {
    # bash 3.2's ${var//pattern/replacement} is quadratic, and this decodes a
    # whole rcfile: measured 4655ms for 22 KiB against 8ms on bash 5, a factor
    # of 580, and the grant is larger than that. That is not a slow prompt, it
    # is a nested launch that misses its window — the user types `sudo -i` and
    # watches a frozen terminal while the shell decodes (nocx-aupk). Same
    # shape as nocx-beib, where a `${frame##*...}` scan cost 1.65s per
    # expansion; different string operation, same cliff.
    #
    # The helper resolved for the readable-probe decodes it in one pass
    # instead. The condition is the same condition — this bash is a 3.2 — so
    # it is read off the same latch rather than tested twice.
    if [[ "$__nocx_lc_probe_mode" == "helper" ]]; then
        __nocx_lc_json_unescape_helper "$1" && return 0
        # Falling through on helper failure is deliberate: a corrupted decode
        # makes the child conventional, which is the safe direction, but a
        # SLOW correct decode is better than a wrong one.
    fi
    __nocx_lc_json_unescape_native "$1"
}

# One left-to-right pass, which is also what makes it correct: a lone `\\`
# followed by `u0041` is a literal backslash then the text "u0041", and any
# decoder that resolves \uXXXX in a separate earlier pass gets that wrong.
# The trailing sentinel survives command substitution's newline stripping —
# the bootstrap legitimately ends in a newline, and losing it truncates the
# rcfile's last line.
__nocx_lc_json_unescape_helper() {
    local __out
    # The program is q{}/qq{} throughout so it can live inside a shell
    # single-quoted string without a quote of its own: one stray ' would end
    # the shell quoting and the rest would be parsed as shell.
    __out="$(
        builtin printf '%s' "$1" | "$__nocx_lc_probe_helper" -e '
            local $/; my $s = <STDIN>;
            my %m = (q{"},q{"}, q{\\},q{\\}, q{/},q{/}, q{b},qq{\b}, q{f},qq{\f},
                     q{n},qq{\n}, q{r},qq{\r}, q{t},qq{\t});
            $s =~ s{\\(?:u([0-9a-fA-F]{4})|(.))}
                   {defined $1 ? chr(hex $1) : (exists $m{$2} ? $m{$2} : $2)}gse;
            utf8::encode($s) if utf8::is_utf8($s);
            print $s, qq{\1};
        ' 2>/dev/null
    )" || return 1
    case "$__out" in
        *$'\1') : ;;
        *) return 1 ;; # no sentinel: the helper died mid-write
    esac
    __nocx_lc_json_unescaped="${__out%$'\1'}"
    return 0
}

__nocx_lc_json_unescape_native() {
    local s="$1" LC_ALL=C hex oct
    # Protect literal backslashes (\\ in JSON) before the single-char
    # escapes: a literal backslash must not be consumed by the \" or \n
    # passes below. The marker is DOMAIN-scoped: the payload being decoded
    # is itself shell source that CONTAINS this function's text, so any
    # fixed marker (like the payload's own __NOCX_BS__ references) would
    # collide with itself; the runtime domain value appears nowhere in the
    # payload's static text.
    s="${s//\\\\/__NOCX_BS_${__nocx_lc_dom}__}"
    s="${s//\\\"/\"}"
    s="${s//\\n/$'\n'}"
    s="${s//\\t/$'\t'}"
    s="${s//\\r/$'\r'}"
    s="${s//\\b/$'\b'}"
    s="${s//\\f/$'\f'}"
    s="${s//\\\//\/}"
    # \uXXXX is rare in the rcfile (printable ASCII dominates); convert it
    # to octal so one printf %b pass below decodes it with the rest.
    while [[ "$s" =~ \\u([0-9a-fA-F]{4}) ]]; do
        hex="${BASH_REMATCH[1]}"
        printf -v oct '%03o' "$((16#$hex))"
        s="${s//\\u$hex/\\$oct}"
    done
    printf -v s '%b' "$s"
    s="${s//__NOCX_BS_${__nocx_lc_dom}__/\\}"
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
        if ! __nocx_lc_probe_readable; then
            sleep 1
            __t=$(( __t + 1 ))
            continue
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
        # The strips are %? (one char) — a pattern of } would be read by
        # bash as an empty pattern plus a literal brace, stripping nothing.
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
    # at most one username.
    if [[ "$__line" =~ ^su([[:space:]]+.*)?$ ]] && [[ "$__line" != *[\;\|\&\<\>\`]* ]] \
        && [[ ! "$__line" =~ -c([[:space:]]|$) ]]; then
        local __rest="${__line#su}" __tok __users=0
        for __tok in $__rest; do
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
    if [[ "$__line" =~ ^ssh([[:space:]]+.*)?$ ]] && [[ "$__line" != *[\;\|\&\<\>\`]* ]]; then
        local -a __toks
        read -r -a __toks <<< "$__line"
        local __i __tok __dest="" __skip=0 __want_port=0 __want_opt=0
        # The options are COLLECTED, not merely tolerated. The backend
        # rebuilds the command line from what this sends, so an option that
        # is recognised here and not carried is an option the user typed and
        # the shell then ran without: `ssh -i ~/.ssh/prod -J bastion host`
        # went out as a bare `ssh host`, with the block still showing the
        # line they typed (nocx-c6z0).
        #
        # -p is not collected because it is modelled as the port, and -t/-tt
        # are not because the composer adds its own -t and ssh reads a second
        # one as -tt — a different request from the one the user made.
        __nocx_nested_opts=()
        for ((__i = 1; __i < ${#__toks[@]}; __i++)); do
            __tok="${__toks[$__i]}"
            if (( __skip )); then
                (( __want_port )) && __nocx_nested_port="$__tok"
                (( __want_opt )) && __nocx_nested_opts[${#__nocx_nested_opts[@]}]="$__tok"
                __want_port=0
                __want_opt=0
                __skip=0
                continue
            fi
            case "$__tok" in
                -t|-tt) : ;;
                -4|-6|-v|-C|-x|-X) __nocx_nested_opts[${#__nocx_nested_opts[@]}]="$__tok" ;;
                -p) __skip=1; __want_port=1 ;;
                -l|-o|-i|-F|-J|-e|-b|-c|-m)
                    __nocx_nested_opts[${#__nocx_nested_opts[@]}]="$__tok"
                    __skip=1; __want_opt=1 ;;
                -*) return 1 ;; # an option we do not model: refuse
                *) [[ -n "$__dest" ]] && return 1; __dest="$__tok" ;;
            esac
        done
        # An option whose argument never arrived (`ssh -i` and nothing after
        # it) is a line ssh itself would refuse. Refusing it here keeps the
        # collected list and the typed line the same thing.
        (( __skip )) && return 1
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
# not a version table, and do it BEFORE emitting start/domain_request: a
# negative answer leaves the line untouched for the normal DEBUG path to run
# exactly once. `--help` needs no authentication; the option spelling is
# stable even when the surrounding prose is localized.
__nocx_sudo_supports_preserve_fds() {
    local __help
    __help="$(LC_ALL=C command env -u BASHOPTS sudo --help 2>&1)" || true
    case "$__help" in
        *--preserve-fds*) return 0 ;;
        *) return 1 ;;
    esac
}

# Request the child domain and launch it. Runs from the DEBUG-trap wrapper,
# which returns 1 (skipping the user's original command) only when this
# returns 0. The launch BLOCKS here for the child's whole lifetime — the
# parent shell is inside this call while the child owns the stream, which is
# exactly the handoff interval §9 names.
__nocx_nested_launch() {
    local __line="$1" __rid __extra __nocx_boot_fd=0 __nocx_boot_w
    __nocx_nested_detect "$__line" || return 1
    if [[ "$__nocx_nested_env" == "sudo" ]] && ! __nocx_sudo_supports_preserve_fds; then
        return 1
    fi
    __rid="r-$__nocx_lc_dom-$(( __nocx_nested_n++ ))"
    __extra='"request":"'"$__rid"'","env":"'"$__nocx_nested_env"'"'
    if [[ "$__nocx_nested_env" == "ssh" ]]; then
        __extra+=',"host":"'"$__nocx_nested_host"'"'
        [[ -n "$__nocx_nested_user" ]] && __extra+=',"user":"'"$__nocx_nested_user"'"'
        (( __nocx_nested_port != 0 )) && __extra+=',"port":'"$__nocx_nested_port"
        # The options the user typed, so the composer can rebuild the line
        # they actually asked for (nocx-c6z0). Escaped one token at a time:
        # an ssh option argument is an arbitrary path or config string, and
        # the composer shell-quotes each one on the far side.
        #
        # The ${#arr[@]} guard is not decoration: under `set -u`, bash 3.2 —
        # still /bin/bash on macOS — treats an EMPTY array expansion as an
        # unbound variable, so the loop below must not be reached at all when
        # nothing was collected.
        if (( ${#__nocx_nested_opts[@]} > 0 )); then
            local __opt __opts_json=''
            for __opt in "${__nocx_nested_opts[@]}"; do
                __nocx_lc_json_escape "$__opt"
                __opts_json+=',"'"$__nocx_lc_json_escaped"'"'
            done
            __extra+=',"opts":['"${__opts_json#,}"']'
        fi
    fi
    __nocx_lc_send domain_request ",$__extra" || return 1
    if ! __nocx_lc_read_grant "$__rid"; then
        return 1 # no grant: channel dead or refused without a reply
    fi
    # The child's hello requires the parent Suspended (§9) — never exec the
    # child before this frame is written.
    __nocx_lc_send domain_suspended
    __nocx_nested_active=1
    if [[ "$__nocx_nested_env" == "ssh" ]]; then
        # The bootstrap is the backend-composed rewritten line (ADR-0022):
        # the -R reverse forward plus the in-band payload piped into ssh -t.
        eval "$__nocx_grant_bootstrap"
        __nocx_nested_rc=$?
    elif [[ -n "$__nocx_grant_bootstrap" ]]; then
        # Same machine: stage the child's rcfile into a preserved descriptor
        # and launch. The child reads it via --rcfile /dev/fd/4 (ADR-0024's
        # preferred answer: the capability never enters a filesystem
        # object); fd 3 is the inherited lifecycle channel, preserved so the
        # child speaks over the SAME transport as the parent.
        # Stage the rcfile into the preserved descriptor with NO writer
        # race: bash reads a pipe rcfile chunk-at-a-time, so a child that
        # starts before the writer finishes gets a truncated rcfile — the
        # exact failure the top-level launcher solved with a file, which
        # ADR-0024 forbids here (the per-epoch capability never enters a
        # filesystem object). The coproc lets the PARENT wait for the
        # writer and close the write end (EOF) before launching; the child
        # then reads the complete rcfile. bash 3.2 (macOS) has no coproc:
        # the substitution's single fast printf is the fallback, and a
        # truncated read fails open — the child is a conventional shell and
        # the parent re-activates at its next prompt (§9's stillborn
        # interval), never a hung session.
        # A redirect on a commandless `exec` is PERMANENT (the script's
        # own warning at the /dev/tcp group) — the coproc's exec must not
        # carry a 2>/dev/null, or the parent's stderr (and the child's
        # inherited copy) would go to /dev/null for the rest of the
        # session. The coproc's own printf redirect is scoped inside its
        # braces instead.
        # bash's coproc and {var} fds are CLOSE-ON-EXEC (measured), so the
        # preserved descriptor must be a LITERAL fd in the free single-digit
        # range (4-9, the POSIX-sh guarantee), opened FRESH from the coproc's
        # read end via /dev/fd/N — an OPEN never sets CLOEXEC, where a dup
        # (N<&src) preserves the source's. A busy user fd is never clobbered
        # (the /dev/fd check first).
        # The `coproc NAME { ...; }` form is bash 4.0+ SYNTAX, and a version
        # test cannot guard syntax: bash parses a function body whole before
        # it runs a line of it, so 3.2 rejected the file at this token and
        # every shell on macOS started with no integration at all. It is
        # `eval`ed for that reason and no other — the string is parsed only
        # when this branch is actually taken, which 3.2 never does. Same rule
        # for `exec {var}>&-` below (bash 4.1+).
        __nocx_stage_ok=0
        __nocx_boot_fd=0
        if (( ${BASH_VERSINFO[0]:-0} > 4 || (${BASH_VERSINFO[0]:-0} == 4 && ${BASH_VERSINFO[1]:-0} >= 1) )); then
            eval "coproc __nocx_boot { builtin printf '%s' \"\$__nocx_grant_bootstrap\" 2>/dev/null; }"
            for __nocx_cand in 4 5 6 7 8 9; do
                if [[ ! -e /dev/fd/$__nocx_cand ]]; then
                    case $__nocx_cand in
                        4) if exec 4</dev/fd/${__nocx_boot[0]}; then __nocx_boot_fd=4; fi ;;
                        5) if exec 5</dev/fd/${__nocx_boot[0]}; then __nocx_boot_fd=5; fi ;;
                        6) if exec 6</dev/fd/${__nocx_boot[0]}; then __nocx_boot_fd=6; fi ;;
                        7) if exec 7</dev/fd/${__nocx_boot[0]}; then __nocx_boot_fd=7; fi ;;
                        8) if exec 8</dev/fd/${__nocx_boot[0]}; then __nocx_boot_fd=8; fi ;;
                        9) if exec 9</dev/fd/${__nocx_boot[0]}; then __nocx_boot_fd=9; fi ;;
                    esac
                    if (( __nocx_boot_fd != 0 )); then
                        # Close the parent's copy of the write end — EOF for
                        # the child — then wait for the writer: the pipe is
                        # complete before the child ever reads it.
                        __nocx_boot_w="${__nocx_boot[1]}"
                        eval 'exec {__nocx_boot_w}>&-'
                        wait "$__nocx_boot_PID" 2>/dev/null
                        __nocx_stage_ok=1
                    fi
                    break
                fi
            done
        else
            # bash 3.2 (macOS): no coproc. The substitution's single fast
            # printf is the fallback (the writer race can truncate — the
            # child then fails open conventionally, §9's stillborn
            # interval); the fd is still a checked-free literal.
            for __nocx_cand in 4 5 6 7 8 9; do
                if [[ ! -e /dev/fd/$__nocx_cand ]]; then
                    case $__nocx_cand in
                        4) if exec 4< <(builtin printf '%s' "$__nocx_grant_bootstrap"); then __nocx_boot_fd=4; fi ;;
                        5) if exec 5< <(builtin printf '%s' "$__nocx_grant_bootstrap"); then __nocx_boot_fd=5; fi ;;
                        6) if exec 6< <(builtin printf '%s' "$__nocx_grant_bootstrap"); then __nocx_boot_fd=6; fi ;;
                        7) if exec 7< <(builtin printf '%s' "$__nocx_grant_bootstrap"); then __nocx_boot_fd=7; fi ;;
                        8) if exec 8< <(builtin printf '%s' "$__nocx_grant_bootstrap"); then __nocx_boot_fd=8; fi ;;
                        9) if exec 9< <(builtin printf '%s' "$__nocx_grant_bootstrap"); then __nocx_boot_fd=9; fi ;;
                    esac
                    break
                fi
            done
            if (( __nocx_boot_fd != 0 )); then
                __nocx_stage_ok=1
            fi
        fi
        if (( __nocx_stage_ok == 1 )); then
            # su has no --preserve-fds: the whole launch rests on the rcfile
            # descriptor surviving su's own exec. Measured/verified 2026-08-09
            # (nocx-u7uh.30): util-linux su (v2.42.2, login-utils/su-common.c
            # run_shell) and shadow su (4.19.4, execve_shell) end in a plain
            # execv/execve with no fd sweep, BSD/macOS su (FreeBSD lineage) is
            # the same, and the real shadow su on this host preserved fd 7
            # through the exact launcher line — but NONE of them promise
            # preservation in a man page; it is an incidental property of plain
            # exec. The fallback when one does not preserve: the child bash
            # cannot read its rcfile, starts as a conventional shell (measured:
            # bash silently ignores the unreadable --rcfile), never
            # establishes, and the parent stillborn-activates at its next
            # prompt — asserted by the fd-closed su test.
            if [[ "$__nocx_nested_env" == "sudo" ]]; then
                env -u BASHOPTS sudo --preserve-fds=3,$__nocx_boot_fd -i env -u BASH_ENV -u BASHOPTS bash --rcfile /dev/fd/$__nocx_boot_fd -i
            else
                env -u BASHOPTS su -l -c 'env -u BASH_ENV -u BASHOPTS bash --rcfile /dev/fd/'"$__nocx_boot_fd"' -i'
            fi
            __nocx_nested_rc=$?
        else
            # Cannot stage (no free fd): run the command conventionally — the
            # child is a plain sudo/su session and the parent still
            # activates at its next prompt.
            eval "$__line"
            __nocx_nested_rc=$?
        fi
    else
        # The grant refused (empty bootstrap): run conventionally.
        eval "$__line"
        __nocx_nested_rc=$?
    fi
    return 0
}

__nocx_first_prompt=
__nocx_in_prompt_command=0
# Latch so the command-start (C) marker fires once per entered line, not once
# per simple command — a pipeline or list fires the DEBUG trap for each element.
#
# Initialised DISARMED (1), not armed (0): the DEBUG trap is live from the
# moment `trap ... DEBUG` runs below, and the remaining lines of THIS sourced
# script (and the rest of .bashrc after it) are ordinary commands — e.g. the
# `[[ ... ]]` tests below do not match the `__nocx_*` skip. Armed, the very
# first such test fires a spurious C, driving the input machine to RUNNING_RAW
# before the first A→B ever arrives; the first real prompt is then untrusted
# and the DOM editor never takes ownership until a command has run once
# (nocx-4ff: "editor appears only after the first command"). __nocx_precmd arms
# the latch (=0) at each prompt, so the first genuine command line still fires C.
__nocx_preexec_done=1

__nocx_encode_url() {
    local s="$1"
    s="${s// /%20}"
    s="${s//$'\t'/%09}"
    s="${s//$'\n'/%0a}"
    builtin printf '%s' "$s"
}

# The exit status is passed in as $1: the caller captures $? before any other
# command (even an assignment) can clobber it.
__nocx_precmd() {
    local __nocx_exit="$1"
    if [[ -n "$__nocx_first_prompt" ]]; then
        __nocx_marker D "$__nocx_exit"
    fi
    __nocx_marker A
    builtin printf '\e]7;file://%s%s\a' \
        "$(__nocx_encode_url "${HOSTNAME%%.*}")" \
        "$(__nocx_encode_url "$PWD")"
    __nocx_first_prompt=1
    # Arm the command-start marker for the next command line.
    __nocx_preexec_done=0
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

__nocx_preexec() {
    # $1 = the user's command line, captured by the DEBUG trap BEFORE this
    # function ran — inside the function $BASH_COMMAND would be our own
    # current command, not the user's.
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

# The lifecycle channel died mid-session: a send failed at a prompt
# boundary (local fd closed, or the forwarded socket broken). Clear the
# active latch — the domain is lost and nothing more may be emitted over the
# dead transport — and mark the session recovered so the prompt-boundary arm
# restores a visible native prompt with the one-shot recovery fence (ADR-
# 0024 decision 8). nocx matches that fence and acknowledges the
# restoration; until it lands, the session is neither an authenticated
# terminal nor a usable conventional one.
__nocx_lc_recover() {
    __nocx_lc_active=0
    __nocx_lc_recovered=1
}

# In marker-only mode __nocx_prompt_command runs the user/framework
# PROMPT_COMMAND first, then emits D/A/OSC 7, then sets PS1 to the
# marker-only B prompt as the final action — so a hostile framework
# PROMPT_COMMAND that rewrites PS1 cannot win. In baseline mode the
# original order is preserved (precmd first, then old PC).
__nocx_prompt_command() {
    # Capture the just-finished command's status FIRST — the assignment below
    # would otherwise reset $? to 0 before __nocx_precmd could read it. A
    # nested child whose command the DEBUG trap skipped leaves $? = 0 (the
    # skip is not the child's exit); the child's real status was captured
    # right after the launch and overrides here.
    #
    # THESE TWO LINES ARE ORDERED, and the order is the fix for nocx-678o.
    # extdebug makes the DEBUG trap fire inside functions, so it fires for
    # every line of this one — and the wrapper suppresses that with two
    # guards: the command text starting `__nocx_`, and __nocx_in_prompt_command.
    # The capture used to be `local __nocx_exit=$?`, which satisfies NEITHER:
    # its text begins with `local`, and the flag was set four lines further
    # down. That leaves exactly one unguarded command at the top of the prompt
    # cycle, and one moment where the C-marker latch is still armed — after an
    # INTERRUPT, where the user ran nothing and __nocx_precmd armed the latch
    # at the previous prompt. So Ctrl-C at a prompt announced nocx's own line
    # to the kernel as the user's command: an OSC 133 C, a `start` naming
    # `local __nocx_exit=$?`, and a `complete` carrying SIGINT's 130.
    # Reproduced identically on bash 3.2 and 5.x.
    #
    # A global for the capture, so its own text matches the `__nocx_*` skip;
    # the flag goes up immediately after, so everything below is covered by
    # the flag instead. `local` cannot come first: it would reset $?.
    __nocx_prompt_exit=$?
    __nocx_in_prompt_command=1
    local __nocx_exit=$__nocx_prompt_exit
    if [[ -n "${__nocx_nested_rc:-}" ]]; then
        __nocx_exit=$__nocx_nested_rc
        __nocx_nested_rc=
    fi
    # --- Authenticated channel: activate, refresh, complete, fence, prompt_ready ---
    if [[ "${__nocx_lc_active:-0}" == "1" ]]; then
        # The child closed (the parent was blocked inside the launch); the
        # parent owns the stream again. Activation MUST precede the complete
        # — a completion for a suspended domain is rejected, and only an
        # authenticated activation restores the parent (§9).
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
            __nocx_lc_last_completed_code="$__nocx_exit"
        fi
        # A framing gap may have desynchronized the domain while the shell
        # was busy; the kernel's refresh_request is buffered. Answer it
        # FIRST — only a snapshot answering it restores authority (decision
        # 7), and while the domain is desynchronized the complete and
        # prompt_ready below would be quarantined anyway.
        if __nocx_lc_ans_refresh; then
            # The refresh was answered; the desync also ended the marker-only
            # suppression (decision 9), so PS1 below takes the visible arm.
            __nocx_lc_attempt_open=0
        elif [[ "${__nocx_lc_attempt_open:-0}" == "1" ]]; then
            # Complete the attempt: the exit status and a fresh fence nonce,
            # and write the SAME nonce to the pty after the command's output
            # — the render-order rendezvous (decision 1 carve-out, doc §8).
            # The complete carries no attempt id; the kernel resolves the
            # domain's single open attempt.
            if __nocx_lc_fence; then
                if __nocx_lc_send complete ',"exit_code":'"$__nocx_exit"',"fence":"'"$__nocx_lc_fence_hex"'"'; then
                    builtin printf '\e]1337;NOCX_FENCE;%s\a' "$__nocx_lc_fence_hex"
                else
                    __nocx_lc_recover
                fi
            fi
            __nocx_lc_attempt_open=0
        fi
        # The editor may own keys only at a ready prompt; the kernel rejects
        # prompt_ready while any attempt is open. A failed send means the
        # transport is dead — the domain is lost, the visible native prompt
        # must be restored (decision 8), and no further send is attempted
        # this boundary (recover cleared the active latch).
        if [[ "${__nocx_lc_active:-0}" == "1" ]]; then
            __nocx_lc_send prompt_ready || __nocx_lc_recover
        fi
    fi
    if [[ "${NOCX_PROMPT_MODE:-}" == "marker-only" ]] && [[ "${__nocx_arm_marker_only:-}" == 1 ]]; then
        # Top-level session: arm the marker-only overlay.
        # 1) run the user/framework prompt command FIRST.
        if [[ -n "${__nocx_old_pc_arr+x}" ]]; then
            local __c
            for __c in "${__nocx_old_pc_arr[@]}"; do eval "$__c"; done
        elif [[ -n "${__nocx_old_pc:-}" ]]; then
            eval "$__nocx_old_pc"
        fi
        # 2) emit D/A/OSC7.
        __nocx_precmd "$__nocx_exit"
        # 3) set the marker-only prompt as the FINAL action — and only when
        # the authenticated channel is live: suppressing the native prompt
        # without a live domain is the phishing primitive decision 9
        # forbids. Not live, the framework's prompt stands visible, with the
        # render-only B partition marker appended exactly as baseline mode
        # wraps it (the marker suppresses nothing by itself).
        if [[ "${__nocx_lc_active:-0}" == "1" ]]; then
            if [[ "${__nocx_lc_desynced:-0}" == "1" ]]; then
                # decision 9: a Desynchronized domain is not live. The
                # suppressed marker-only prompt would be an invisible prompt
                # taking raw input — restore a visible native prompt until
                # resynchronization succeeds. The shell cannot observe the
                # snapshot's acceptance (no ack envelope exists), so the
                # visible prompt stays: a visible prompt is never the
                # failure mode; an invisible one over a desynchronized
                # domain is.
                PS1='\w \$ '"$__nocx_b_marker"
            else
                PS1="$__nocx_b_marker"
            fi
        elif [[ "${__nocx_lc_recovered:-0}" == 1 ]]; then
            # The channel died mid-session (a send failed): a visible native
            # prompt stands, never a suppressed one taking raw input
            # (decision 8). The one-shot recovery fence rides exactly the
            # FIRST prompt's bytes — nocx matches it and acknowledges the
            # restoration; afterwards PS1 is rebuilt without it, so the
            # nonce reaches the terminal once and is never reused.
            __nocx_native_mode
            if [[ "${__nocx_lc_recovery_emitted:-0}" != 1 ]] && [[ -n "${__nocx_lc_recovery:-}" ]]; then
                PS1="${PS1}"'\[\e]1337;NOCX_RECOVERY;'"${__nocx_lc_recovery}"'\a\]'
                __nocx_lc_recovery_emitted=1
            fi
            PS1="${PS1}${__nocx_b_marker}"
        else
            PS1="${PS1}${__nocx_b_marker}"
        fi
    else
        __nocx_precmd "$__nocx_exit"
        if [[ -n "${__nocx_old_pc_arr+x}" ]]; then
            local __c
            for __c in "${__nocx_old_pc_arr[@]}"; do eval "$__c"; done
        elif [[ -n "${__nocx_old_pc:-}" ]]; then
            eval "$__nocx_old_pc"
        fi
    fi

    # Command-existence snapshot (OSC 636): the background compgen started at
    # source time (below). The FIRST prompt gives it a bounded grace period
    # so a freshly opened tab is marked immediately — the owner's test is
    # typing a nonexistent command before running anything, and a snapshot
    # that arrives only after an unrelated command reads as a broken feature.
    # On timeout (compgen takes seconds on NFS) the snapshot is left for a
    # later prompt rather than delaying this one; the wait applies once per
    # session. The payload is only ever written from a prompt, while the
    # shell is the sole writer to the tty, so it cannot interleave with
    # command output.
    if [[ -n "${__nocx_snap_staging:-}" ]] && [[ "${__nocx_snapshot_done:-0}" != "1" ]]; then
        if [[ -f "$__nocx_snap_file" ]]; then
            __nocx_snapshot_emit
        elif [[ "${__nocx_snapshot_waiting:-0}" != "1" ]]; then
            __nocx_snapshot_waiting=1
            # The bound is on ELAPSED TIME, not on a number of iterations.
            #
            # This loop used to count: ten passes of `sleep 0.025`, adding 25 to
            # a counter each time. That assumed a sleep costs what it asks for.
            # `sleep` is not a builtin — every pass forks and execs — and on a
            # loaded machine the fork dominates: CI measured a first prompt held
            # for 1.319s by a bound this file documents as 250 ms. The user's
            # prompt is what pays, which makes it a broken promise rather than a
            # slow test.
            if [[ -n "${EPOCHREALTIME:-}" ]]; then
                # bash 5: a monotonic-enough clock with no fork. The substitution
                # strips the decimal separator to get microseconds; the class
                # covers locales where that separator is a comma.
                local __nocx_deadline=$(( ${EPOCHREALTIME//[.,]/} + __nocx_snapshot_wait_ms * 1000 ))
                while (( ${EPOCHREALTIME//[.,]/} < __nocx_deadline )); do
                    if [[ -f "$__nocx_snap_file" ]]; then
                        __nocx_snapshot_emit
                        break
                    fi
                    sleep 0.025
                done
            else
                # bash 3.2 — still /bin/bash on macOS — has no clock that does
                # not cost a fork, so reading the time to bound the loop would
                # spend the thing being bounded. Spend the whole budget in ONE
                # sleep instead: less responsive than polling, and bounded by
                # construction, which is the property that was missing.
                sleep 0.25
                if [[ -f "$__nocx_snap_file" ]]; then
                    __nocx_snapshot_emit
                fi
            fi
        fi
    fi
    __nocx_in_prompt_command=0
}

if [[ -z "${PROMPT_COMMAND:-}" ]]; then
    PROMPT_COMMAND='__nocx_prompt_command'
elif [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == declare\ -a* ]]; then
    # Array form: save and replace.
    eval "__nocx_old_pc_arr=(\"\${PROMPT_COMMAND[@]}\")"
    PROMPT_COMMAND='__nocx_prompt_command'
else
    __nocx_old_pc="$PROMPT_COMMAND"
    PROMPT_COMMAND='__nocx_prompt_command'
fi

# Save the original DEBUG trap so we can chain to it after our preexec hook.
__nocx_old_debug="$(trap -p DEBUG 2>/dev/null | sed "s/^trap -- '//;s/' DEBUG$//")"

# extdebug (nocx-u7uh.11): with it, a DEBUG trap returning non-zero SKIPS
# the next command. That is the nested-environment interception: the parent
# detects `sudo -i` / `su -` / `ssh host` in its preexec hook, launches the
# child (which runs to completion inside the trap), and returns 1 so the
# original command never also runs. The wrapper therefore returns 0 on
# every non-nested path — explicitly, because extdebug would otherwise let a
# non-zero return from the USER's own old DEBUG trap start skipping their
# commands. It never returns 2 (which extdebug turns into a synthetic
# `return` from the enclosing function). extdebug also makes the DEBUG trap
# fire inside functions and command substitutions; the __nocx_* /
# in_prompt_command / preexec_done guards below are exactly what keeps that
# from recursing.
shopt -s extdebug

__nocx_preexec_wrapper() {
    local __nocx_current_command=${BASH_COMMAND}
    # Fire the command-start marker once per entered line. Skip our own
    # internal commands, anything that runs while servicing PROMPT_COMMAND,
    # every command after the first (the DEBUG trap fires per simple
    # command, so a pipeline/list would otherwise emit several C markers),
    # and commands inside the nested launch itself.
    if [[ "$__nocx_current_command" != __nocx_* ]] \
        && [[ "${__nocx_in_prompt_command:-0}" != "1" ]] \
        && [[ "${__nocx_in_nested_launch:-0}" != "1" ]] \
        && [[ "${__nocx_preexec_done:-0}" != "1" ]]; then
        __nocx_preexec_done=1
        __nocx_preexec "$__nocx_current_command"
        # Nested environment: the parent requests a child domain and runs
        # the child here, inside the trap. Returning 1 skips the original
        # command (extdebug), so the child runs exactly once; the parent
        # shell survives and re-activates at its next prompt (§9).
        __nocx_in_nested_launch=1
        __nocx_nested_launch "$__nocx_current_command"
        local __nested_rc=$?
        __nocx_in_nested_launch=0
        if (( __nested_rc == 0 )); then
            return 1 # the launch ran; skip the original command
        fi
    fi
    # Chain to the previous DEBUG trap, if any.
    if [[ -n "${__nocx_old_debug:-}" ]]; then
        eval "$__nocx_old_debug"
    fi
    # Explicit 0 on every non-nested path (see the extdebug note above).
    return 0
}
trap '__nocx_preexec_wrapper' DEBUG
__nocx_b_marker='\[\e]133;B\a\]'

if [[ "${NOCX_PROMPT_MODE:-}" != "marker-only" ]] || [[ "${__nocx_arm_marker_only:-}" != 1 ]]; then
    # Baseline mode or nested marker-only (nocx-4ff.13): wrap PS1 with
    # the B marker so the prompt is visible. Top-level marker-only leaves
    # PS1 untouched — __nocx_prompt_command sets it at runtime.
    if [[ -z "${__nocx_prompt_wrapped:-}" ]]; then
        # Use ANSI-C quoting with doubled backslashes so \[ and \] are emitted
        # literally; they tell bash that the OSC sequence is non-printing.
        PS1="${PS1:-}"$'\\[\e]133;B\\a\\]' 
        __nocx_prompt_wrapped=1
    fi
fi

# Nested-session gate (nocx-4ff.13): record the owning session at source
# time so child shells see the guard and keep a visible prompt.
# ALSO capture owner-ness into __nocx_arm_marker_only before the export,
# so __nocx_prompt_command can distinguish owner from nested descendant.
if [[ "${NOCX_PROMPT_MODE:-}" == "marker-only" ]] && [[ -z "${__nocx_owned_session:-}" ]]; then
    __nocx_owned_session="${NOCX_SESSION_ID:-}"
    export __nocx_owned_session
    __nocx_arm_marker_only=1
fi

#   OSC 636 ; S ; <nonce> ; <names> ST          snapshot; <names> is
#                                               `;`-joined and hex-escaped
#                                               (\\ for backslash, \xHH for
#                                               control/C1 bytes and ';')
#   OSC 636 ; H ; <nonce> ST                    session hello — the FIRST 636
#                                               message, before any command
# The nonce is a per-session secret generated here: any process can print an
# OSC — a command's own output can forge a snapshot — so the frontend
# discards any payload that does not carry the established nonce. It is
# emitted at source time, before the first prompt, when no user command has
# run; the frontend accepts exactly one hello, so a forged re-hello cannot
# re-anchor the nonce.
#
# compgen -c | sort -u measures ~37 ms on this machine and can take seconds
# on NFS, so it must never sit in front of the prompt. The snapshot is
# computed in a background job started at SOURCE time — the old reason for
# deferring it to the first prompt was "the environment is final only once
# the rc has finished", weighed and rejected: the gate line is appended at
# the END of ~/.bashrc, so in the common case sourcing already happens last
# and commands defined after it are missed either way. Starting early is what
# makes a freshly opened tab mark commands before the user runs anything.
# The payload is emitted from a prompt — the only moment the shell is the
# sole writer to the tty — so it can never interleave with other output. One
# snapshot per session; staleness is deliberately a later problem
# (per-prompt fingerprints cost the same enumeration they were meant to
# save).
#
# The snapshot is staged in a mktemp file whose name carries no secret — the
# nonce must never appear in a path, in any argv, or exported — and mode 600
# from creation. An EXIT trap (chained, like the DEBUG trap) removes the
# staging and final files even when the shell exits before the snapshot was
# emitted, and the final name only exists after the atomic mv, so a prompt
# can never read a partial payload.
__nocx_gen_nonce() {
    # 32 hex chars from the kernel RNG; RANDOM+$$ fallback if od is missing.
    local n
    n="$(od -An -N16 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n')"
    if [[ -n "$n" ]]; then
        builtin printf '%s' "$n"
    else
        builtin printf '%04x%04x%04x%04x' "$RANDOM" "$RANDOM" "$RANDOM" "$RANDOM"
    fi
}
# ONCE PER SHELL, NOT ONCE PER SOURCE — the same rule, and the same reason,
# as __nocx_snapshot_wait_ms one screen above.
#
# The launcher rcfile deliberately unsets __nocx_loaded and sources this
# script a second time, so the session's authenticated copy installs over the
# installer-era one the user's ~/.bashrc already sourced. That is EVERY local
# enhanced session, not an edge case. Minting a fresh nonce on the second
# pass emitted a SECOND hello, and the renderer's forgery defence is
# "exactly one hello per session" (command-snapshot.ts): it keeps the first
# nonce and discards the second. The snapshot is then emitted from a prompt
# carrying the second nonce, fails the match, and is discarded — so the store
# stays `unavailable` for the life of the session and command completion
# never learns a single command name. Observed directly: two H frames with
# different nonces, then one 6699-byte S frame that no longer matched.
#
# The renderer is right and is not the place to fix it — accepting a re-hello
# is exactly the re-anchoring the rule exists to prevent. The script simply
# must not claim to be a new session when it is the same shell.
#
# `__nocx_loaded` cannot serve as the latch: the rcfile unsets it on purpose,
# which is what makes the second pass happen at all.
#
# The latch is the PID, not merely the nonce's presence, and that distinction
# is load-bearing. "Is the variable set" cannot tell a RE-SOURCE from an
# INHERITED value, and the two need opposite answers: a re-source must stay
# quiet, a child shell must announce itself. A nonce that leaked into the
# environment — a user rc under `set -a` auto-exports every assignment, which
# is the hazard the `export -n` below exists for — would otherwise silence a
# legitimately new session for good: no hello, so no snapshot is ever
# accepted, and completion is dead with nothing said. Measured, on a child
# shell started with the nonce exported: zero hello frames, zero snapshots.
#
# That would also be a fail-CLOSED degrade, which is the wrong direction
# everywhere else in this file. $$ is the shell's own pid and differs in any
# child, so a child mints its own nonce however it inherited the variable,
# and a re-source of the same shell does not.
if [[ "${__nocx_snapshot_owner:-}" != "$$" ]] || [[ -z "${__nocx_snapshot_nonce:-}" ]]; then
    __nocx_snapshot_nonce="$(__nocx_gen_nonce)"
    __nocx_snapshot_owner=$$
    __nocx_snapshot_announce=1
else
    __nocx_snapshot_announce=0
fi
export -n __nocx_snapshot_owner
# A user rc running under `set -a` would auto-export every assignment,
# publishing the nonce in /proc/<pid>/environ; drop the export attribute
# explicitly (the nonce is assigned exactly once, so this sticks).
export -n __nocx_snapshot_nonce
# The snapshot is staged in a mktemp file whose name carries NO secret: the
# nonce must never appear in a path (ls /tmp is world-readable), in any argv
# (ps reads argv), or exported (/proc/<pid>/environ) — the nonce is the whole
# forgery defence of OSC 636. mktemp creates the file mode 600 from the
# start (no create-then-chmod window) with O_EXCL (no symlink pre-emption).
# The atomic mv to the .snap name below is what tells a later prompt the
# payload is complete; the .snap name inherits the staging file's 600 mode.
#
# Staged once per shell for the same reason as the nonce, and this half
# matters independently: a second mktemp would repoint __nocx_snap_file at a
# name the FIRST pass's background job is never going to write, so even a
# correctly-nonced snapshot would never be found at a prompt — and the first
# staging file would leak, because the EXIT trap only knows the latest name.
if (( __nocx_snapshot_announce )); then
    __nocx_snap_staging="$(mktemp "${TMPDIR:-/tmp}/nocx-snap.XXXXXX" 2>/dev/null)"
    __nocx_snap_file="${__nocx_snap_staging:+${__nocx_snap_staging}.snap}"
    __nocx_snapshot_done=0
fi

# How long the FIRST prompt waits for the source-time snapshot job before
# degrading to the later-prompt schedule. 250 ms: fast when compgen is local,
# bounded when it is not (NFS); the prompt never waits longer than this, and
# only once per session.
#
# The __nocx_ prefix is not decoration: this script is sourced INTO the user's
# interactive shell, so every name it defines is a name the user no longer
# has. A readonly one is worse — it cannot even be unset, so a collision
# breaks their shell for the rest of the session with no way back.
#
# NOCX_SNAPSHOT_WAIT_MS overrides it, and exists for the tests. 250 ms is a
# budget for a HUMAN's prompt, so a test that asserts the snapshot mechanism
# works is really asserting that the machine finished compgen inside a UX
# deadline — which a loaded CI runner does not, and a user's laptop does. The
# override lets such a test state a budget of its own and go on testing the
# mechanism. A non-numeric value falls back to the default rather than
# breaking the arithmetic in someone's prompt.
#
# Declared once per shell, not once per source. The launcher rcfile
# deliberately unsets __nocx_loaded and sources this script again so a
# session's authenticated copy installs over an installer-era one the user's
# ~/.bashrc already sourced — which is EVERY local enhanced session, because
# the app writes that gate line itself. A readonly cannot be unset and cannot
# be re-declared, so re-declaring printed "__nocx_snapshot_wait_ms: readonly
# variable" into the user's terminal as the first thing they saw
# (nocx-u7uh.22). The value is identical on both passes, so keeping the first
# is not a compromise; readonly still holds for the rest of the session.
if [[ -z "${__nocx_snapshot_wait_ms:-}" ]]; then
    if [[ "${NOCX_SNAPSHOT_WAIT_MS:-}" =~ ^[0-9]+$ ]]; then
        readonly __nocx_snapshot_wait_ms="$NOCX_SNAPSHOT_WAIT_MS"
    else
        readonly __nocx_snapshot_wait_ms=250
    fi
fi

# Nothing may survive the shell: a session that exits before the snapshot was
# emitted (the leak path) must leave no file behind. Kill the background
# compgen first — it could otherwise mv the .snap name into place AFTER the
# rm below — then remove both files, then chain the shell's pre-existing EXIT
# trap, the same pattern the DEBUG trap above uses.
#
# The job is disowned (see the start below) so it prints no job-control
# notification, which means `jobs -p` can no longer vouch for it. The kill is
# guarded by a process-identity check instead: the job's PID plus the start
# time captured at spawn, read back via `ps -o lstart=` at exit. A reaped
# child's PID may have been reused — a reused PID has a different start time,
# so it is never killed.
__nocx_old_exit="$(trap -p EXIT 2>/dev/null | sed "s/^trap -- '//;s/' EXIT$//")"
__nocx_exit_cleanup() {
    # The DEBUG trap fires for every simple command below (and inside trap
    # handlers on some bash versions); mark the exit path as "in a prompt
    # command" so the wrapper suppresses any spurious OSC 133 C.
    __nocx_in_prompt_command=1
    # Tell the kernel the domain is ending (best-effort: the transport may
    # already be gone, and the kernel marks open attempts unknown on close).
    # No explicit fd close: the process exit closes it, which is what the
    # adapter reads as the transport ending.
    if [[ "${__nocx_lc_active:-0}" == "1" ]]; then
        __nocx_lc_send domain_closed
        __nocx_lc_active=0
    fi
    if [[ -n "${__nocx_snap_job:-}" ]] && [[ -n "${__nocx_snap_lstart:-}" ]] \
        && [[ "$(ps -o lstart= -p "$__nocx_snap_job" 2>/dev/null | tr -s ' ')" == "$__nocx_snap_lstart" ]]; then
        # Group-kill when the job is a process-group leader (interactive job
        # control): that also stops the pipeline's compgen/sort/build, which a
        # plain kill of the subshell would orphan. In scripts (no job control)
        # the group form fails harmlessly and the fallback kills the subshell.
        kill -- -"$__nocx_snap_job" 2>/dev/null || kill "$__nocx_snap_job" 2>/dev/null
        # wait closes the rename-in-flight window: after it returns the job is
        # dead, so no mv can recreate the .snap name after the rm below.
        wait "$__nocx_snap_job" 2>/dev/null
    fi
    if [[ -n "${__nocx_snap_staging:-}" ]]; then
        rm -f "$__nocx_snap_staging" "$__nocx_snap_file"
    fi
    if [[ -n "${__nocx_old_exit:-}" ]]; then
        eval "$__nocx_old_exit"
    fi
}
trap '__nocx_exit_cleanup' EXIT

# Hex-escape one command name into the global payload accumulator, appending
# a `;` separator. Bytes that would break or fake the OSC sequence are
# escaped as \xHH; backslash is \\ (VS Code's scheme). Raw UTF-8 (>= 0xa0)
# passes through — the terminal decodes the byte stream, and escaping every
# byte would double the payload for no safety.
__nocx_encode_hex_into() {
    local s="$1" i c code hex LC_ALL=C
    # Fast path: a name needing no escaping is appended whole.
    #
    # This is the difference between making the first prompt's grace and
    # missing it. The loop below runs per CHARACTER with a `printf -v` in it,
    # and a normal PATH is ~700 names of ~10 characters — about 7000 forks'
    # worth of builtin work, measured at ~85ms of the ~104ms the whole
    # snapshot pipeline took in the e2e container (compgen and sort together
    # were 17ms). The grace is 250ms and there is no second chance at it: a
    # shell idle in readline runs no traps, so a job that misses the window
    # waits for a prompt a fresh tab never produces (nocx-z9s9.16).
    #
    # The test is a strict subset of what the loop passes through unchanged —
    # printable ASCII under LC_ALL=C is 32..126, and the two bytes with
    # meaning in the payload are excluded explicitly — so this cannot encode
    # anything differently, only faster. Everything else still takes the loop:
    # control bytes, DEL, 127..159, and UTF-8 (non-printable in C).
    if [[ "$s" != *[![:print:]]* && "$s" != *'\'* && "$s" != *';'* ]]; then
        __nocx_payload+="$s;"
        return
    fi
    for ((i = 0; i < ${#s}; i++)); do
        c="${s:i:1}"
        if [[ "$c" == '\' ]]; then
            __nocx_payload+='\\'
        elif [[ "$c" == ';' ]]; then
            __nocx_payload+='\x3b'
        else
            builtin printf -v code '%d' "'$c"
            (( code < 0 )) && (( code += 256 ))
            if (( code < 32 || (code >= 127 && code <= 159) )); then
                builtin printf -v hex '%02x' "$code"
                __nocx_payload+="\\x$hex"
            else
                __nocx_payload+="$c"
            fi
        fi
    done
    __nocx_payload+=';'
}

# Fill __nocx_payload with the hex-escaped, `;`-joined names, capped at
# 8192 names and 65536 encoded characters. Returns non-zero when the list is
# empty — an empty snapshot must never reach the frontend: "every command is
# unknown" is the same lie as "every command exists", pointing the other way.
__nocx_snapshot_build() {
    __nocx_payload=''
    local line n=0 before LC_ALL=C
    while IFS= read -r line; do
        before=${#__nocx_payload}
        __nocx_encode_hex_into "$line"
        if (( ${#__nocx_payload} > 65536 )); then
            __nocx_payload="${__nocx_payload:0:before}"
            break
        fi
        n=$((n + 1))
        if (( n >= 8192 )); then
            break
        fi
    done
    # Emit the payload on stdout — the caller redirects it into the temp file.
    if [[ -n "$__nocx_payload" ]]; then
        builtin printf '%s' "$__nocx_payload"
        return 0
    fi
    return 1
}

# Emit the finished snapshot once and remove the staging files. Only ever
# called from __nocx_prompt_command — the shell is the sole writer to the
# tty there, so the payload cannot interleave with command output.
#
# It has to be a prompt, and that is a constraint rather than a preference:
# a shell idle in readline runs no traps at all. Measured on bash 5.2 and
# 5.3, a SIGUSR1 raised while the user is sitting at a prompt does not run
# its handler until the next command is submitted, and SIGWINCH does not
# flush it either — so there is no way for the shell to speak on its own
# (nocx-z9s9.16).
__nocx_snapshot_emit() {
    __nocx_snapshot_done=1
    __nocx_payload="$(< "$__nocx_snap_file")"
    builtin printf '\e]636;S;%s;%s\a' "$__nocx_snapshot_nonce" "$__nocx_payload"
    rm -f "$__nocx_snap_staging" "$__nocx_snap_file"
}

# The snapshot job starts HERE, at source time — not at the first prompt —
# so a freshly opened tab is marked before the user runs anything (see the
# protocol comment above for why the late start was rejected). The first
# prompt grants a bounded grace period (__nocx_snapshot_wait_ms).
#
# That grace is the ONLY automatic delivery point, which is why the encoder
# above was made cheap: bash idle in readline runs no traps, so a job that
# misses this window waits for a prompt a fresh tab may never produce
# (nocx-z9s9.16).
#
# Both the job and the hello are gated on the once-per-shell latch: a second
# source must not start a second enumeration, and must not announce a session
# that has already been announced. See the latch's own comment for what the
# second hello cost.
if (( __nocx_snapshot_announce )); then
    ( compgen -c 2>/dev/null | LC_ALL=C sort -u | __nocx_snapshot_build \
        >| "$__nocx_snap_staging" 2>/dev/null \
        && mv -f "$__nocx_snap_staging" "$__nocx_snap_file" ) &
    __nocx_snap_job=$!
    # Record the job's identity for the EXIT trap (PID + start time), then
    # disown: without disown, bash prints "[N]+ Done ( compgen ... )" — the
    # job's implementation, verbatim — into the user's output when it finishes.
    # Only OUR job is disowned, so no other job loses its notification.
    __nocx_snap_lstart="$(ps -o lstart= -p "$__nocx_snap_job" 2>/dev/null | tr -s ' ')"
    disown "$__nocx_snap_job" 2>/dev/null

    # Announce the session nonce before the first prompt.
    builtin printf '\e]636;H;%s\a' "$__nocx_snapshot_nonce"
fi

# Restore a visible native prompt. Real caller: the prompt-boundary arm's
# recovered branch (ADR-0024 decision 8) — after the lifecycle channel dies
# mid-session, the user must never be left at a suppressed prompt taking raw
# input, which is the worst of both. The older nocx-4ff.9 "user hits escape"
# attribution had no caller and is deleted: the escape surface it described
# no longer exists. PS1 is rebuilt fresh here, so a framework PROMPT_COMMAND
# that rewrote PS1 cannot win after a loss — the same ordering the
# marker-only arm guarantees while live.
__nocx_native_mode() {
    PS1='\w \$ '
}
