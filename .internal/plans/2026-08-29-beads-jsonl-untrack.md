# `.beads/issues.jsonl` уходит из git — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task becomes a bead (`bd create -t task --parent <epic-id>`). Steps within tasks use checkbox (`- [ ]`) syntax for human readability.

**Goal:** `.beads/issues.jsonl` перестаёт быть трекнутым файлом, поэтому конфликты по нему в PR на GitHub исчезают физически; копия бэклога на remote сохраняется, её публикует `pre-push` в `refs/beads/snapshot` из локальной Dolt-базы.

**Architecture:** Три шага в жёстком порядке. Сначала появляется новая страховка (`publish_beads_snapshot` в `.githooks/beads-hook.sh`, вызывается из `pre-push`), только потом снимается старая (untrack файла и вся машинерия merge driver-а), и последним подтягивается документация. Обратный порядок оставил бы окно, в котором копии бэклога в git нет вовсе.

**Tech Stack:** POSIX `sh` (хуки и тест-харнесс), git plumbing (`hash-object`, `mktree`, `commit-tree`, `push`), `bd` CLI, GNU make.

**Спека:** `.internal/specs/2026-08-29-beads-jsonl-untrack-design.md`

## Global Constraints

- Хуки — POSIX `sh`, не bash. `.githooks/pre-push` работает под `set -eu`: любое присваивание из `$(...)` должно быть защищено `|| { ...; return 0; }`, иначе скрипт умрёт до того, как политика ошибок его увидит.
- **Новый код никогда не блокирует `git push`.** Политика ошибок копируется с `pull_beads_state`, а не с `push_beads_state`: любая ветка возвращает 0, максимум печатает WARN. Причина записана в `.githooks/beads-hook.sh`: страховка, которая ломает push, будет обойдена через `--no-verify`, и тогда её нет вовсе.
- `bd` отсутствует, или `bd` вернул 3 (нет базы в этом клоне) — тихий выход 0, без WARN. Это контракт всех трёх существующих функций в `beads-hook.sh`.
- `timeout -k 5`, не голый `timeout`. `bd` игнорирует SIGTERM (`nocx-v48vl`): TERM оставляет процесс живым с эксклюзивным `.dolt/noms/LOCK`, который делят все worktree на машине.
- Каждый коммит называет бид в конце сабжекта: `<type>(<scope>): <subject> (<bead-id>)`. Scope здесь — `beads`.
- Ни одна проверка не зависит от времени: ждать наблюдаемого изменения состояния, не длительности.

---

### Task 1: `pre-push` публикует снапшот бэклога в `refs/beads/snapshot`

**Files:**

- Create: `scripts/test-beads-snapshot-hook.sh`
- Modify: `.githooks/beads-hook.sh` (добавить функцию после `pull_beads_state`)
- Modify: `.githooks/pre-push` (вызов **перед** `push_beads_state`)

**Interfaces:**

- Produces: `publish_beads_snapshot` — sourced-функция в `.githooks/beads-hook.sh`. Без аргументов: remote всегда `origin`. Всегда возвращает 0. Читает `BEADS_SNAPSHOT_TIMEOUT` (секунды, по умолчанию 60).
- Consumes: ничего из других задач.

**Три решения, каждое куплено находкой ревью (codex, 2026-08-29):**

1. **Внутренний `git push` идёт с `--no-verify`.** Иначе он заново запускает `pre-push` — рекурсия без дна, и каждый виток ещё и дёргает `bd dolt push` по общему для всех worktree `.dolt/noms/LOCK`. Измерено: без флага хук отработал 5 раз и остановился только предохранителем, с флагом — один раз.
2. **Вызов стоит ПЕРЕД `push_beads_state`.** `pre-push` под `set -e`, а `push_beads_state` намеренно возвращает ненулевой код при настоящем отказе `bd dolt push` — значит следующая строка не выполнится ровно в том сценарии, ради которого страховка существует. Сначала спасаем копию, потом рискуем.
3. **Remote — жёстко `origin`, а не `$1`.** `git push fork feature` иначе уложит снапшот в `fork`, а канонический `origin/refs/beads/snapshot` останется старым, и README будет восстанавливать из устаревшей копии, не зная об этом.

**Acceptance Criteria:**

- `sh scripts/test-beads-snapshot-hook.sh` печатает `0 failed`.
- **Регрессия на рекурсию:** настоящий `git push` в песочнице с установленным `core.hooksPath` запускает `pre-push` ровно один раз.
- **Регрессия на порядок:** когда `bd dolt push` падает, хук возвращает ненулевой код (контракт `push_beads_state` не тронут) **и** снапшот на remote всё равно опубликован.
- Счастливый путь: `git cat-file -p refs/beads/snapshot:issues.jsonl` на remote **побайтово** равен выводу `bd export` (сравнение через `cmp`, не через `$(...)`, который срезает завершающие переводы строк).
- Каждый внешний вызов имеет тест, где он падает: `bd export`, `git hash-object`, `git mktree`, `git commit-tree`, `git push`. Плюс: `bd` не на PATH, `bd` вернул 3, remote `origin` отсутствует, `bd` завис, `git push` завис.
- Рабочее дерево и индекс не тронуты: `git status --porcelain` до и после идентичен.
- Из опубликованного рефа восстанавливается рабочая база с тем же числом иссуев.

