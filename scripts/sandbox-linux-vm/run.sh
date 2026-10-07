#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

fail() { printf 'sandbox-linux-vm: %s\n' "$*" >&2; exit 1; }
[[ $# == 1 && ( $1 == source || $1 == packaged ) ]] || fail 'usage: scripts/sandbox-linux-vm/run.sh source|packaged'
lane=$1
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'requires Linux x86_64'
for tool in docker qemu-system-x86_64 go git tar sha256sum timeout mktemp df du cp mv find curl realpath truncate cut tail tr head python3; do command -v "$tool" >/dev/null || fail "missing required tool: $tool"; done
[[ -r /dev/kvm && -w /dev/kvm ]] || fail '/dev/kvm must be readable and writable by the invoking user'

repo=$(git rev-parse --show-toplevel) || fail 'must run inside a Git working tree'
repo=$(realpath "$repo")
for input in Dockerfile kernel.config init.sh guest-proof.sh exit.c; do [[ -f "$repo/scripts/sandbox-linux-vm/$input" ]] || fail "VM image input is missing: $input"; done
[[ -f "$repo/build/libghostty-vt/vendor/linux-amd64-gnu/libghostty-vt.a" ]] || fail 'verified Linux VT vendor archive is missing; fetch it before launching'
[[ -f "$repo/internal/helper/notices/licenses/THIRD_PARTY_LICENSES.txt" ]] || fail 'verified generated VT notices are missing; fetch them before launching'
for f in "$repo"/internal/helper/deploy/artifacts/bin/nocx-helper-*.gz "$repo"/internal/helper/deploy/artifacts/bin/local/nocx-helper-*.gz; do [[ -f $f ]] || fail "required helper archive missing: $f"; done

# Confirm QEMU can create a KVM VM, not merely open the device node.
python3 - <<'PY'
import fcntl
import os

with open("/dev/kvm", "r+b", buffering=0) as kvm:
    vm = fcntl.ioctl(kvm.fileno(), 0xAE01, 0)  # KVM_CREATE_VM
    os.close(vm)
PY

# Reserve a size-dependent staging copy and rootfs headroom before any duplication.
repo_bytes=$(du -s -B1 "$repo" | cut -f1)
avail_bytes=$(df -B1 --output=avail "$repo" | tail -n1 | tr -d ' ')
preflight=$(( repo_bytes * 2 + 2 * 1024 * 1024 * 1024 ))
(( avail_bytes >= preflight )) || fail "insufficient project-filesystem space before staging: need ${preflight} bytes; have ${avail_bytes}"
# All bulky work stays on the repository filesystem, in this invocation's private directory.
work=$(mktemp -d "$repo/.sandbox-linux-vm.XXXXXXXX") || fail 'cannot create private project-filesystem temp directory'
chmod 700 "$work"
image="nocx-sandbox-linux-vm:$(cat /proc/sys/kernel/random/uuid)"
image_build_attempted=0
container_id=
builder_id=
buildkit=
qemu_pid=
cleanup() {
  rc=$?
  trap - EXIT INT TERM HUP
  set +e
  cleanup_failed=0
  local cleanup_deadline=$((SECONDS + 60))
  cleanup_run() {
    local limit=$1
    shift
    local remaining=$((cleanup_deadline - SECONDS - 1))
    (( remaining > 0 )) || return 1
    (( limit <= remaining )) || limit=$remaining
    timeout --kill-after=1 "$limit" "$@"
  }
  if [[ -n $qemu_pid ]]; then kill -TERM "$qemu_pid" 2>/dev/null; wait "$qemu_pid" 2>/dev/null; fi
  if [[ -n $builder_id ]] && ! cleanup_run 15 docker rm -f "$builder_id" >/dev/null; then cleanup_failed=1; fi
  if [[ -n $container_id ]] && ! cleanup_run 15 docker rm -f "$container_id" >/dev/null; then cleanup_failed=1; fi
  if [[ -n $buildkit ]] && ! cleanup_run 15 docker buildx rm --force "$buildkit" >/dev/null; then cleanup_failed=1; fi
  if (( image_build_attempted )); then
    image_id=$(cleanup_run 5 docker image ls --quiet --filter "reference=$image") || cleanup_failed=1
    if [[ -n $image_id ]] && ! cleanup_run 10 docker image rm "$image" >/dev/null; then cleanup_failed=1; fi
  fi
  if ! cleanup_run 15 bash -c 'chmod -R u+rwX "$1" && rm -rf -- "$1"' -- "$work"; then cleanup_failed=1; fi
  if (( cleanup_failed )); then
    printf 'sandbox-linux-vm: owned-resource cleanup failed\n' >&2
    (( rc != 0 )) || rc=1
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

stage_deadline=$((SECONDS + 240))
mkdir -p "$work/stage/ci/repo" "$work/stage/ci/modules" "$work/stage/opt/go" "$work/gomodcache" "$work/gopath" "$work/oldtree"
# Stage only tracked working-tree files; never copy the caller's .git/config or credentials.
git -C "$repo" ls-files -z --cached -- . ':!.beads/**' | tar -C "$repo" --null --verbatim-files-from --no-recursion -T - -cf - | tar -xf - -C "$work/stage/ci/repo"
# Import only this HEAD's object closure, without config, remotes or unrelated refs.
historical=3160c5c6cfcee34b166e13480f1f71de2ac9901c
git -C "$repo" bundle create "$work/repo.bundle" HEAD
object_store="$work/stage/ci/repo/.git"
git init --bare "$object_store"
git --git-dir="$object_store" bundle unbundle "$work/repo.bundle" >/dev/null
git --git-dir="$object_store" update-ref refs/heads/vm-proof "$(git -C "$repo" rev-parse HEAD)"
git --git-dir="$object_store" symbolic-ref HEAD refs/heads/vm-proof
git --git-dir="$object_store" cat-file -e "$historical^{commit}" || fail "object store lacks required historical commit $historical"
git --git-dir="$object_store" config core.bare false
git --git-dir="$object_store" config core.worktree "$work/stage/ci/repo"

# Bring only the required generated assets and exact helper archives into the guest tree.
for rel in build/libghostty-vt/vendor internal/helper/notices/licenses/THIRD_PARTY_LICENSES.txt internal/helper/runner/bin/linux-amd64; do
  mkdir -p "$work/stage/ci/repo/$(dirname "$rel")"
  cp -aT "$repo/$rel" "$work/stage/ci/repo/$rel"
done
mkdir -p "$work/stage/ci/repo/internal/helper/deploy/artifacts/bin/local"
for helper in "$repo"/internal/helper/deploy/artifacts/bin/nocx-helper-*.gz; do cp -a "$helper" "$work/stage/ci/repo/internal/helper/deploy/artifacts/bin/"; done
for helper in "$repo"/internal/helper/deploy/artifacts/bin/local/nocx-helper-*.gz; do cp -a "$helper" "$work/stage/ci/repo/internal/helper/deploy/artifacts/bin/local/"; done
(( SECONDS <= stage_deadline )) || fail 'input staging exceeded four minutes'
# Resolve the exact consumer build graphs into a fresh public cache before boot,
# rather than downloading unrelated application/UI/tool modules.
goroot=$(go env GOROOT)
[[ -x "$goroot/bin/go" ]] || fail 'Go SDK is unavailable'
cp -a "$goroot/." "$work/stage/opt/go/"
export PATH="$goroot/bin:$PATH"
unset GOFLAGS GOPRIVATE GONOSUMDB GONOPROXY GOINSECURE
export GOENV=off GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 GOMODCACHE="$work/gomodcache" GOPATH="$work/gopath" GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
(
  cd "$work/stage/ci/repo"
  remaining=$((stage_deadline - SECONDS))
  (( remaining > 0 )) || fail 'input staging exceeded four minutes'
  timeout "$remaining" go list -deps ./scripts/sandbox-smoke-linux ./scripts/sandbox-smoke-linux/probe >/dev/null
  if [[ $lane == source ]]; then
    remaining=$((stage_deadline - SECONDS))
    (( remaining > 0 )) || fail 'input staging exceeded four minutes'
    timeout "$remaining" go list -deps -tags nocx_local_ssh ./cmd/nocx-helper >/dev/null
  fi
)
git -C "$repo" archive "$historical" | tar -xf - -C "$work/oldtree" --no-same-owner
oldtree="$work/oldtree"
(
  cd "$oldtree"
  remaining=$((stage_deadline - SECONDS))
  (( remaining > 0 )) || fail 'input staging exceeded four minutes'
  timeout "$remaining" go list -deps ./cmd/nocx-helper >/dev/null
)
# Move the invocation-owned cache instead of keeping a redundant staging copy.
rmdir "$work/stage/ci/modules"
mv "$work/gomodcache" "$work/stage/ci/modules"
# Download integrity has already been checked. Offline builds use extracted module
# directories plus their checksum/graph metadata, not retained download ZIPs.
find "$work/stage/ci/modules/cache/download" -type f -name '*.zip' -delete
rm -rf -- "$work/oldtree" "$work/repo.bundle"
(( SECONDS <= stage_deadline )) || fail 'input staging exceeded four minutes'
# Graph inspection uses the host staging path; the offline consumer uses its
# final guest worktree, without disabling genuine VCS status checks.
git --git-dir="$object_store" config core.worktree /ci/repo

printf '%s' "$lane" > "$work/stage/ci/lane"
chmod 0444 "$work/stage/ci/lane"

# Reserve all three payload copies plus base image/kernel-build headroom.
source_bytes=$(du -s -B1 "$work/stage" | cut -f1)
avail_bytes=$(df -B1 --output=avail "$repo" | tail -n1 | tr -d ' ')
required=$(( source_bytes * 3 + 2 * 1024 * 1024 * 1024 ))
(( avail_bytes >= required )) || fail "insufficient project-filesystem space before disk construction: need ${required} bytes for ${source_bytes}-byte inputs; have ${avail_bytes}"

mkdir -p "$work/build-context/scripts/sandbox-linux-vm"
for input in Dockerfile kernel.config init.sh guest-proof.sh exit.c; do cp "$repo/scripts/sandbox-linux-vm/$input" "$work/build-context/scripts/sandbox-linux-vm/$input"; done
stage_remaining=$((stage_deadline - SECONDS))
(( stage_remaining > 0 )) || fail 'input staging exceeded four minutes'
build_deadline=$((SECONDS + 720))
# Download with the host resolver; do not depend on nested BuildKit DNS. The
# image checks the pinned SHA-256 before extracting the exact supplied bytes.
timeout 720 curl --fail --location --silent --show-error https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.2.9.tar.xz --output "$work/build-context/scripts/sandbox-linux-vm/linux-7.2.9.tar.xz"
buildkit="nocx-sandbox-vm-${image##*:}"
docker buildx create --name "$buildkit" --driver docker-container --driver-opt image=moby/buildkit:v0.33.1,memory=2g,memory-swap=2g,cpu-quota=200000,cpu-period=100000,restart-policy=no >/dev/null
build_remaining=$((build_deadline - SECONDS))
(( build_remaining > 0 )) || fail 'base image build exceeded 12 minutes'
image_build_attempted=1
timeout "$build_remaining" docker buildx build --builder "$buildkit" --load -f "$work/build-context/scripts/sandbox-linux-vm/Dockerfile" -t "$image" "$work/build-context"
(( SECONDS <= build_deadline )) || fail 'base image build exceeded 12 minutes'
timeout 15 docker buildx rm --force "$buildkit" >/dev/null
buildkit=
stage_deadline=$((SECONDS + stage_remaining))
container_id=$(docker create --network none "$image" /bin/bash -euc 'chown root:root / /ci /opt /opt/go /ci/lane; chmod 0755 / /ci /opt /opt/go; chown -R 1001:1001 /ci/repo /ci/modules; chmod 0444 /ci/lane')
remaining=$((stage_deadline - SECONDS))
(( remaining > 0 )) || fail 'input staging exceeded four minutes before container copy'
timeout "$remaining" docker cp "$work/stage/." "$container_id:/"
remaining=$((stage_deadline - SECONDS))
(( remaining > 0 )) || fail 'input staging exceeded four minutes before ownership setup'
timeout "$remaining" docker start -a "$container_id"
(( SECONDS <= stage_deadline )) || fail 'input staging exceeded four minutes'
# Export the stopped staged container directly into ext4 without retaining a tar copy.
disk="$work/guest.raw"
truncate -s 8G "$disk"
builder_id=$(docker create --network none -i -v "$disk:/out/guest.raw" "$image" /bin/bash -euc 'mkdir /rootfs; tar -xpf - -C /rootfs; mkfs.ext4 -F -d /rootfs /out/guest.raw >/dev/null')
remaining=$((stage_deadline - SECONDS))
(( remaining > 0 )) || fail 'input staging exceeded four minutes before disk construction'
timeout "$remaining" docker export "$container_id" | timeout "$remaining" docker start -ai "$builder_id"
remaining=$((stage_deadline - SECONDS))
(( remaining > 0 )) || fail 'input staging exceeded four minutes before disk builder removal'
timeout "$remaining" docker rm "$builder_id" >/dev/null
builder_id=
kernel="$work/kernel"
remaining=$((stage_deadline - SECONDS))
(( remaining > 0 )) || fail 'input staging exceeded four minutes before kernel extraction'
timeout "$remaining" docker cp "$container_id:/opt/kernel/bzImage" "$kernel"
chmod 600 "$kernel"
(( SECONDS <= stage_deadline )) || fail 'input staging exceeded four minutes'
printf 'QEMU: %s\n' "$(qemu-system-x86_64 --version | head -n1)"
printf 'Guest kernel SHA-256: %s\n' "$(sha256sum "$kernel" | cut -d' ' -f1)"
serial="$work/serial.log"
vm_timeout=2100
[[ $lane != packaged ]] || vm_timeout=600
set +e
timeout --signal=TERM --kill-after=5 "$vm_timeout" qemu-system-x86_64 \
  -machine q35,accel=kvm -cpu host -smp 2 -m 2048 \
  -kernel "$kernel" -append 'console=ttyS0 root=/dev/vda rw rootfstype=ext4 init=/ci-init lsm=landlock panic=-1' \
  -drive "file=$disk,if=virtio,format=raw,cache=none" -nographic -no-reboot \
  -device isa-debug-exit,iobase=0xf4,iosize=0x04 -nic none \
  >"$serial" 2>&1 &
qemu_pid=$!
wait "$qemu_pid"
qemu_status=$?
qemu_pid=
set -e
cat "$serial"
[[ $qemu_status == 33 ]] || fail "guest did not report the required success status 33 (QEMU status $qemu_status)"
printf 'sandbox-linux-vm: %s lane passed in isolated Linux 7.2.9 guest\n' "$lane"
