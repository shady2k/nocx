# Native fish hooks. The launcher consumes the private startup pipe before
# native configuration and supplies global, unexported credentials. Never reset
# those globals here. The authenticated channel, not terminal OSCs, owns state.

function __nocx_lc_random_hex
    builtin set -l fish_trace
    builtin set -l value (command od -An -N32 -tx1 /dev/urandom 2>/dev/null | builtin string replace -ar '\s' '' | builtin string join '')
    builtin string match -qr '^[0-9a-f]{64}$' -- "$value"; or return 1
    builtin printf '%s' "$value"
end

function __nocx_byte_length
    builtin set -l fish_trace
    if builtin string match -qr '^[\x00-\x7f]*$' -- "$argv[1]"
        builtin string length -- "$argv[1]"
        return
    end
    # URL encoding exposes each UTF-8 byte as either ASCII or one %HH token.
    # Collapsing those tokens gives a byte count using only fish builtins.
    builtin string escape --style=url -- "$argv[1]" | builtin string replace -ar '%[0-9a-fA-F]{2}' x | builtin string length
end

function __nocx_lc_send
    builtin set -l fish_trace
    builtin test (builtin count $argv) -ge 1; or return 2
    builtin set -l event "$argv[1]"
    builtin set -e argv[1]
    builtin set -g __nocx_lc_seq (builtin math "$__nocx_lc_seq + 1")
    builtin printf '%s\0' /v number 1 /lane string "$__nocx_lc_lane" \
        /dom string "$__nocx_lc_dom" /epoch number "$__nocx_lc_epoch" \
        /seq number "$__nocx_lc_seq" /cap string "$__nocx_cap" \
        /evt string "$event" $argv | command "$NOCX_AGENT_HELPER_PATH" fish-channel send --fd "$__nocx_lc_fd" 2>/dev/null
    builtin set -l codes $pipestatus
    if builtin test "$codes[1]" -ne 0; or builtin test "$codes[2]" -ne 0
        __nocx_fail_open
        return 1
    end
end

function __nocx_lc_read_frame
    builtin set -l fish_trace
    # Clearing first means even a failed poll cannot expose an earlier reply.
    builtin set -g __nocx_lc_reply_keys
    builtin set -g __nocx_lc_reply_types
    builtin set -g __nocx_lc_reply_values
    builtin set -g __nocx_lc_value ''
    builtin set -l wait_ms 5000
    builtin test (builtin count $argv) -eq 0; or builtin set wait_ms "$argv[1]"
    # The status is appended INSIDE the producing block. Checking the status of
    # string split0 alone would accidentally commit partial failed-child output.
    # split0 must remain the last substitution command: it preserves embedded
    # newlines and empty values without evaluating any received text.
    builtin set -l fields (begin
        command "$NOCX_AGENT_HELPER_PATH" fish-channel receive --fd "$__nocx_lc_fd" --wait-ms "$wait_ms" 2>/dev/null
        builtin set -l child_status $status
        builtin printf '%s\0' '' __nocx_channel_status "$child_status"
    end | builtin string split0)
    builtin set -l size (builtin count $fields)
    if builtin test "$size" -lt 3; or builtin test "$fields[-2]" != __nocx_channel_status
        __nocx_fail_open
        return 1
    end
    builtin set -l child_status "$fields[-1]"
    if builtin test "$child_status" != 0
        if builtin test "$child_status" = 3
            return 3
        end
        __nocx_fail_open
        return "$child_status"
    end
    builtin set -e fields[-3..-1]
    builtin set size (builtin count $fields)
    if builtin test "$size" -lt 3; or builtin test (builtin math "$size % 3") -ne 0
        __nocx_fail_open
        return 1
    end
    builtin set -l keys
    builtin set -l types
    builtin set -l values
    builtin set -l i 1
    while builtin test "$i" -le "$size"
        builtin set -l j (builtin math "$i + 1")
        builtin set -l k (builtin math "$i + 2")
        if not builtin contains -- "$fields[$j]" string number bool null object array
            __nocx_fail_open
            return 1
        end
        builtin set -a keys "$fields[$i]"
        builtin set -a types "$fields[$j]"
        builtin set -a values "$fields[$k]"
        builtin set i (builtin math "$i + 3")
    end
    if builtin test "$keys[1]" != ''; or builtin test "$types[1]" != object; or builtin test "$values[1]" != ''
        __nocx_fail_open
        return 1
    end
    builtin set -g __nocx_lc_reply_keys $keys
    builtin set -g __nocx_lc_reply_types $types
    builtin set -g __nocx_lc_reply_values $values
