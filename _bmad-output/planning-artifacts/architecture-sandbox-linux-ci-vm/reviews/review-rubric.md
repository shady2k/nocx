# Rubric review — Sandbox Linux CI VM

**Вердикт: FAIL — изоляция и сохранение native consumers заданы правильно, но перед передачей в реализацию необходимо замкнуть offline build inputs, boot/kernel prerequisites и ресурсный контракт.**

Объект: `architecture-sandbox-linux-ci-vm.md`, строки 1–110. Это review design artifact, не проверка реализованного VM runner. `git diff -- <draft>` не показал tracked diff; ссылки ниже относятся к предоставленному черновику. Spine и код не изменялись. Build, lint, tests, formatters и VM boot не запускались.

## Высокий приоритет

### R1 — Зафиксировать полный offline input contract для обоих consumers

**P1; autofix в spine.** AD-12 перечисляет tracked source, bare git store, VT assets, Go SDK и public module cache (строки 53–58), но не определяет обязательную guest build environment и привязку этих объектов к существующим consumers. Это не только запуск готового helper: `scripts/sandbox-smoke-linux/main.go:93–103` собирает probe/current source helper, а `:449–475` в **обоих** lanes выполняет `git archive` конкретного `oldHelperRevision`, `tar` и `go build` с `CGO_ENABLED=1` и external linker. `:452` ищет revision через `git -C repositoryRoot()`, а `:463–469` читает VT vendor и notices по фиксированным путям. Bare object store рядом с tracked tree сам по себе не делает этот tree Git repository. Packaged consumer выбирает embedded local/remote artifacts для `runtime.GOARCH` (`:398–411`), не произвольный архив рядом с исходниками. Перечисленный сейчас набор не гарантирует даже запуск этих неизменённых consumers без сети.

**Точное исправление:** определить guest cwd/repository layout и привязку `.git` к credential-free store; включить достижимый commit/tree/blob closure для `3160c5c6cfcee34b166e13480f1f71de2ac9901c`; перечислить Git, tar, C compiler/external linker, libc headers/runtime, writable UID1001 HOME/build cache/temp и Go module closure **current + old revision**. Generated VT vendor/notices и native embed inputs должны находиться именно по ожидаемым consumer paths. Явно разделить staging source-generated archives и exact downloaded local/remote release archives с сохранением байтов: ни один packed current helper не пересобирается. Offline preflight выполняется до boot, отсутствие любого обязательного input — failure, а не разрешение guest download или rebuild release bytes.

Основание brownfield: `docs/architecture.md:261–265`; ADR0081, Consequences and acceptance. Это CI userspace/tool staging, не изменение application dependencies.

### R2 — Включить diagnostic backend в обязательный kernel/boot contract

**P1; autofix в spine.** AD-11 требует built-in Landlock и disk/PTY/network drivers (строки 44–47), но не фиксирует поддержку seccomp filter/user notification. ABI9 не доказывает её наличие. Между тем каждый consumer обязательно ожидает real Linux attempted-denial event и `ObserverActive`: `main.go:328–370, 619–640`. Brownfield прямо определяет backend как seccomp `openat`/`openat2` notifications (`docs/architecture.md:255`). Следовательно kernel, удовлетворяющий сформулированным сейчас AD-11 feature requirements, может пройти ABI check, но не пройти обязательный diagnostic consumer.

**Точное исправление:** сделать kernel config/boot manifest частью входа VM seam: в дополнение к Landlock включить seccomp/filter с user-notification support для выбранной архитектуры; закрепить activation Landlock в booted LSM set. Определить одну согласованную пару QEMU disk device/root device, built-in ext4 и необходимые boot drivers, способ запуска собственного PID1 и console. Проверять не только версию/ABI, но и сохранение обязательного diagnostic proof. Нельзя решать проблему отключением diagnostics или изменением smoke assertions.