**Известный компромисс, проверен вручную 2026-08-29.** `git hash-object -w` и `git commit-tree` пишут в локальный object store, и коммит снапшота ни на что не ссылается, пока не отправлен, — он dangling. `git gc --prune=now` его удаляет. После успешного push это безразлично, объект уже на remote. Но все worktree на машине делят один object store, поэтому конкурентный `gc --prune=now` в соседнем worktree в окне между `commit-tree` и `push` объект снесёт, и push упадёт — что даст WARN и ничего больше, а следующий push опубликует новый снапшот. Авто-gc держит двухнедельный grace period и в это окно не попадает. Не закрываем: цена выше риска.

**Вторая гонка, тоже не чиним.** Worktree A экспортирует S1 и задерживается; B экспортирует S2 и делает force-push; A просыпается и кладёт S1 поверх — tip рефа старее базы. Следующий push любого worktree выправляет. Межпроцессный лок добавил бы отказ ровно в тот код, который обязан никогда не блокировать push.

- [ ] **Step 1: Написать падающий тест**

Создать `scripts/test-beads-snapshot-hook.sh`. Он строит песочницу — обычный репозиторий с bare-remote — и подкладывает на PATH заглушки, как это делает `scripts/test-beads-pull-hook.sh`. Приём с NOBD-путём взят оттуда дословно: неисполняемый файл-заглушка НЕ останавливает поиск по PATH, и `command -v` нашёл бы настоящий `bd`, а тест тогда полез бы в общую Dolt-базу.

