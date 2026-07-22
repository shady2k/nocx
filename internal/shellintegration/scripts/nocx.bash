# nocx shell integration for bash
# Activated when NOCX_SHELL_INTEGRATION is set.
# Emits OSC 133 (A/B/C/D) command markers and OSC 7 (cwd).

if [[ -z "$NOCX_SHELL_INTEGRATION" ]]; then
    return 2>/dev/null || exit 0
fi

if [[ -n "$__nocx_loaded" ]]; then
    return 2>/dev/null || exit 0
fi
__nocx_loaded=1

__nocx_first_prompt=

__nocx_encode_url() {
    local s="$1"
    s="${s// /%20}"
    s="${s//$'\t'/%09}"
    s="${s//$'\n'/%0a}"
    builtin printf '%s' "$s"
}

__nocx_precmd() {
    local __nocx_exit=$?
    if [[ -n "$__nocx_first_prompt" ]]; then
        builtin printf '\e]133;D;%s\a' "$__nocx_exit"
    fi
    builtin printf '\e]133;A\a'
    builtin printf '\e]7;file://%s%s\a' \
        "$(__nocx_encode_url "${HOSTNAME%%.*}")" \
        "$(__nocx_encode_url "$PWD")"
    __nocx_first_prompt=1
}

__nocx_preexec() {
    builtin printf '\e]133;C\a'
}

if [[ -z "${PROMPT_COMMAND:-}" ]]; then
    PROMPT_COMMAND=__nocx_precmd
else
    __nocx_old_pc="$PROMPT_COMMAND"
    PROMPT_COMMAND='__nocx_precmd; eval "$__nocx_old_pc"'
fi

__nocx_old_debug="$(trap -p DEBUG 2>/dev/null | sed "s/^trap -- '//;s/' DEBUG$//")"
__nocx_preexec_wrapper() {
    __nocx_preexec
    if [[ -n "${__nocx_old_debug:-}" ]]; then
        eval "$__nocx_old_debug"
    fi
}
trap '__nocx_preexec_wrapper' DEBUG

if [[ -z "${__nocx_prompt_wrapped:-}" ]]; then
    PS1="${PS1}"$'\[\e]133;B\a\]'
    __nocx_prompt_wrapped=1
fi
