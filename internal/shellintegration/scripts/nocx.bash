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
        builtin printf '\e]133;D;%s\a' "$__nocx_exit"
    fi
    builtin printf '\e]133;A\a'
    builtin printf '\e]7;file://%s%s\a' \
        "$(__nocx_encode_url "${HOSTNAME%%.*}")" \
        "$(__nocx_encode_url "$PWD")"
    __nocx_first_prompt=1
    # Arm the command-start marker for the next command line.
    __nocx_preexec_done=0
}

__nocx_preexec() {
    builtin printf '\e]133;C\a'
}

# In marker-only mode __nocx_prompt_command runs the user/framework
# PROMPT_COMMAND first, then emits D/A/OSC 7, then sets PS1 to the
# marker-only B prompt as the final action — so a hostile framework
# PROMPT_COMMAND that rewrites PS1 cannot win. In baseline mode the
# original order is preserved (precmd first, then old PC).
__nocx_prompt_command() {
    # Capture the just-finished command's status FIRST — the assignment below
    # would otherwise reset $? to 0 before __nocx_precmd could read it.
    local __nocx_exit=$?
    __nocx_in_prompt_command=1
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
        # 3) set the marker-only prompt as the FINAL action.
        PS1="$__nocx_b_marker"
    elif [[ "${NOCX_PROMPT_MODE:-}" == "marker-only" ]]; then
        # Nested session (nocx-4ff.13): keep a visible prompt via baseline path.
        __nocx_precmd "$__nocx_exit"
        if [[ -n "${__nocx_old_pc_arr+x}" ]]; then
            local __c
            for __c in "${__nocx_old_pc_arr[@]}"; do eval "$__c"; done
        elif [[ -n "${__nocx_old_pc:-}" ]]; then
            eval "$__nocx_old_pc"
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

__nocx_preexec_wrapper() {
    local __nocx_current_command=${BASH_COMMAND}
    # Fire the command-start marker once per entered line. Skip our own
    # internal commands, anything that runs while servicing PROMPT_COMMAND, and
    # every command after the first (the DEBUG trap fires per simple command,
    # so a pipeline/list would otherwise emit several C markers).
    if [[ "$__nocx_current_command" != __nocx_* ]] \
        && [[ "${__nocx_in_prompt_command:-0}" != "1" ]] \
        && [[ "${__nocx_preexec_done:-0}" != "1" ]]; then
        __nocx_preexec_done=1
        __nocx_preexec
    fi
    # Chain to the previous DEBUG trap, if any.
    if [[ -n "${__nocx_old_debug:-}" ]]; then
        eval "$__nocx_old_debug"
    fi
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

# Native-mode escape (nocx-4ff.9): restore a visible prompt.
__nocx_native_mode() {
    unset NOCX_PROMPT_MODE
    PS1='\w \$ '
}