```sh
#!/bin/sh
# Exercise every branch of the backlog-snapshot hook with stub bd and git on PATH.
#
# Contract under test: publish_beads_snapshot NEVER blocks a push. Exit 0 in
# every case — missing bd, no database, no origin, a failed export, a failed
# plumbing call, an unreachable remote, a hung bd, a hung push — and warn only
# when something genuinely failed. Same policy as the pull side and deliberately
# unlike push_beads_state; see .githooks/beads-hook.sh.
#
# Two of these are regression tests for defects found in review before the code
# existed, and they are the reason this file is worth its length:
#   - the inner `git push` must not re-enter pre-push (unbounded recursion)
#   - the snapshot must be published even when `bd dolt push` fails, which is
#     the only scenario the snapshot exists for
#
# Run: sh scripts/test-beads-snapshot-hook.sh
set -u

SRC=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
STUB=$(mktemp -d)
REAL_GIT=$(command -v git)
PASS=0
FAIL=0

pass() { echo "OK:   $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }
want() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (want '$2' got '$3')"; fi; }

check() { # check <label> <expected-exit> <actual-exit> <expect-warn:yes|no> <output>
    _label=$1 _want=$2 _got=$3 _warn=$4 _out=$5
    _ok=true
    [ "$_got" = "$_want" ] || { _ok=false; echo "  exit: want $_want got $_got"; }
    case $_warn in
        yes) printf '%s' "$_out" | grep -q WARN || { _ok=false; echo "  expected a WARN, got none"; } ;;
        no)  printf '%s' "$_out" | grep -q WARN && { _ok=false; echo "  unexpected WARN: $_out"; } ;;
    esac
    if $_ok; then pass "$_label"; else fail "$_label"; fi
}

make_bd() { printf '#!/bin/sh\n%s\n' "$1" > "$STUB/bd"; chmod +x "$STUB/bd"; }

# A git that delegates everything to the real one except the named subcommand.
# This is how each plumbing call gets a test where it fails (AGENTS.md rule 3).
make_git_failing() { # make_git_failing <subcommand> <body>
    cat > "$STUB/git" <<EOF
#!/bin/sh
if [ "\$1" = "$1" ]; then
$2
fi
exec $REAL_GIT "\$@"
EOF
    chmod +x "$STUB/git"
}
no_git_stub() { rm -f "$STUB/git"; }

fresh_sandbox() { # sets REPO and REMOTE
    REPO=$(mktemp -d)
    REMOTE=$(mktemp -d)
    git init -q --bare "$REMOTE"
    git init -q "$REPO"
    git -C "$REPO" config user.email tester@example.invalid
    git -C "$REPO" config user.name tester
    git -C "$REPO" remote add origin "$REMOTE"
    git -C "$REPO" commit -q --allow-empty -m init
}

# Runs the function the way pre-push runs it: under `set -eu`, cwd in the repo.
# `set -eu` is part of the contract — a bare $(...) that fails would kill the
# hook before its error policy could look at it.
run_publish() {
    HOOK_OUT=$(cd "$REPO" && PATH="$STUB:$PATH" BEADS_SNAPSHOT_TIMEOUT=2 \
        sh -c "set -eu; . '$SRC/.githooks/beads-hook.sh'; publish_beads_snapshot" 2>&1)
    HOOK_EXIT=$?
}

remote_blob() { git -C "$REMOTE" cat-file -p refs/beads/snapshot:issues.jsonl; }
have_ref() { [ -n "$(git -C "$REMOTE" for-each-ref refs/beads/snapshot)" ]; }

echo "=== publish_beads_snapshot: the happy path ==="

# 1. the ref appears and carries EXACTLY what bd printed, trailing newlines included
fresh_sandbox; no_git_stub
make_bd 'printf "%s\n" "{\"id\":\"nocx-1\"}" "{\"id\":\"nocx-2\"}"'
run_publish
check "success is silent" 0 "$HOOK_EXIT" no "$HOOK_OUT"
if have_ref; then pass "ref published"; else fail "ref published"; fi
# cmp on files, not $(...) — command substitution strips trailing newlines, so a
# hook that lost or gained one would sail through a string comparison.
PATH="$STUB:$PATH" bd export > "$STUB/expected.jsonl" 2>/dev/null
remote_blob > "$STUB/actual.jsonl"
if cmp -s "$STUB/expected.jsonl" "$STUB/actual.jsonl"; then
    pass "blob is byte-for-byte bd export"
else
    fail "blob is byte-for-byte bd export"; cmp "$STUB/expected.jsonl" "$STUB/actual.jsonl"
fi

# 2. the working tree is untouched — this runs on every push, it may not stage anything
fresh_sandbox; no_git_stub
printf 'dirty\n' > "$REPO/untracked.txt"
BEFORE=$(git -C "$REPO" status --porcelain)
make_bd 'echo "{}"'
run_publish
want "working tree untouched" "$BEFORE" "$(git -C "$REPO" status --porcelain)"

# 3. republish overwrites — consecutive snapshots share no history
fresh_sandbox; no_git_stub
make_bd 'echo "{\"v\":1}"'
run_publish
make_bd 'echo "{\"v\":2}"'
run_publish
check "republish is silent" 0 "$HOOK_EXIT" no "$HOOK_OUT"
want "republish overwrites" '{"v":2}' "$(remote_blob)"

echo "=== publish_beads_snapshot: every external call fails once ==="

# 4. bd exits 3 — no beads database in this clone
fresh_sandbox; no_git_stub
make_bd 'exit 3'
run_publish
check "no database (exit 3) skips silently" 0 "$HOOK_EXIT" no "$HOOK_OUT"

# 5. bd export genuinely fails
fresh_sandbox; no_git_stub
make_bd 'echo "database is locked" >&2; exit 1'
run_publish
check "failed export warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 6. git hash-object fails
fresh_sandbox
make_bd 'echo "{}"'
make_git_failing hash-object '    echo "stub: hash-object failed" >&2; exit 1'
run_publish
check "failed hash-object warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 7. git mktree fails
fresh_sandbox
make_git_failing mktree '    echo "stub: mktree failed" >&2; exit 1'
run_publish
check "failed mktree warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 8. git commit-tree fails for the reason it actually fails in the field: a clone
# with no committer identity. Pushing existing commits works there; minting one
# does not.
fresh_sandbox; no_git_stub
EMPTY_HOME=$(mktemp -d)
git -C "$REPO" config --unset user.email
git -C "$REPO" config --unset user.name
HOOK_OUT=$(cd "$REPO" && PATH="$STUB:$PATH" BEADS_SNAPSHOT_TIMEOUT=2 \
    HOME="$EMPTY_HOME" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
    GIT_AUTHOR_NAME= GIT_AUTHOR_EMAIL= GIT_COMMITTER_NAME= GIT_COMMITTER_EMAIL= \
    sh -c "set -eu; . '$SRC/.githooks/beads-hook.sh'; publish_beads_snapshot" 2>&1)
HOOK_EXIT=$?
check "no committer identity warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"
rm -rf "$EMPTY_HOME"

# 9. the remote is unreachable
fresh_sandbox; no_git_stub
git -C "$REPO" remote set-url origin "$REMOTE-does-not-exist"
run_publish
check "unreachable remote warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 10. there is no origin at all — a clone that only has a fork remote
fresh_sandbox; no_git_stub
git -C "$REPO" remote remove origin
run_publish
check "no origin skips silently" 0 "$HOOK_EXIT" no "$HOOK_OUT"

echo "=== publish_beads_snapshot: nothing hangs ==="

# 11. bd hangs
fresh_sandbox; no_git_stub
make_bd 'sleep 30'
run_publish
check "hung bd hits the timeout and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 12. git push hangs — an SSH remote waiting on interactive auth. The
# unreachable-path test above fails instantly and does not model this at all.
fresh_sandbox
make_bd 'echo "{}"'
make_git_failing push '    sleep 30'
run_publish
check "hung push hits the timeout and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 13. bd absent entirely — a contributor who does not use beads.
# Can't just trim PATH: on NixOS bd and coreutils share /run/current-system/sw/bin,
# so dropping bd's directory also drops dirname and the hook dies for the wrong
# reason. Build a PATH with the utilities the hook needs and no bd. A stub that
# is merely non-executable does NOT work — PATH search walks straight past it to
# the real bd, and the test would then read the shared Dolt database for real.
fresh_sandbox; no_git_stub
NOBD=$(mktemp -d)
for _u in dirname timeout sleep grep sh env git mktemp rm printf; do
    _p=$(command -v "$_u" 2>/dev/null) && ln -sf "$_p" "$NOBD/$_u"
done
[ -e "$NOBD/git" ] || fail "test setup — no git to link"
HOOK_OUT=$(cd "$REPO" && PATH="$NOBD" \
    sh -c "set -eu; . '$SRC/.githooks/beads-hook.sh'; publish_beads_snapshot" 2>&1)
HOOK_EXIT=$?
check "bd absent skips silently" 0 "$HOOK_EXIT" no "$HOOK_OUT"
rm -rf "$NOBD"

echo "=== pre-push as a whole: the two defects found in review ==="

install_real_hooks() { # copy the shipped hook into the sandbox and arm it
    mkdir -p "$REPO/hooks"
    cp "$SRC/.githooks/pre-push" "$SRC/.githooks/beads-hook.sh" "$REPO/hooks/"
    chmod +x "$REPO/hooks/pre-push"
    git -C "$REPO" config core.hooksPath hooks
}

# 14. REGRESSION: the inner push must not re-enter pre-push.
# Without --no-verify this recurses without a floor, and every level also runs
# bd dolt push against the .dolt/noms/LOCK every worktree on the machine shares.
fresh_sandbox; no_git_stub
install_real_hooks
FIRED="$REPO/fired.log"; : > "$FIRED"
make_bd "printf '%s\n' '{}'; [ \"\$1\" = dolt ] && exit 0; exit 0"
# count invocations by wrapping the hook
mv "$REPO/hooks/pre-push" "$REPO/hooks/pre-push.real"
printf '#!/bin/sh\necho fired >> "%s"\nexec "$(dirname "$0")/pre-push.real" "$@"\n' "$FIRED" \
    > "$REPO/hooks/pre-push"
chmod +x "$REPO/hooks/pre-push"
(cd "$REPO" && PATH="$STUB:$PATH" BEADS_SNAPSHOT_TIMEOUT=5 \
    git push -q origin HEAD:refs/heads/main >/dev/null 2>&1) || :
want "pre-push fires exactly once" 1 "$(wc -l < "$FIRED" | tr -d ' ')"

# 15. REGRESSION: the snapshot is published even when bd dolt push fails.
# That failure is the entire reason the snapshot exists; publishing after it,
# under set -e, would mean never publishing in the one case that matters.
fresh_sandbox; no_git_stub
install_real_hooks
make_bd 'case "$1 $2" in "dolt push") echo "remote is stranded" >&2; exit 1 ;; esac
case "$1" in export) printf "%s\n" "{\"id\":\"survivor\"}" ;; esac
exit 0'
(cd "$REPO" && PATH="$STUB:$PATH" BEADS_SNAPSHOT_TIMEOUT=5 \
    git push -q origin HEAD:refs/heads/main >/dev/null 2>&1) && PUSH_EXIT=0 || PUSH_EXIT=$?
if [ "$PUSH_EXIT" -ne 0 ]; then
    pass "a failed bd dolt push still blocks the code push"
else
    fail "a failed bd dolt push still blocks the code push"
fi
if have_ref && [ "$(remote_blob)" = '{"id":"survivor"}' ]; then
    pass "the snapshot was published anyway"
else
    fail "the snapshot was published anyway"
fi

rm -rf "$STUB"
echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
```