end

function __nocx_lc_field
    builtin set -l fish_trace
    builtin set -g __nocx_lc_value ''
    builtin test (builtin count $argv) -ge 1; or return 1
    builtin set -l wanted_type string
    builtin test (builtin count $argv) -lt 2; or builtin set wanted_type "$argv[2]"
    builtin set -l i (builtin contains --index -- "$argv[1]" $__nocx_lc_reply_keys)
    builtin test -n "$i"; or return 1
    builtin test "$__nocx_lc_reply_types[$i]" = "$wanted_type"; or return 1
    builtin set -g __nocx_lc_value "$__nocx_lc_reply_values[$i]"
end

function __nocx_lc_reply_matches
    builtin set -l fish_trace
    __nocx_lc_field /v number; and builtin test "$__nocx_lc_value" = 1; or return 1
    __nocx_lc_field /lane; and builtin test "$__nocx_lc_value" = "$__nocx_lc_lane"; or return 1
    __nocx_lc_field /dom; and builtin test "$__nocx_lc_value" = "$__nocx_lc_dom"; or return 1
    __nocx_lc_field /epoch number; and builtin test "$__nocx_lc_value" = "$__nocx_lc_epoch"; or return 1
end

function __nocx_lc_ans_refresh
    builtin set -l fish_trace
    # Inspect the CURRENT frame, including when called from a launch wait.
    # Only prompt polling reads a new frame; never swallow the following reply.
    __nocx_lc_reply_matches; or return 1
    __nocx_lc_field /evt; and builtin test "$__nocx_lc_value" = refresh_request; or return 1
    __nocx_lc_field /request; or return 1
    builtin set -l request "$__nocx_lc_value"
    builtin string match -qr '^req-[0-9a-f]{16}$' -- "$request"; or return 1
    builtin set -l extra /request string "$request" /shell_state string at_prompt
    if builtin test "$__nocx_in_command" = 1; and builtin test "$__nocx_lc_attempt_open" = 1
        builtin set extra /request string "$request" /shell_state string running /active_attempt string "$__nocx_lc_attempt_id"
    end
    if builtin test -n "$__nocx_lc_last_completed_id"
        builtin set -a extra /last_completed object '' \
            /last_completed/attempt string "$__nocx_lc_last_completed_id" \
            /last_completed/exit_code number "$__nocx_lc_last_completed_code"
    end
    builtin set -a extra /next_seq number (builtin math "$__nocx_lc_seq + 2")
    __nocx_lc_send snapshot $extra; or return 1
    if builtin test "$__nocx_in_command" != 1
        builtin set -g __nocx_lc_attempt_open 0
    end
end

function __nocx_lc_poll
    builtin set -l fish_trace
    # Bound stale-reply draining as well as each individual receive.
    builtin set -l i 0
    while builtin test "$i" -lt 32
        builtin set i (builtin math "$i + 1")
        __nocx_lc_read_frame 0
        builtin set -l result $status
        builtin test "$result" -ne 3; or return 0
        builtin test "$result" -eq 0; or return 1
        __nocx_lc_ans_refresh
        builtin test "$__nocx_lc_active" = 1; or return 1
    end
end

function __nocx_fail_open
    builtin set -l fish_trace
    builtin set -g __nocx_lc_active 0
    builtin set -g __nocx_lc_attempt_open 0
    if builtin test "$__nocx_lc_hello_sent" = 1
        builtin set -g __nocx_lc_recovered 1
    end
    builtin set -g __nocx_selected_prompt __nocx_native_prompt
    builtin set -g __nocx_selected_right_prompt __nocx_native_right_prompt
    builtin set -g __nocx_selected_mode_prompt __nocx_native_mode_prompt
end

function __nocx_recovery_observation
    builtin set -l fish_trace
    if builtin test "$__nocx_lc_recovered" = 1; and builtin test "$__nocx_lc_recovery_emitted" != 1
        if builtin string match -qr '^[0-9a-f]{64}$' -- "$__nocx_recovery"
            builtin printf '\e]1337;NOCX_RECOVERY;%s\a' "$__nocx_recovery"
            builtin set -g __nocx_lc_recovery_emitted 1
        end
    end
end

# The user's function MUST be the first command, with its argv intact. In
# particular, neither a local assignment nor a command substitution may precede
# it: fish supplies the user's original status AND pipestatus at this boundary.
# Direct calls also preserve prompt functions' global side effects. Dispatchers
# select redirection without opening /dev/stdout and bypassing fish's IO chain.
function __nocx_native_prompt
    __nocx_saved_fish_prompt $argv
    builtin set -l result $status
    builtin set -l fish_trace
    __nocx_recovery_observation
    builtin printf '\e]133;B\a'
    return $result
