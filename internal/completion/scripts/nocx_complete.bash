#!/usr/bin/env bash
# nocx completion script — runs on the remote host via a second shell
# (the DiscoveryConn lane of ADR-0020). The user's line is never touched;
# no keystroke is ever forwarded.
#
# Arguments: $1=cwd  $2=line  $3=caret  $4=limit  $5=nonce
set -euo pipefail

NONCE="${5:-}"

# cd to cwd, if given and reachable.
if [[ -n "${1:-}" ]] && [[ -d "$1" ]]; then
  cd "$1" 2>/dev/null || true
fi

LINE="${2:-}"
POS="${3:-0}"
LIMIT="${4:-50}"

printf '%s\n' "NONCE:${NONCE}:START"

line_len=${#LINE}
# Clamp POS.
if [[ $POS -gt $line_len ]]; then POS=$line_len; fi

# Extract the word at POS.
word_start=$POS
while [[ $word_start -gt 0 ]]; do
  prev="${LINE:$((word_start-1)):1}"
  case "$prev" in
    " "|$'\t'|$'\n'|"|"|"&"|";"|"("|")"|"<"|">"|'"'|"'"|"\`") break;;
  esac
  word_start=$((word_start-1))
done
word_end=$POS
while [[ $word_end -lt $line_len ]]; do
  cur="${LINE:$word_end:1}"
  case "$cur" in
    " "|$'\t'|$'\n'|"|"|"&"|";"|"("|")"|"<"|">"|'"'|"'"|"\`") break;;
  esac
  word_end=$((word_end+1))
done
WORD="${LINE:$word_start:$((word_end-word_start))}"

# Determine command position.
cmd_start=0
while [[ $cmd_start -lt $line_len ]]; do
  c="${LINE:$cmd_start:1}"
  case "$c" in " "|$'\t') cmd_start=$((cmd_start+1));; *) break;; esac
done
cmd_end=$cmd_start
while [[ $cmd_end -lt $line_len ]]; do
  c="${LINE:$cmd_end:1}"
  case "$c" in
    " "|$'\t'|$'\n'|"|"|"&"|";"|"("|")"|"<"|">"|'"'|"'"|"\`") break;;
  esac
  cmd_end=$((cmd_end+1))
done
CMD="${LINE:$cmd_start:$((cmd_end-cmd_start))}"

is_cmd_pos=0
if [[ $POS -le $cmd_end ]]; then is_cmd_pos=1; fi

n=0