- [ ] **Step 2: Прогнать тест и убедиться, что он падает**

```bash
sh scripts/test-beads-snapshot-hook.sh
```

Ожидается: каждый `run_publish` печатает `publish_beads_snapshot: not found`, `HOOK_EXIT` равен `127`, тесты 14 и 15 тоже красные, в конце `0 passed, N failed` и ненулевой код возврата. Это правильный красный.

- [ ] **Step 3: Реализовать `publish_beads_snapshot`**

В `.githooks/beads-hook.sh`, сразу после закрывающей `}` функции `pull_beads_state`:

```sh
# Publish the issue export to the git remote as a standalone ref.
#
# This is the spare copy of the backlog, and it exists for exactly one failure:
# nocx-wj4 records that concurrent pushes to a git-protocol Dolt remote can
# strand history in refs/dolt/data. When that happens the healthy backlog is the
# local Dolt database, and this ref is what carries it off the machine.
#
# Which is why this runs in a hook and not in CI. A GitHub Action can only read
# the remote — so in the one moment this insurance exists for, it would faithfully
# back up the broken state. The developer's database is the good copy and a hook
# on their machine is the only thing that can reach it.
#
# A ref, not a branch: refs/beads/snapshot does not appear in the branch list, is
# not part of any pull request and is not cloned by default — exactly like the
# refs/dolt/data that already lives on the same remote. Recovery is three lines,
# and they are in README.md.
#
# Nothing here touches the working tree or the index. The blob goes straight into
# the object database and the commit is built with plumbing, so a push never
# rewrites a file under somebody's editor.
#
# Failure policy is the pull side's, not the push side's: every branch returns 0
# and at most warns. A push that fails because the SPARE copy could not be made
# is a push somebody learns to make with --no-verify, and then neither the spare
# nor the real sync happens.
#
# Three details are load-bearing, all three found in review before this shipped:
#
#   --no-verify on the inner push. Without it, `git push` inside pre-push runs
#   pre-push again — recursion with no floor, each level also firing bd dolt push
#   at the .dolt/noms/LOCK every worktree on this machine shares. Measured in a
#   throwaway repo: 5 levels deep and still going, versus 1 with the flag.
#
#   origin, not the remote git is pushing to (which git passes as $1). A
#   `git push fork feature` would put the snapshot in fork and leave the
#   canonical origin/refs/beads/snapshot silently stale, which is worse than
#   having none: README recovers from origin.
#
#   A timeout around the push as well as around the export. An SSH remote waiting
#   on interactive auth would otherwise hold a push open forever — the one thing
#   this function promises never to do.
publish_beads_snapshot() {
    command -v bd >/dev/null 2>&1 || return 0
    git remote get-url origin >/dev/null 2>&1 || return 0

    BD_GIT_HOOK=1
    export BD_GIT_HOOK

    # A variable rather than the if/elif/else the other two functions inline,
    # because this one needs the same timeout twice. -k for the reason recorded
    # above them: bd ignores SIGTERM and a plain timeout leaves it holding the
    # shared lock (nocx-v48vl).
    timeout_secs=${BEADS_SNAPSHOT_TIMEOUT:-60}
    if command -v timeout >/dev/null 2>&1; then
        _t="timeout -k 5 $timeout_secs"
    elif command -v gtimeout >/dev/null 2>&1; then
        _t="gtimeout -k 5 $timeout_secs"
    else
        _t=""
    fi

    _snap=$(mktemp) || return 0

    $_t bd export >"$_snap" 2>/dev/null && bd_exit=0 || bd_exit=$?

    if [ "$bd_exit" -eq 3 ]; then
        rm -f "$_snap" || :
        return 0 # no database in this clone
    fi
    if [ "$bd_exit" -ne 0 ]; then
        rm -f "$_snap" || :
        printf "\nWARN: bd export exited %s — no backlog snapshot published this push.\n" \
            "$bd_exit" >&2
        return 0
    fi

    _blob=$(git hash-object -w --stdin <"$_snap") || _blob=""
    rm -f "$_snap" || :
    if [ -z "$_blob" ]; then
        printf "\nWARN: could not write the backlog snapshot blob — none published.\n" >&2
        return 0
    fi

    _tree=$(printf '100644 blob %s\tissues.jsonl\n' "$_blob" | git mktree) || _tree=""
    if [ -z "$_tree" ]; then
        printf "\nWARN: could not build the backlog snapshot tree — none published.\n" >&2
        return 0
    fi

    _commit=$(git commit-tree "$_tree" -m "beads snapshot") || _commit=""
    if [ -z "$_commit" ]; then
        printf "\nWARN: could not build the backlog snapshot commit — none published.\n" >&2
        printf "      A clone with no committer identity cannot mint one; git config user.email.\n" >&2
        return 0
    fi

    # --force because each snapshot is a fresh root commit: consecutive ones share
    # no history, so every push after the first is a non-fast-forward.
    if ! $_t git push --no-verify --force --quiet origin "$_commit:refs/beads/snapshot" 2>/dev/null; then
        printf "\nWARN: could not publish the backlog snapshot to origin.\n" >&2
        printf "      The backlog itself is unaffected — this is only the spare copy.\n" >&2
        return 0
    fi

    return 0
}
```