end

function __nocx_hidden_prompt
    __nocx_saved_fish_prompt $argv >/dev/null
    builtin set -l result $status
    builtin set -l fish_trace
    builtin printf '\e]133;B\a'
    return $result
end

function __nocx_native_right_prompt
    __nocx_saved_fish_right_prompt $argv
end

function __nocx_hidden_right_prompt
    __nocx_saved_fish_right_prompt $argv >/dev/null
end

function __nocx_native_mode_prompt
    __nocx_saved_fish_mode_prompt $argv
end

function __nocx_hidden_mode_prompt
    __nocx_saved_fish_mode_prompt $argv >/dev/null
end

function __nocx_prompt_dispatch
    $__nocx_selected_prompt $argv
end

function __nocx_right_prompt_dispatch
    $__nocx_selected_right_prompt $argv
end

function __nocx_mode_prompt_dispatch
    $__nocx_selected_mode_prompt $argv
end

function __nocx_install_prompts
    builtin set -l fish_trace
    # Force native autoload before copying. Frameworks can replace a prompt at
    # startup or in an event handler; retain their newest definition, not ours.
    for name in fish_prompt fish_right_prompt fish_mode_prompt
        builtin functions "$name" >/dev/null 2>/dev/null
        if not builtin functions --query "$name"
            continue
        end
        builtin set -l dispatcher __nocx_prompt_dispatch
        switch "$name"
            case fish_right_prompt
                builtin set dispatcher __nocx_right_prompt_dispatch
            case fish_mode_prompt
                builtin set dispatcher __nocx_mode_prompt_dispatch
        end
        builtin set -l definition (builtin functions "$name" | builtin string collect)
        # Copying changes the declared name and adds source metadata. Compare
        # the executable body, not those headers, to recognize our dispatcher.
        builtin set -l body (builtin string match -r '\$__nocx_selected_(?:right_|mode_)?prompt \$argv' -- "$definition")
        if builtin test (builtin count $body) -eq 0
            builtin functions --erase __nocx_saved_$name
            builtin functions --copy "$name" __nocx_saved_$name; or continue
            builtin functions --erase "$name"
            builtin functions --copy "$dispatcher" "$name"
        end
    end
    builtin set -g __nocx_selected_prompt __nocx_native_prompt
    builtin set -g __nocx_selected_right_prompt __nocx_native_right_prompt
    builtin set -g __nocx_selected_mode_prompt __nocx_native_mode_prompt
    if builtin test "$__nocx_lc_active" = 1; and builtin test "$__nocx_prompt_owner" = "$fish_pid"; and builtin test "$NOCX_PROMPT_MODE" = marker-only
        builtin set -g __nocx_selected_prompt __nocx_hidden_prompt
        builtin set -g __nocx_selected_right_prompt __nocx_hidden_right_prompt
        builtin set -g __nocx_selected_mode_prompt __nocx_hidden_mode_prompt
    end
end

function __nocx_snapshot
    builtin set -l fish_trace
    builtin test "$__nocx_snapshot_done" != 1; or return 0
    builtin set -l random (__nocx_lc_random_hex)
    builtin test -n "$random"; or return 1
    builtin set -g -u __nocx_snapshot_nonce (builtin string sub -l 32 -- "$random")
    builtin set -l names (builtin --names) (builtin functions --names) \
        '!' and begin break case command continue else end exec for function if in not or return switch time while
    # Encode the native tables in one builtin pipeline, not a command
    # substitution/function call for every name and every unusual character.
    # URL escaping turns each unsafe UTF-8 byte into %HH; the OSC codec's \xHH
    # spelling represents the same byte. Everything left is printable ASCII.
    builtin set -l encoded_names (builtin string escape --style=url -- $names | builtin string replace -ar '%([0-9a-fA-F]{2})' '\\\\x$1')
    builtin set -l lengths (builtin string length -- $encoded_names)
    builtin set -l emitted
    builtin set -l n 0
    builtin set -l payload_bytes 0
    for encoded in $encoded_names
        builtin set n (builtin math "$n + 1")
        builtin set payload_bytes (builtin math "$payload_bytes + $lengths[$n] + 1")
        builtin test "$payload_bytes" -le 65536; or break
        builtin set -a emitted "$encoded"
        builtin test "$n" -lt 4096; or break
    end
    builtin test (builtin count $emitted) -gt 0; or return 1
    builtin set -l payload (builtin string join ';' -- $emitted)
    builtin printf '\e]636;H;%s\a\e]636;S;%s;%s;\a' "$__nocx_snapshot_nonce" "$__nocx_snapshot_nonce" "$payload"
    builtin set -g __nocx_snapshot_done 1
