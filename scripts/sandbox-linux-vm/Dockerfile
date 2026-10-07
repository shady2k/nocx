FROM ubuntu:24.04 AS userspace

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       bash bc binutils bison ca-certificates coreutils e2fsprogs flex gcc git \
       iproute2 libc6-dev libelf-dev make mount passwd perl python3 tar \
       util-linux xz-utils \
    && rm -rf /var/lib/apt/lists/*

FROM userspace AS kernel-build
WORKDIR /build
COPY scripts/sandbox-linux-vm/kernel.config /tmp/kernel.config
COPY scripts/sandbox-linux-vm/linux-7.2.9.tar.xz /build/linux-7.2.9.tar.xz
# Kconfig parses other architecture menus even for a fixed x86 build.
RUN set -eux; \
    echo 'b4c5dfbe51a364a6c7f03869200f88c8e1f77403539005f14b7fc6bc91b8d8ba  linux-7.2.9.tar.xz' | sha256sum --check --status; \
    tar -xJf linux-7.2.9.tar.xz \
        --exclude='*/Kconfig*' \
        --exclude=linux-7.2.9/Documentation \
        --exclude=linux-7.2.9/arch/arm \
        --exclude=linux-7.2.9/arch/arm64 \
        --exclude=linux-7.2.9/arch/mips \
        --exclude=linux-7.2.9/arch/powerpc \
        --exclude=linux-7.2.9/arch/riscv \
        --exclude=linux-7.2.9/tools/perf \
        --exclude=linux-7.2.9/tools/testing; \
    tar -xJf linux-7.2.9.tar.xz --wildcards \
        'linux-7.2.9/Kconfig' 'linux-7.2.9/*/Kconfig*'; \
    cd linux-7.2.9; \
    make ARCH=x86_64 KCONFIG_ALLCONFIG=/tmp/kernel.config allnoconfig; \
    while IFS= read -r option; do \
        case "$option" in CONFIG_*=*) \
            grep -Fxq "$option" .config || { echo "Missing required kernel option: $option" >&2; exit 1; };; \
        esac; \
    done < /tmp/kernel.config; \
    make ARCH=x86_64 -j2 bzImage; \
    install -D -m 0644 arch/x86/boot/bzImage /out/bzImage; \
    cd /; \
    rm -rf /build

FROM userspace
RUN groupadd --gid 1001 nocx-proof \
    && useradd --uid 1001 --gid 1001 --no-create-home --home-dir /home/nocx-proof --shell /bin/bash nocx-proof \
    && install -d -o root -g root -m 0755 /opt/kernel /opt/nocx /ci \
    && install -d -o 1001 -g 1001 -m 0700 /home/nocx-proof

COPY --from=kernel-build /out/bzImage /opt/kernel/bzImage
COPY scripts/sandbox-linux-vm/init.sh /ci-init
COPY scripts/sandbox-linux-vm/guest-proof.sh /opt/nocx/guest-proof.sh
COPY scripts/sandbox-linux-vm/exit.c /tmp/exit.c
RUN chmod 0755 /ci-init /opt/nocx/guest-proof.sh \
    && gcc -O2 -Wall -Wextra -Werror -o /opt/nocx/debug-exit /tmp/exit.c \
    && chmod 0755 /opt/nocx/debug-exit \
    && rm /tmp/exit.c \
    && mkdir -p /ci/repo /ci/modules /tmp/go-build \
    && chmod 0755 /ci/repo /ci/modules \
    && chmod 1777 /tmp