- [ ] **Step 4: Подключить к `pre-push` ПЕРЕД `push_beads_state`**

В `.githooks/pre-push` заменить хвост файла:

```sh
. "$(dirname "$0")/beads-hook.sh"

# The spare copy first, from the local database, to a ref of its own.
#
# Before push_beads_state and not after, because this script runs under `set -e`
# and push_beads_state deliberately returns nonzero when bd dolt push genuinely
# fails. That failure IS the scenario the snapshot exists for — a stranded
# refs/dolt/data with the healthy backlog only on this machine. Publishing
# afterwards would mean never publishing in the one case that matters.
publish_beads_snapshot

push_beads_state
```

- [ ] **Step 5: Прогнать тест и убедиться, что он зелёный**

```bash
sh scripts/test-beads-snapshot-hook.sh
```

Ожидается: `19 passed, 0 failed`.

- [ ] **Step 6: Проверить на живом remote**

```bash
git push
git ls-remote origin refs/beads/snapshot
git fetch -q origin refs/beads/snapshot:refs/beads/snapshot
bd export > /tmp/snap-expected.jsonl
git cat-file -p refs/beads/snapshot:issues.jsonl > /tmp/snap-actual.jsonl
cmp /tmp/snap-expected.jsonl /tmp/snap-actual.jsonl && echo IDENTICAL
```

Ожидается: `ls-remote` печатает одну строку с sha, `cmp` молчит, печатается `IDENTICAL`.

Обратите внимание на `:refs/beads/snapshot` в `fetch` — без явного destination git пишет только `FETCH_HEAD` и локального рефа не создаёт.

- [ ] **Step 7: Проверить, что из снапшота действительно восстанавливается база**

Критерий приёмки, а не формальность: реф, из которого нельзя восстановиться, — не страховка, а её изображение.

```bash
BEFORE=$(bd list --status all --json | jq 'length'); echo "before: $BEFORE"
TMP=$(mktemp -d)
git clone -q --no-local --depth 1 "$PWD" "$TMP/recover"
cd "$TMP/recover"
git fetch -q "$OLDPWD" refs/beads/snapshot:refs/beads/snapshot
git cat-file -p refs/beads/snapshot:issues.jsonl > .beads/issues.jsonl
bd init --from-jsonl --discard-remote --yes
bd list --status all --json | jq 'length'
cd "$OLDPWD" && rm -rf "$TMP"
```

Ожидается: последнее число равно `$BEFORE`.

`bd init --from-jsonl`, а не `bd bootstrap`: проверено `bd bootstrap --dry-run` в этом клоне — он отвечает «clone from remote», потому что его порядок (справка: sync.remote → `refs/dolt/data` → `.beads/backup/*.jsonl` → `.beads/issues.jsonl`) до JSONL не доходит, пока `sync.remote` настроен. А снапшот существует ровно для случая «remote доступен, но испорчен». `--from-jsonl` — булев флаг, путь берётся из `import.path` (`bd config get import.path` уже отвечает `issues.jsonl`); `--discard-remote` обязателен и честно называет происходящее.

Если число не совпало — снапшот неполон, и это блокер задачи, а не замечание.

- [ ] **Step 8: Коммит**

```bash
git add .githooks/beads-hook.sh .githooks/pre-push scripts/test-beads-snapshot-hook.sh
git commit -m "feat(beads): publish the backlog snapshot to a ref of its own (<bead-id>)"
```

---

### Task 2: убрать `.beads/issues.jsonl` из git и всю машинерию merge driver-а

**Depends on Task 1** — страховка должна существовать до того, как снимается старая. Иначе между коммитами есть окно без копии бэклога в git вовсе.

**Files:**