end

function __nocx_preexec
    builtin set -l fish_trace
    builtin test "$__nocx_owner_pid" = "$fish_pid"; or return
    builtin test (builtin count $argv) -gt 0; or return
    builtin string match -qr '\S' -- "$argv[1]"; or return
    builtin set -g __nocx_in_command 1
    builtin printf '\e]133;C\a'
    if builtin test "$__nocx_lc_active" = 1
        builtin set -g __nocx_lc_attempt_n (builtin math "$__nocx_lc_attempt_n + 1")
        builtin set -g __nocx_lc_attempt_id "s-$__nocx_lc_dom-$__nocx_lc_attempt_n"
        # Four thousand bytes, not four thousand Unicode code points. Keep a
        # whole character when dropping the tail of a long native fish command.
        builtin set -l text (builtin string sub -l 4000 -- "$argv[1]" | builtin string collect --allow-empty)
        builtin set -l bytes (__nocx_byte_length "$text")
        while builtin test "$bytes" -gt 4000
            builtin set -l length (builtin string length -- "$text")
            builtin set -l drop (builtin math "ceil(($bytes - 4000) / 4)")
            builtin set text (builtin string sub -l (builtin math "$length - $drop") -- "$text" | builtin string collect --allow-empty)
            builtin set bytes (__nocx_byte_length "$text")
        end
        if __nocx_lc_send start /attempt string "$__nocx_lc_attempt_id" /command string "$text"
            builtin set -g __nocx_lc_attempt_open 1
        end
    end
end

function __nocx_postexec
    builtin set -l saved_status $status $pipestatus
    builtin set -l fish_trace
    builtin test "$__nocx_owner_pid" = "$fish_pid"; or return
    builtin set -g __nocx_exit_code "$saved_status[1]"
    builtin set -g __nocx_exit_pipestatus $saved_status[2..-1]
    builtin set -g __nocx_in_command 0
    # Cleanup can itself receive a refresh, so publish the real observation
    # before any launch-owned wait, not merely before the complete send.
    if builtin test "$__nocx_lc_attempt_open" = 1
        builtin set -g __nocx_lc_last_completed_id "$__nocx_lc_attempt_id"
        builtin set -g __nocx_lc_last_completed_code "$__nocx_exit_code"
    end
    # Both are launch-owned, and deliberately also run after interrupted fish
    # functions. The real command status was captured before any cleanup.
    __nocx_finish_launch
    __nocx_restore_parent_domain
    if builtin test "$__nocx_lc_active" = 1; and builtin test "$__nocx_lc_attempt_open" = 1
        __nocx_lc_poll
        if builtin test "$__nocx_lc_active" = 1; and builtin test "$__nocx_lc_attempt_open" = 1
            builtin set -l fence (__nocx_lc_random_hex)
            if builtin test -n "$fence"; and __nocx_lc_send complete /attempt string "$__nocx_lc_attempt_id" /exit_code number "$__nocx_exit_code" /fence string "$fence"
                builtin printf '\e]1337;NOCX_FENCE;%s\a' "$fence"
            else
                __nocx_fail_open
            end
            builtin set -g __nocx_lc_attempt_open 0
        end
    end
    builtin printf '\e]133;D;%s\a' "$__nocx_exit_code"
    return "$saved_status[1]"
end

function __nocx_prompt_event
    builtin set -l saved_status $status $pipestatus
    builtin set -l fish_trace
    builtin test "$__nocx_owner_pid" = "$fish_pid"; or return
    if builtin test "$__nocx_lc_active" = 1
        __nocx_lc_poll
        if builtin test "$__nocx_lc_active" = 1
            __nocx_lc_send prompt_ready
        end
    end
    builtin printf '\e[J\e]133;A\a'
    builtin set -l host (builtin string escape --style=url -- "$hostname")
    builtin set -l directory (builtin string escape --style=url -- "$PWD" | builtin string replace -a '%2F' /)
    builtin printf '\e]7;file://%s%s\a' "$host" "$directory"
    __nocx_snapshot
    __nocx_install_prompts
    return "$saved_status[1]"
end

function __nocx_shell_exit
    builtin set -l saved_status $status
    builtin set -l fish_trace
    builtin test "$__nocx_owner_pid" = "$fish_pid"; or return
    __nocx_finish_launch
    __nocx_restore_parent_domain
    if builtin test "$__nocx_lc_active" = 1
        __nocx_lc_send domain_closed
    end
    builtin set -g __nocx_lc_active 0
    return "$saved_status"
