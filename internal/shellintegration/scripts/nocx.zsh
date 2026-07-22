# nocx shell integration for zsh
# Activated when NOCX_SHELL_INTEGRATION is set.
# Emits OSC 133 (A/B/C/D) command markers and OSC 7 (cwd).

if [[ -z "$NOCX_SHELL_INTEGRATION" ]]; then
    return 2>/dev/null || exit 0
fi

if [[ -n "$__nocx_loaded" ]]; then
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

__nocx_capture_status() {
    __nocx_exit_code=$?
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

if [[ -z "${__nocx_prompt_wrapped:-}" ]]; then
    PS1="${PS1}"$'%{\e]133;B\a%}'
    __nocx_prompt_wrapped=1
fi