- Modify: `.gitignore:5-6`
- Delete: `.gitattributes` (в нём нет других правил)
- Modify: `Makefile:172-183` (комментарий + цель `hooks`)
- Modify: `.githooks/pre-commit:108` (комментарий) и `:275-280` (шаг 7)
- Modify: `.githooks/beads-hook.sh` (удалить `export_beads_snapshot`)
- Modify: `.beads/config.yaml:47-60`
- Untrack: `.beads/issues.jsonl`

**Interfaces:**

- Consumes: `publish_beads_snapshot` из Task 1 — она уже вызывается из `pre-push` и остаётся единственным местом, где запускается `bd export`.
- Produces: ничего для Task 3, кроме списка утверждений, которые документация должна перестать делать.

**Acceptance Criteria:**

- `git -c merge.beads-export.driver=false merge-tree --write-tree --name-only <A> <B>` для двух веток, обе из которых меняли бэклог, не печатает ни одного `CONFLICT`. Это и есть то, что вычисляет GitHub.
- `git log --stat -1` нового коммита не содержит `.beads/issues.jsonl`, а `git status --porcelain` после коммита пуст: файл проигнорирован, а не «untracked».
- `git config --get merge.beads-export.driver` пуст после `make hooks`, в том числе в клоне, где драйвер был настроен раньше.
- `git ls-files .beads/issues.jsonl` не печатает ничего.
- В `.beads/config.yaml` ветки `export.auto` и `export.git-add` равны `false`. **Не** проверять это через `bd config get`: `bd` резолвит корень проекта по базе, которая живёт в главном клоне, поэтому из worktree он читает `/home/dev/repos/nocx/.beads/config.yaml` и покажет значение с `main`, а не из ветки. Значение вступит в силу, когда ветка вмержится.

- [ ] **Step 1: Зафиксировать красный — воспроизвести сегодняшний конфликт**

```bash
git -c merge.beads-export.driver=false merge-tree --write-tree --name-only \
    origin/fix/tui-summon-stop-recovery origin/fix/live-screen-capture-bounded
```

Ожидается сейчас: первой строкой sha дерева, затем `.beads/issues.jsonl`, затем `CONFLICT (content): Merge conflict in .beads/issues.jsonl`. Записать вывод — это baseline, к которому вернёмся на шаге 8.

- [ ] **Step 2: Заигнорить и снять с трекинга**

В `.gitignore`, рядом с уже существующей строкой `.beads/proxieddb/`:

```
.beads-credential-key
.beads/issues.jsonl
.beads/proxieddb/
```

Затем:

```bash
git rm --cached -q .beads/issues.jsonl
```

- [ ] **Step 3: Снять `git-add` и авто-экспорт с самого `bd`**

Это не косметика. `.beads/config.yaml` держит `git-add: true`, то есть `bd` стейджит файл сам, независимо от хука; оставить это значит получать `git add` на игнорируемый путь при каждой записи в базу.

Заменить блок `export:` в `.beads/config.yaml` (сейчас строки 47-60) на:

```yaml
# Optional JSONL auto-export for viewers, interchange, and issue-level migration.
# Disabled by default; enable only when an integration needs fresh .beads/issues.jsonl.
# Use relative paths under .beads/ for JSONL import/export filenames.
#
# Off here. It was on because .beads/issues.jsonl used to be tracked in git and
# had to stay in step with the database; the file is untracked now, so there is
# nothing for the export to keep in step with. `git-add: true` in particular
# would now stage an ignored path on every write.
#
# The spare copy of the backlog is published by .githooks/pre-push to
# refs/beads/snapshot on the remote. Run `bd export` by hand whenever you want
# a local file.
export:
  auto: false
  path: issues.jsonl
  git-add: false
# import:
#   path: issues.jsonl
```

- [ ] **Step 4: Удалить merge driver**

```bash
git rm -q .gitattributes
```

В `Makefile` заменить комментарий и цель `hooks` (сейчас строки 172-183) на:

```make
# Per-clone git configuration: git behaviour this repo needs that a clone cannot
# carry by itself.
#
# The --unset lines clean up after the beads merge driver, which used to resolve
# .beads/issues.jsonl by regenerating it. The file is untracked now, so there is
# nothing left to merge — and the driver never helped where it hurt most anyway:
# GitHub computes a pull request's mergeability server-side and does not run
# custom merge drivers at all.
hooks:
	git config core.hooksPath .githooks
	-@git config --unset merge.beads-export.driver 2>/dev/null || true
	-@git config --unset merge.beads-export.name 2>/dev/null || true
	@echo "git hooks installed from .githooks/"
```

- [ ] **Step 5: Удалить шаг 7 из `pre-commit`**

Удалить из `.githooks/pre-commit` целиком блок (сейчас строки 275-280):

```sh
# --- 7. beads: materialise the issue snapshot into this commit ---------------
# Last, deliberately: a gate that fails above must not leave .beads/issues.jsonl
# rewritten and staged for a commit that never happens.
. "$(dirname "$0")/beads-hook.sh"
export_beads_snapshot
ok "beads export"
```

И в комментарии на строке 108 убрать `, and the beads export` — перечисление того, что делает хук, больше не включает экспорт.

- [ ] **Step 6: Удалить осиротевшую функцию**

Из `.githooks/beads-hook.sh` удалить `export_beads_snapshot` целиком, вместе с её комментарием (сейчас строки 142-170). После удаления у неё не остаётся вызывающих.

```bash
grep -rn 'export_beads_snapshot' . --exclude-dir=node_modules --exclude-dir=.git
```

Ожидается: пусто.