end

function __nocx_initialize
    builtin set -l fish_trace
    if builtin set -q __nocx_owner_pid; and builtin test "$__nocx_owner_pid" = "$fish_pid"
        return
    end
    # An ordinary nested fish inherits public NOCX_* metadata, never the
    # credentials or this non-exported ownership. It must not steal the lane.
    builtin set -g -u __nocx_owner_pid "$fish_pid"
    builtin set -g __nocx_lc_active 0
    builtin set -g __nocx_lc_seq 0
    builtin set -g __nocx_lc_attempt_open 0
    builtin set -g __nocx_lc_attempt_n 0
    builtin set -g __nocx_lc_attempt_id ''
    builtin set -g __nocx_lc_last_completed_id ''
    builtin set -g __nocx_lc_last_completed_code ''
    builtin set -g __nocx_lc_hello_sent 0
    builtin set -g __nocx_lc_recovered 0
    builtin set -g __nocx_lc_recovery_emitted 0
    builtin set -g __nocx_lc_timeout_s 5
    builtin set -g __nocx_lc_grant_timeout_s 5
    builtin set -g __nocx_lc_lane "$NOCX_LIFECYCLE_LANE"
    builtin set -g __nocx_lc_dom "$NOCX_LIFECYCLE_DOMAIN"
    builtin set -g __nocx_lc_epoch "$NOCX_LIFECYCLE_EPOCH"
    builtin set -g __nocx_lc_fd "$NOCX_LIFECYCLE_FD"
    builtin set -g __nocx_in_command 0
    builtin set -g __nocx_exit_code 0
    builtin set -g __nocx_snapshot_done 0
    builtin set -g __nocx_prompt_owner ''
    if not builtin set -q __nocx_owned_session
        builtin set -g __nocx_prompt_owner "$fish_pid"
        builtin set -gx __nocx_owned_session "$NOCX_SESSION_ID"
    end
    builtin set -g __nocx_selected_prompt __nocx_native_prompt
    builtin set -g __nocx_selected_right_prompt __nocx_native_right_prompt
    builtin set -g __nocx_selected_mode_prompt __nocx_native_mode_prompt
    if builtin set -q __nocx_cap; and builtin set -q __nocx_recovery
        builtin set -g -u __nocx_cap "$__nocx_cap"
        builtin set -g -u __nocx_recovery "$__nocx_recovery"
        if builtin string match -qr '^[0-9a-f]{64}$' -- "$__nocx_cap"; and builtin test -n "$__nocx_lc_lane"; and builtin test -n "$__nocx_lc_dom"; and builtin string match -qr '^[0-9]+$' -- "$__nocx_lc_epoch"; and builtin string match -qr '^[0-9]+$' -- "$__nocx_lc_fd"; and builtin test -x "$NOCX_AGENT_HELPER_PATH"
            builtin set -g __nocx_lc_hello_sent 1
            if __nocx_lc_send hello /shell string fish /max_frame number 262144 /gen string "$NOCX_GENERATION"
                if __nocx_lc_read_frame
                    if __nocx_lc_reply_matches; and __nocx_lc_field /evt; and builtin test "$__nocx_lc_value" = accept
                        builtin set -g __nocx_lc_active 1
                    else
                        __nocx_fail_open
                    end
                end
            end
        end
    end
    __nocx_install_prompts
    # Register only after user startup and initialization. Native events own
    # execution: blank input, repaint and idle Ctrl+C never fabricate attempts.
    function __nocx_preexec_handler --on-event fish_preexec
        __nocx_preexec $argv
    end
    function __nocx_postexec_handler --on-event fish_postexec
        __nocx_postexec $argv
    end
    function __nocx_prompt_handler --on-event fish_prompt
        __nocx_prompt_event $argv
    end
    function __nocx_cancel_handler --on-event fish_cancel
        builtin set -l saved_status $status $pipestatus
        builtin set -l fish_trace
        # Cancelling a parser continuation repaints Fish's cached prompt,
        # without fish_prompt. Publish readiness, never a fake execution.
        # A reader inside a running Fish command still belongs to that command.
        if builtin test "$__nocx_in_command" = 0
            __nocx_prompt_event
        end
        return "$saved_status[1]"
    end
    function __nocx_exit_handler --on-event fish_exit
        __nocx_shell_exit $argv
    end
end

# @NOCX_FISH_LAUNCH_SUPPORT@
# @NOCX_AGENT_WRAPPERS@

__nocx_initialize
