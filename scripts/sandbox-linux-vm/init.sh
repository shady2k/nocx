#!/bin/bash
set -Eeuo pipefail

signal_failure() {
  /opt/nocx/debug-exit 0x11 || true
  while :; do sleep 3600; done
}
trap signal_failure ERR

mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev
mkdir -p /dev/pts /dev/shm /tmp /home/nocx-proof
mount -t devpts devpts /dev/pts
mount -t tmpfs -o mode=1777,nosuid,nodev tmpfs /tmp
mkdir -p /tmp/go-build
chmod 1777 /tmp
chown 1001:1001 /tmp/go-build /home/nocx-proof
chmod 0700 /home/nocx-proof
ip link set lo up

if [[ ! -f /ci/lane || -L /ci/lane ]]; then
  echo 'missing regular /ci/lane input' >&2
  signal_failure
fi
lane_meta=$(stat -c '%u:%g:%a' /ci/lane)
if [[ "$lane_meta" != '0:0:444' ]]; then
  echo 'invalid /ci/lane ownership or permissions' >&2
  signal_failure
fi
lane=$(cat /ci/lane)
case "$lane" in
  source|packaged) ;;
  *) echo 'invalid /ci/lane value' >&2; signal_failure ;;
esac

mkdir -p /ci/repo /ci/modules
chown -R 1001:1001 /ci/repo /ci/modules
chmod 0755 /ci/repo /ci/modules

set +e
env -i \
  HOME=/home/nocx-proof \
  USER=nocx-proof \
  LOGNAME=nocx-proof \
  PATH=/opt/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  GOROOT=/opt/go \
  GOTOOLCHAIN=local \
  GOPROXY=off \
  GOSUMDB=off \
  GOMODCACHE=/ci/modules \
  GOCACHE=/tmp/go-build \
  CGO_ENABLED=1 \
  NOCX_SANDBOX_SMOKE_MANDATORY=1 \
  TMPDIR=/tmp \
  setpriv --reuid=1001 --regid=1001 --clear-groups \
    /opt/nocx/guest-proof.sh "$lane"
proof_status=$?
set -e
if (( proof_status != 0 )); then
  echo "guest proof failed with status $proof_status" >&2
  signal_failure
fi

trap - ERR
/opt/nocx/debug-exit 0x10
# A successful debug-exit is expected to terminate the VM. Never turn a
# returning or failed success report into a different terminal result.
echo 'debug-exit success signal returned unexpectedly' >&2
signal_failure
