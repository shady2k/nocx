# Adversarial review — Sandbox Linux CI VM

**Вердикт: нужны уточнения контракта до реализации.** Пять замечаний ниже относятся к интерфейсам draft, а не к существующим smoke. Исходники и spine не изменены. Проверка статическая: build/lint/tests/formatters и запуск VM не выполнялись. `git diff` для draft пуст; поэтому якоря ниже относятся к строкам представленного design artifact, а не к выдуманному code patch.

## Две независимо согласованные реализации

Это контрмодели спецификации, не написанный или запущенный код. Каждая тройка host-builder / guest-init / workflow внутренне согласована с AD-11…AD-15; при обмене компонентами они несовместимы.

| Интерфейс                 | Реализация A                                                                                                                        | Реализация B                                                                                                                    | Контрпример при смешивании                                                                                                           |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| Source и generated inputs | Checkout в `/work/src`; vendor и notices материализованы по repository-relative paths; оба helper archive лежат в embed directories | Tracked source в `/opt/src`; verified assets и неизменённые downloads в `/inputs`; собственный init раскладывает их перед smoke | Builder B + init A: отсутствуют generated inputs там, где их читает smoke                                                            |
| Git и UID                 | UID1001 владеет source и bare repo `/work/history.git`; init задаёт `GIT_DIR` только для доказательств                              | Bare repo `/inputs/history.git`; init создаёт `.git` pointer и передаёт владение UID1001                                        | Builder B + init A: `git -C <cwd> archive` не находит repository; сохранённый root-owned repo также требует согласованного ownership |
| Offline Go                | SDK `/opt/go`, cache `/opt/modules`; init явно задаёт пути и writable HOME/build cache                                              | SDK в системном PATH, module cache перенесён в guest GOPATH; builder заранее создаёт writable UID1001 cache                     | Builder A + init B: Go смотрит не в staged modules; без сети сборка прекращается                                                     |
| Lane                      | Workflow вызывает `run.sh source`; host пишет `/etc/nocx-vm-lane`; init читает файл                                                 | Та же CLI; host передаёт `nocx.lane=source` в kernel command line; init читает `/proc/cmdline`                                  | Host A + init B: явный CLI lane не достигает consumer; корректный fail-closed init отвергает запуск                                  |
| Terminal status           | PID1 пишет success `0x10`, failure `0x11`; host принимает только status 33                                                          | PID1 пишет success `0x11`, failure `0x10`; host принимает только status 35                                                      | Init B + host A: завершившаяся неуспехом proof даёт status 33 и становится зелёной                                                   |

Ни один вариант не требует изменения host kernel/HOME, exports, сети гостя, helper consumers или ABI9. Следовательно, общий набор инвариантов ещё не задаёт совместимый seam. Ниже — минимальные уточнения, которые исключают эти расхождения.

## 1. P1 — Зафиксировать layout generated inputs, а не только tracked source

**Якорь:** `architecture-sandbox-linux-ci-vm.md:55–56,65–67`.

**Контрпример.** Builder вправе понимать «verified VT assets» как скачанные archives в `/inputs`, а «downloaded release archives» как отдельный artifact directory. Guest, ожидающий готовый checkout, запускает существующий consumer из source root. `buildOldHelper` читает именно `build/libghostty-vt/vendor` и `internal/helper/notices/licenses/THIRD_PARTY_LICENSES.txt`; этих generated файлов нет в tracked-source snapshot. Даже packaged lane строит old helper и probe, поэтому одних current helper archives недостаточно. Более того, source/old helper используют `CGO_ENABLED=1` без `vtmusl`: только musl assets для release helper не удовлетворяют native GNU link paths. Если release local и remote archives с одинаковыми basenames распаковать в один каталог, одна вариация вытеснит другую; если сохранить downloads в отдельном каталоге, embed их не увидит.

**Доказательства:** `scripts/sandbox-smoke-linux/main.go:92–110,398–428,448–477`; `internal/helper/deploy/artifacts/source.go` (`all:bin`, `artifactsInDir`, пропуск subdirectories); `Makefile:155–189,306–309`; `third_party/libghostty-vt/README.md`, разделы «Six archives, four targets» и «Where a build finds them»; `.github/workflows/release.yml:348–360`.

**Минимальная правка:** одна обязательная input-layout таблица: source root/cwd; `build/libghostty-vt/vendor/<native-gnu-target>/` с matching headers и archive; staged `internal/helper/notices/licenses/THIRD_PARTY_LICENSES.txt`; `internal/helper/deploy/artifacts/bin/nocx-helper-linux-<arch>.gz` и `bin/local/nocx-helper-linux-<arch>.gz`. Остальные скачанные release archives сохранять в исходной структуре без recompression/rebuild; переносить точные `.gz` bytes до компиляции smoke. Указать владельца материализации layout — builder или init, не оба по догадке. Host и guest target architecture должны совпадать с выбранными archives.