- [ ] **Step 7: Проверить, что `bd` больше ничего не стейджит**

Проверять записью в базу здесь **нельзя**: Dolt-база общая для всех worktree на
машине и синхронизируется на remote. Пара `bd create` / `bd purge` в этом шаге —
это окно, в котором параллельный worktree сделает `pre-push`, и scratch-задача
уедет в общий бэклог и в снапшот; другой агент может успеть её увидеть или взять,
а `bd purge` — столкнуться с его операцией. Проверяем конфигурацией:

```bash
grep -A3 '^export:' .beads/config.yaml   # ожидается auto: false и git-add: false
grep -rn 'git add' .githooks/            # ожидается: ни одного упоминания .beads
```

**`bd config get` здесь не работает и не является проверкой.** В worktree нет
базы (`.beads/embeddeddolt/` отсутствует), поэтому `bd` резолвит корень проекта в
главный клон и читает его `.beads/config.yaml` — то есть значение с `main`.
Измерено 2026-08-29: файл ветки уже говорит `false`, а `bd config get
export.git-add` отвечает `true`, и это correct — на `main` файл пока трекается,
так что там `auto: true` ещё уместен. Оба значения сойдутся в момент мержа.

- [ ] **Step 8: Проверить зелёный — тот же merge-tree**

Записи в бэклог здесь больше не нужны и не делаются: после untrack-а файл вообще
не является входом для git, поэтому две обычные ветки доказывают ровно то же и
ничего не пишут в общую базу.

```bash
git switch -c tmp/merge-check-a
git commit -q --allow-empty -m "scratch a"
git switch -c tmp/merge-check-b HEAD~1
git commit -q --allow-empty -m "scratch b"
git -c merge.beads-export.driver=false merge-tree --write-tree --name-only \
    tmp/merge-check-a tmp/merge-check-b
git switch -
git branch -D tmp/merge-check-a tmp/merge-check-b
```

Ожидается: `merge-tree` печатает одну строку — sha дерева, и больше ничего. Ни `.beads/issues.jsonl`, ни `CONFLICT`. Контраст с шагом 1 и есть доказательство.

- [ ] **Step 9: Проверить, что `make hooks` вычищает драйвер**

```bash
git config merge.beads-export.driver "bd export -o %A"   # притвориться старым клоном
make hooks
git config --get merge.beads-export.driver; echo "exit=$?"
```

Ожидается: `make hooks` не падает, `--get` ничего не печатает и `exit=1`.

- [ ] **Step 10: Коммит**

```bash
git add -A .gitignore Makefile .githooks/pre-commit .githooks/beads-hook.sh .beads/config.yaml
git add -u .gitattributes .beads/issues.jsonl
git commit -m "build(beads): take the issue export out of git (<bead-id>)"
```

Проверить сразу:

```bash
git ls-files --error-unmatch .beads/issues.jsonl 2>&1   # ожидается: "did not match any file"
git status --porcelain                                  # ожидается пусто
git check-ignore -v .beads/issues.jsonl                 # ожидается: строка из .gitignore
```

**Не** проверять этот коммит через `git log --stat`: `git rm --cached` записывает
в него удаление файла, поэтому `.beads/issues.jsonl` в нём присутствует
обязательно и при полностью правильной реализации. Отсутствие проверяется на
СЛЕДУЮЩЕМ обычном коммите:

```bash
git commit -q --allow-empty -m "scratch"
git show --stat --diff-filter=AM -1 | grep -c 'beads/issues.jsonl'   # ожидается 0
git reset -q --hard HEAD~1
```

---

### Task 3: документация перестаёт описывать несуществующий файл

**Depends on Task 2.**

**Files:**

- Modify: `AGENTS.md:22-32`
- Modify: `README.md:163-166`, `README.md:229-231`, `README.md:253-256`
- Modify: `.githooks/pre-push:62` (комментарий про tracked JSONL)
- Modify: `.githooks/beads-hook.sh:66` (текст сообщения об ошибке)

**Interfaces:**

- Consumes: `refs/beads/snapshot` и команды восстановления из Task 1; факт untrack-а из Task 2.
- Produces: ничего.

**Acceptance Criteria:**

- `grep -rn 'issues\.jsonl' AGENTS.md README.md .githooks/` не находит ни одного утверждения о том, что файл трекается, коммитится, стейджится, конфликтует или служит fallback-ом для `bd bootstrap`. Комментарии в хуках входят в проверку намеренно: следующий человек будет диагностировать восстановление по ним, а не по README.
- Команда восстановления из `refs/beads/snapshot` присутствует в `README.md` дословно и выполняется без правок.
- Managed-блоки «Architecture in one line» не изменены (их генерирует `bd`), `.internal/HANDOFF-2026-08-18.md` не изменён (датированный исторический документ).

- [ ] **Step 1: `AGENTS.md`**

Заменить абзац «Fresh clone» (сейчас строки 22-27) и следующий за ним абзац «Never resolve a conflict…» (строки 29-33) на:

```markdown
**Fresh clone:** install the tooling, then `make init`. Git carries neither the issue
database nor its ref, so until `make init` runs there is no backlog — `bd ready` answers
"no beads database found". `git push` runs `bd dolt push`; if a push fails on beads, fix
the sync, because `--no-verify` leaves everyone on a backlog that looks current and is not.

**`.beads/issues.jsonl` is not in git, deliberately.** `.githooks/pre-commit` used to
regenerate and stage it on every commit, so every branch touched all 2707 lines of it and
almost every pull request came back conflicted. A merge driver fixed only half of that:
it is per-clone git config, and GitHub computes mergeability server-side without running
custom drivers at all, which is why PR #129 was clean locally and CONFLICTING on the site.
The file is ignored now. The backlog lives in Dolt and syncs through `refs/dolt/data`; the
spare copy is published by `.githooks/pre-push` to `refs/beads/snapshot` on the same
remote, from your local database. Recovering it is three lines in
[README](README.md#beads).
```