Primary boot reference: [QEMU Direct Linux Boot](https://www.qemu.org/docs/master/system/linuxboot.html) явно связывает `-kernel`, `-drive` и kernel `root=`; наличие raw ext4 image ещё не определяет загрузочный контракт. Здесь не утверждается, что выбранный Linux release лишён seccomp: дефект — отсутствие обязательного feature requirement в контракте сборки собственного kernel.

## Средний приоритет

### R3 — Определить ресурсный и временной envelope внутри существующих ceilings

**P2; discuss, затем зафиксировать решение до implementation handoff.** Operational Envelope (строки 100–105) задаёт только 60/30-minute job ceilings, хотя этот же job должен собрать kernel/userspace, создать raw image, загрузить VM и выполнить consumers. Не определены guest RAM/vCPU, disk/workspace capacity, build concurrency либо boot/proof/cleanup deadlines. `main.go:68` оставляет каждому consumer 15-minute context; `source` по AD-13 запускает два consumers. Один общий CI ceiling не задаёт ни допустимые ресурсы, ни резерв на cleanup после VM timeout. Это именно пропущенная operational/environmental dimension из good-spine checklist, а не требование ускорить приложение.

**Точное исправление:** выбрать поддерживаемый Linux host/guest architecture и KVM-capable runner class, численные RAM/vCPU/disk/free-space bounds и build parallelism; задать stage deadlines и резерв cleanup, укладывающиеся в существующие source 60m / packaged 30m вместе с уже существующими шагами. При отсутствии KVM/ресурсов завершать preflight failure. Указать cold-cache path как обязательный сценарий acceptance; cache может ускорять только verified inputs, не быть скрытым prerequisite. Фактическое укладывание в лимиты должна доказать последующая реализация; этот review не выдумывает время сборки и не предлагает увеличивать ceilings.

## Good-spine checklist

| Критерий                                                | Оценка                    | Основание                                                                                                                                                                                           |
| ------------------------------------------------------- | ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Real divergence points для уровня ниже                  | FAIL                      | R1–R3 оставляют разные, несовместимые image/runner implementations формально допустимыми.                                                                                                           |
| Rule enforceable и предотвращает заявленное расхождение | PARTIAL                   | AD-11/12 нуждаются в R1/R2; fail-closed AD-14 сформулирован правильно.                                                                                                                              |
| Deferred не размывает seam                              | PASS                      | Application VM support, self-hosted registration и product network isolation не нужны для выбранного batch worker. Ресурсный контракт текущего worker нельзя считать этим Deferred.                 |
| Named tech verified-current                             | НЕ ПОДТВЕРЖДЕНО ПОЛНОСТЬЮ | Версии/ kernel digest записаны; QEMU direct boot reference прочитан. SHA-256 Linux 7.2.9 и актуальность всех pins этим rubric review независимо не проверялись; это не заявление об их ошибочности. |
| Brownfield и inherited AD-1…AD-10                       | PASS по границам          | Нет переноса application ownership в VM. ADR0081 mandatory ABI9 и неизменяемый grant сохраняются. Требуется R1/R2 для реальных существующих consumers.                                              |
| Operational/environmental dimension                     | FAIL                      | R3; boot/input subdimensions — R1/R2.                                                                                                                                                               |

## Preserved consumers и host isolation

- **Покрыто:** `source` включает source + packaged; `packaged` не пересобирает downloaded one-origin helpers; shared seam, macOS native path, ABI9 refusal, old/current helpers, ordinary sessions, descendants, pathname sockets, TCP, readiness и diagnostics остаются обязательными. `unchanged native consumers` также сохраняет malformed prepare/runner failure no-fallback, remote-only refusal и persisted grant checks — отдельной упрощённой VM proof реализации не нужно.
- **Покрыто на уровне инварианта:** AD-12 запрещает host filesystem exports, shared HOME, secrets/control sockets и guest network device; Docker не получает host root/HOME; UID1001 и cleanup ownership заданы. Слова host TCP/socket внутри smoke относятся к **guest-local fixture**, которую consumer сам создаёт (`main.go:126–165`), а не разрешают доступ к физическому host. Loopback нужен и уже разрешён.
- **Result boundary покрыта концептуально:** producer — root PID1, transport — isa-debug-exit, consumer — host runner/workflow; AD-14 разрешает только encoded success и отвергает panic/missing result/timeout/nonzero. Кода принимающего dispatch ещё нет, поэтому его корректность не заявляется. Реализация должна возвращать aggregate result обоих required consumers для source, не успех последней команды и не console marker. Численные encoding/device параметры должны совпасть на обеих сторонах; это деталь исполнения уже принятого правила, не отдельный blocker rubric.
- **Остаются вне доказательства VM success:** полный release/signing acceptance, critical npm audit и Darwin lint. Никаких suppressions, dependency upgrades, PR/publish/merge или изменения host kernel/HOME/control sockets review не предлагает. Epic `nocx-a0qhd.16` остаётся in progress.

## Основания

- `.agents/skills/bmad-architecture/references/reviewer-gate.md:11` — good-spine checklist.
- `docs/architecture.md:253–265, 273–277` — diagnostics, native artifacts/consumers и operational/release envelope.
- `docs/decisions/0081-filesystem-authority-belongs-to-one-helper-owned-launch.md` — Execution, Launch contract, Guarantee and limitations, Consequences and acceptance.
- `scripts/sandbox-smoke-linux/main.go` — consumer прочитан целиком, включая packaged artifact extraction, old-helper build, diagnostic observer и окончательный result path.

**Условие PASS:** внести R1/R2 в spine и закрыть R3 конкретным контрактом без увеличения ceilings; после этого можно передавать дизайн в реализацию. Проверки реализации здесь не выполнялись.

## Повторный gate после исправлений #7FE6

**Вердикт: FAIL — R1–R3 закрыты на уровне design contract, но конкретизация AD-14 ввела один новый material defect.**

Повторно прочитан обновлённый spine целиком. Input table, Git/compiler/module closure, kernel seccomp/boot contract, численные ресурсы/stage deadlines и cold-cache acceptance закрывают прежние замечания. Проверки/код не запускались и не менялись.

### R4 — Не принимайте QEMU exit status 1 за успешный guest result

**P1; autofix в spine.** AD-14, строки 97–101, назначает guest success value=0, следовательно host success status=1. Этот же exit status QEMU 8.2 использует для собственной ошибки запуска, ещё до PID1 и smoke: `system/vl.c` в `configure_accelerators` вызывает `exit(1)` при неудачной инициализации единственного accelerator; ошибки аргументов также завершаются `exit(1)`. Поэтому принимающий host dispatch, реализованный буквально по AD-14, объявит success при не состоявшемся VM proof. KVM preflight не устраняет коллизию: отдельный последующий запуск QEMU может завершиться ошибкой, и proof result тогда отсутствует.

**Точное исправление:** выбрать непересекающийся success value, например guest `0x10` → host `33`, и failure `0x11` → `35`; явно настроить устройство `isa-debug-exit,iobase=0xf4,iosize=0x04`. Принимать только `33`; QEMU `0`, `1`, timeout/signal и любое другое значение — failure. PID1 сохраняет правило отправки success исключительно после обоих обязательных source consumers либо единственного packaged consumer. Console markers по-прежнему не authority.

Primary evidence:

- [QEMU v8.2.0 debugexit.c](https://github.com/qemu/qemu/blob/v8.2.0/hw/misc/debugexit.c#L32-L36): `exit((val << 1) | 1)`.
- [QEMU v8.2.0 system/vl.c](https://github.com/qemu/qemu/blob/v8.2.0/system/vl.c#L2389-L2395): accelerator initialization failure → `exit(1)`; producer encoding и принимающий host rule сопоставлены, а не проверены изолированно.

**Оставшееся условие PASS:** исправить только эту коллизию result encoding. Остальные прежние rubric blockers закрыты; implementation acceptance по-прежнему требует фактических cold runs и не заявляется этим review.