## 2. P1 — Привязать bare object store к неизменённому git consumer

**Якорь:** `architecture-sandbox-linux-ci-vm.md:55–58`.

**Контрпример.** Отдельный credential-free bare repo удовлетворяет AD-12, но само его присутствие не делает tracked-source directory Git repository. Existing smoke выполняет `git -C repositoryRoot() archive --format=tar 3160c5c6cfcee34b166e13480f1f71de2ac9901c`, не принимает путь к bare store и использует cwd как repositoryRoot. Без `.git` pointer либо корректного inherited `GIT_DIR` обе lane ломаются при old-generation proof. Даже store с одним текущим commit формально является object store, но не содержит обязательный historical tree. Ownership также пересекает эту границу: root-populated repo и UID1001 consumer нельзя согласовать одной фразой «preserves filesystem ownership».

**Доказательства:** `scripts/sandbox-smoke-linux/main.go:39,110–111,448–462,515–519`; workflows уже используют `fetch-depth: 0` (`ci.yml:226–228`, `release.yml:343–345`).

**Минимальная правка:** выбрать один способ discovery, например guest `.git` pointer на фиксированный bare repo; явно потребовать полный commit/tree/blob closure указанного oldHelperRevision без alternates на host. Source root и Git metadata/store назначать UID/GID1001 до запуска; consumer работает с cwd=source root. Не экспортировать `GIT_WORK_TREE` на весь процесс: inherited Git overrides должны оставаться совместимыми с `go build` в отдельно распакованном old tree. Root сохраняет владение системными файлами, а не всеми proof inputs.

## 3. P1 — Определить доступный UID1001 offline Go environment и native compiler

**Якорь:** `architecture-sandbox-linux-ci-vm.md:56–58,68–69`.

**Контрпример.** Cache, подготовленный root builder в `/root/go/pkg/mod`, и cache в `/opt/modules` одинаково являются «public module cache». UID1001 с собственным HOME и стандартным GOPATH не использует ни один автоматически; `/root` ещё и недоступен. Перенос SDK без PATH также не помогает: consumer вызывает именно `exec.CommandContext(..., "go", "build", ...)`, независимо от способа запуска внешнего smoke. Ubuntu userspace + Go SDK не гарантируют наличие C compiler и development headers, а обе lane собирают historical helper с `CGO_ENABLED=1` и external linking. Ошибка возникает до проверки sandbox и не исправляется добавлением только release binaries.

**Доказательства:** `scripts/sandbox-smoke-linux/main.go:93–103,471–477`; `go.mod` и `git show 3160c5c6cfcee34b166e13480f1f71de2ac9901c:go.mod` требуют Go 1.26. Исторический module graph действительно отдельный вход; проверенные версии основных dependencies совпадают, поэтому здесь не утверждается, будто уже обнаружена отсутствующая старая версия.

**Минимальная правка:** зафиксировать guest PATH с Go SDK и tool binaries (`git`, `tar`, native C compiler/linker); подходящие libc development inputs; HOME, TMPDIR, GOPATH, GOMODCACHE, GOCACHE и их ownership/access для UID1001. Зафиксировать `GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`; подготовить module/download metadata для фактических current smoke/probe/source и historical-helper build graphs до boot. Использовать guest-local writable cache, не host HOME. Missing-input failure остаётся обязательным, но не заменяет определение самих inputs.

## 4. P2 — Определить доставку lane до PID1 и точный dispatch

**Якорь:** `architecture-sandbox-linux-ci-vm.md:65–69,84–85`.

**Контрпример.** Явный `run.sh source` определяет только workflow→host boundary. Он не определяет host→guest boundary: файл и kernel command line из таблицы выше одинаково допустимы. При смешивании lane отсутствует у init. Передать host lane напрямую в smoke тоже недостаточно: один `go run ... source` запускает только source consumer; это не VM lane `source`, которому нужны оба consumer. Existing make target кажется готовым dispatcher, но `make sandbox-smoke-linux` имеет prerequisite `helpers-this-machine` и в госте заново строит current helpers, нарушая заявленное host-only размещение build.

**Доказательства:** `scripts/sandbox-smoke-linux/main.go:60–67,98–109`; `Makefile:306–309,350–351`; `.github/workflows/release.yml:361–365`.