# ── Path completion ─────────────────────────────────────────────────────
# Always applicable except for a bare command name.
if [[ "$WORD" == */* ]] || [[ "$WORD" == .* ]] || [[ "$WORD" == ~* ]] || [[ $is_cmd_pos -eq 0 ]]; then
  # Dedup across the two compgen passes below: a directory is listed by both
  # `compgen -f` and `compgen -d`, so without this it would be offered twice.
  #
  # A DELIMITED STRING, not an associative array. `declare -A` is bash 4.0+,
  # and macOS still ships bash 3.2 — where it does not merely misbehave, it
  # aborts the script: "declare: -A: invalid option", exit 2, and the remote
  # returns no candidates at all. Completing a path on any stock-bash host was
  # therefore silently empty (nocx-smy9).
  #
  # The membership test quotes "$entry" INSIDE the pattern so a filename
  # holding *, ? or [ is compared literally rather than glob-matched. The list
  # is bounded by LIMIT, so the linear scan is bounded too.
  #
  # The `name` column is the LAST PATH SEGMENT, never compgen's word. compgen
  # answers with the whole replacement for the token — `repos/tabby` for the
  # token `repos/t` — while `Candidate.Name` and shell.complete's schema both
  # declare name to be the last segment, which is what LocalCompleter emits and
  # what the renderer's shell adapter assumes: it inserts the token's own
  # prefix plus the name. Handing it the word inserted `repos/` twice, so
  # `cd repos/t` completed to `cd repos/repos/tabby/` (nocx-yqoy5). `$entry`
  # stays whole for the dedup and for `$abs`; only the printed name is cut.
  seen_paths=""
  # compgen -f: files + dirs (always a bash builtin).
  while IFS= read -r entry; do
    [[ -z "$entry" ]] && continue
    if [[ "$seen_paths" != *"|$entry|"* ]]; then
      seen_paths="$seen_paths|$entry|"
      abs=""
      if [[ "$entry" == /* ]]; then abs="$entry"
      elif [[ "$entry" == ~* ]]; then abs="${entry/#\~/$HOME}"
      else abs="$PWD/$entry"; fi
      isd=0
      if [[ -d "$entry" ]]; then isd=1; fi
      printf '%s\t%s\t%s\t%d\n' "path" "${entry##*/}" "$abs" "$isd"
      n=$((n+1))
      if [[ $n -ge $LIMIT ]]; then break 2; fi
    fi
  done < <(compgen -f -- "$WORD" 2>/dev/null || true)
  # compgen -d: directories only.
  while IFS= read -r entry; do
    [[ -z "$entry" ]] && continue
    if [[ "$seen_paths" != *"|$entry|"* ]]; then
      seen_paths="$seen_paths|$entry|"
      abs=""
      if [[ "$entry" == /* ]]; then abs="$entry"
      elif [[ "$entry" == ~* ]]; then abs="${entry/#\~/$HOME}"
      else abs="$PWD/$entry"; fi
      printf '%s\t%s\t%s\t%d\n' "path" "${entry##*/}" "$abs" 1
      n=$((n+1))
      if [[ $n -ge $LIMIT ]]; then break 2; fi
    fi
  done < <(compgen -d -- "$WORD" 2>/dev/null || true)
fi

# ── Command-name completion ──────────────────────────────────────────────
if [[ $is_cmd_pos -eq 1 ]] && [[ "$WORD" != */* ]] && [[ "$WORD" != .* ]] && [[ "$WORD" != ~* ]]; then
  while IFS= read -r entry; do
    [[ -z "$entry" ]] && continue
    printf '%s\t%s\n' "command" "$entry"
    n=$((n+1))
    if [[ $n -ge $LIMIT ]]; then break 2; fi
  done < <(compgen -c -- "$WORD" 2>/dev/null || true)
fi

# ── Command-specific completion (bash-completion) ─────────────────────────
if [[ $is_cmd_pos -eq 0 ]] && [[ -n "$CMD" ]] && type -t _completion_loader &>/dev/null; then
  _completion_loader "$CMD" 2>/dev/null || true
  comp_func=$(complete -p "$CMD" 2>/dev/null | sed -n 's/.*-F \([^ ]*\).*/\1/p')
  if [[ -n "$comp_func" ]] && type -t "$comp_func" &>/dev/null; then
    COMP_LINE="$LINE"
    COMP_POINT=$POS
    COMP_TYPE=9
    COMP_KEY=9
    # Build COMP_WORDS from LINE.
    COMP_WORDS=()
    local w="$LINE"
    while [[ -n "$w" ]]; do
      w="${w#"${w%%[! ]*}"}"
      if [[ -z "$w" ]]; then break; fi
      local ew="${w%% *}"
      COMP_WORDS+=("$ew")
      w="${w#"$ew"}"
    done
    COMP_CWORD=$((${#COMP_WORDS[@]} - 1))
    "$comp_func" "$CMD" "$WORD" "${WORD: -1}" 2>/dev/null || true
    if [[ ${#COMPREPLY[@]} -gt 0 ]]; then
      for entry in "${COMPREPLY[@]}"; do
        [[ -z "$entry" ]] && continue
        printf '%s\t%s\n' "function" "$entry"
        n=$((n+1))
        if [[ $n -ge $LIMIT ]]; then break 2; fi
      done
    fi
  fi
fi

printf '%s\n' "NONCE:${NONCE}:END"
