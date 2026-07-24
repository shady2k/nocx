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

__nocx_precmd() {
    if [[ -n "$__nocx_first_prompt" ]]; then
        builtin printf '\e]133;D;%s\a' "$__nocx_exit_code"
    fi
    builtin printf '\e]133;A\a'
    builtin printf '\e]7;file://%s%s\a' \
        "$(__nocx_encode_url "${HOST%%.*}")" \
        "$(__nocx_encode_url "$PWD")"
    __nocx_first_prompt=1
}

__nocx_preexec() {
    builtin printf '\e]133;C\a'
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
            PROMPT="$__nocx_b_marker"
            PS1="$__nocx_b_marker"
            RPROMPT=''
            RPS1=''
        }
        add-zsh-hook precmd __nocx_marker_only_prompt
        # Force it last, deduped, on every source.
        precmd_functions=(${precmd_functions:#__nocx_marker_only_prompt} __nocx_marker_only_prompt)
    fi
elif [[ -z "${__nocx_prompt_wrapped:-}" ]]; then
    PS1="${PS1:-}"$'%{\e]133;B\a%}'
    __nocx_prompt_wrapped=1
fi

# Native-mode escape (nocx-4ff.9): drop the marker-only overlay and restore a
# visible prompt on the next precmd. Called by nocx when the user hits escape.
__nocx_native_mode() {
    add-zsh-hook -d precmd __nocx_marker_only_prompt 2>/dev/null
    unset NOCX_PROMPT_MODE
    PROMPT='%~ %# '
    PS1='%~ %# '
}