**Минимальная правка:** зафиксировать `run.sh <source|packaged>` и ровно один guest transport/path/key; PID1 отвергает отсутствующее, повторное или неизвестное значение. Указать таблицу dispatch: VM `source` последовательно выполняет existing `go run ./scripts/sandbox-smoke-linux source`, затем `... packaged`; VM `packaged` выполняет только `... packaged`. В обоих случаях cwd=source root, `NOCX_SANDBOX_SMOKE_MANDATORY=1`, staged inputs, без helper-building make target внутри VM. Неуспех любого обязательного шага делает общий результат неуспешным.

## 5. P1 — Зафиксировать wire encoding genuine terminal result

**Якорь:** `architecture-sandbox-linux-ci-vm.md:75–78`.

**Контрпример.** Формулировка «only its encoded success exit» не определяет, какое именно значение является success. Обе кодировки из таблицы удовлетворяют ей в собственной паре PID1/host; после замены одного компонента failure становится success. Это не проблема console parsing и не отдельный security workstream — это несовместимость producer/consumer одного status protocol. Дополнительно device port/width должны быть одинаковы у guest writer и QEMU configuration. Агрегация двух source-lane commands должна предшествовать единственному terminal write: печать `LINUX_NATIVE_*_PROOF_COMPLETE` не означает process exit, поскольку после неё ещё выполняются Go deferred cleanup.

**Доказательства:** [QEMU 8.2.0 `hw/misc/debugexit.c`](https://raw.githubusercontent.com/qemu/qemu/v8.2.0/hw/misc/debugexit.c): `exit((val << 1) | 1)`, default `iobase=0x501`, `iosize=0x02`, access widths 1–4. Consumer smoke регистрирует cleanup через defer (`main.go:78–82` и cleanup handlers) и печатает completion marker до возврата main.

**Минимальная правка:** выбрать фиксированные port/width и reserved wire values, например QEMU `iobase=0xf4,iosize=0x04`, 32-bit write: success `0x10`→host status 33, failure `0x11`→35. PID1 ждёт реальный exit обязательных commands, нормализует любой nonzero/signal/setup failure в failure, никогда не пересылает raw child status как device value. Host захватывает статус именно QEMU, не `tee`/cleanup, принимает только 33 после запуска VM, все прочие состояния, timeout и отсутствие terminal result отвергает. Cleanup не заменяет уже выбранный failure на 0. Нужна единая таблица, а не независимо выбранные константы.

## Границы решения

Уточнения не требуют application dependency changes, suppressions Darwin lint, изменения ABI9 gate или перестройки downloaded one-origin helpers. Native macOS остаётся без изменений; critical npm audit и Darwin lint остаются blockers. Никаких PR/publish/merge. Job ceilings остаются 60m source / 30m packaged, smoke context 15m не увеличивается. Сам review не доказывает, что cold VM build укладывается в эти ceilings.

После согласования контракта main agent должен проверять именно seam: UID1001 + offline old-helper build; сохранность layout и точных release `.gz`; dispatch обеих source commands; отсутствие/неизвестный lane; провал первого/второго consumer, abnormal QEMU exit и timeout, успешный real exit после cleanup. Здесь эти проверки не выполнялись.

## Повторное чтение spine #7FE6

**Вердикт: remaining material gap; пункты 1–4 закрыты на уровне design contract.** Таблица `/ci/repo`/vendor/notices/archives, credential-free Git store с historical tree и UID1001, clean offline Go/compiler environment и `/ci/lane` с точным двухшаговым dispatch устраняют исходные контрмодели. Это оценка спецификации, не подтверждение работоспособности VM.

**P1 — Не принимайте QEMU exit 1 за доказательство успешного guest result.** Новый AD-14 (`architecture-sandbox-linux-ci-vm.md:97–101`) назначает device value 0→process exit 1 для success и принимает только exit 1. Но QEMU 8.2.0 сам завершает процесс с exit(1), если `-kernel` невозможно открыть, header невозможно прочесть или kernel header недопустим: [hw/i386/x86.c, x86_load_linux, строки 815–828 и 1053–1054](https://raw.githubusercontent.com/qemu/qemu/v8.2.0/hw/i386/x86.c). В таком случае PID1 вообще не запускался и isa-debug-exit write отсутствует, однако host получает ровно выбранный success status. Проверка KVM до boot не различает эти исходы. Фраза «Missing result ... fails» противоречит назначенному единственному status discriminator.

**Минимальная правка:** сохранить фиксированные port/width, ожидание real exits и всю текущую агрегацию, но назначить reserved device values success=0x10 и failure=0x11, принимая только QEMU status 33, а 35 и все другие результаты отвергать. Явно конфигурировать соответствующее устройство `isa-debug-exit,iobase=0xf4,iosize=0x04`. Это исправление существующего terminal protocol, не дополнительный канал/console marker и не отдельная hardening-задача. Никаких code/checks не выполнено; изменён только этот review.