- [ ] **Step 2: `README.md`, предупреждение про версию `bd`**

В абзаце на строках 163-166 фраза «the auto-export strips every dependency edge from `.beads/issues.jsonl`, which the pre-commit hook then commits» описывает механику, которой больше нет. Заменить хвост предложения на:

```markdown
> ⚠️ `bd` must be **≥ 1.1.0**. Older builds (e.g. 1.0.3, which some distros and
> nixpkgs still ship) misread the tracker's dependency schema: `bd stats` errors,
> and — worse — the export strips every dependency edge, which would put a
> dependency-free backlog into the snapshot the pre-push hook publishes. Check
> with `bd version` before enabling hooks.
```

- [ ] **Step 3: `README.md`, описание `bd bootstrap`**

Строки 229-231, «it clones from the configured remote and falls back to the tracked `.beads/issues.jsonl` only if that is unavailable» — трекнутого файла больше нет. Заменить предложение на:

```markdown
`bd bootstrap`, not `bd init`: the backlog lives in a Dolt database that git does
not carry, and bootstrap is the command that knows where to get it — it clones
from the configured remote. There is no tracked JSONL in this repository to fall
back to; if the remote is unusable, recover from the snapshot ref below. A clone
without this step has no issue database at all, and `bd ready` will tell you so.
`bd init --from-jsonl` exists, but it builds a history divergent from the remote,
so keep it for recovery, not setup.
```

- [ ] **Step 4: `README.md`, описание хуков**

Абзац на строках 253-256 («It then writes `.beads/issues.jsonl` and stages it…») заменить целиком на:

````markdown
The pre-push hook pushes the issue database itself with `bd dolt push`. That is what a
fresh clone reads, so skipping it leaves collaborators on a backlog that looks current
and is not. It then publishes a spare copy of the export to `refs/beads/snapshot` on the
same remote — a ref, not a branch, so it stays out of the branch list and out of every
pull request.

`.beads/issues.jsonl` is **not** tracked. It used to be, regenerated and staged on every
commit, and it conflicted in almost every pull request: GitHub decides mergeability
server-side and never runs the repository's merge driver. The snapshot ref replaces it,
and unlike a CI job it is written from your local database — which matters, because the
failure it insures against is a stranded history on the remote itself.

Recovering the backlog from the snapshot:

```bash
git fetch origin refs/beads/snapshot:refs/beads/snapshot
git cat-file -p refs/beads/snapshot:issues.jsonl > .beads/issues.jsonl
bd init --from-jsonl --discard-remote
```
````

`bd init`, not `bd bootstrap`. Bootstrap prefers the configured remote and only
reaches a local JSONL fourth, so in the failure this snapshot exists for — a
remote that answers but whose history is stranded — it would faithfully restore
the broken state. `--from-jsonl` is a boolean flag that reads `import.path`;
`--discard-remote` is what authorizes replacing that stranded history, and it is
required rather than convenient.

If `bd` is missing or this clone has no database, every beads step in both hooks steps
aside silently; a

````

(последняя строка — стык с уже существующим текстом, не дублировать его.)

- [ ] **Step 5: Поправить комментарии в самих хуках**

Оба сейчас утверждают, что трекнутый JSONL — это fallback для `bd bootstrap`.

`.githooks/pre-push:62`, фразу «That ref — not the tracked `.beads/issues.jsonl` — is what a fresh clone reads: `bd bootstrap` prefers sync.remote and falls back to the JSONL only fourth» заменить на:

```sh
# The issue database goes to refs/dolt/data on the remote, and that is what a
# fresh clone reads — there is no tracked JSONL in this repository to fall back
# to. A push that skips this step leaves collaborators on a backlog that can be
# days old, with nothing on screen to suggest it.
````

`.githooks/beads-hook.sh:66`, строку сообщения об ошибке «because bd bootstrap prefers the Dolt remote over the tracked JSONL» заменить на:

```sh
    printf "      because the Dolt remote is the only copy a fresh clone can read.\n" >&2
```

- [ ] **Step 6: Проверить, что не осталось ложных утверждений**

```bash
grep -rn 'issues\.jsonl' AGENTS.md README.md .githooks/
```

Ожидается: только упоминания в новом тексте — путь в команде восстановления и объяснение, почему файла нет в git. Ни одного утверждения про «tracked», «stages it», «commits it», «merge driver», «resolve a conflict».

- [ ] **Step 7: Коммит**

```bash
git add AGENTS.md README.md .githooks/pre-push .githooks/beads-hook.sh
git commit -m "docs(beads): the export is not in git; the spare copy is a ref (<bead-id>)"
```

---

## После плана

Гейт перед `main` — на интеграторе, как обычно: merge slot, `make ci-full` на смерженном дереве, потом push. Изменения тут — хуки, `.gitignore`, `Makefile` и документация; кода приложения ни одна задача не трогает, но `pre-commit` меняется, поэтому `make ci-full` прогоняется целиком, а не выборочно.

Отдельно висит `nocx-mvlsv`: попадают ли memories в снапшот. Он намеренно не в этом плане — сейчас держится паритет с тем, что делал трекнутый файл.
