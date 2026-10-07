#!/bin/sh
# Builds a fully static (musl) BCH2 node inside an Alpine container of the target platform; run by
# build-release.sh. Expects the node source at /w/src and builds in /w/work-<arch>, kept between
# runs so depends and objects are reused. Output: /w/out/<arch>/{bitcoincashIId,bitcoincashII-cli}.
# ARCH names the build when uname cannot (armv6l: QEMU reports armv7l, but the armhf image's
# compiler targets ARMv6).
set -eux
ARCH=${ARCH:-$(uname -m)}
J=${JOBS:-$(nproc)}
mkdir -p /w/logs
apk add --no-cache build-base autoconf automake libtool pkgconf linux-headers bash curl make cmake python3 patch xz file >/dev/null
[ -d /w/work-$ARCH ] || cp -a /w/src /w/work-$ARCH
cd /w/work-$ARCH
make -C depends -j"$J" NO_QT=1 NO_WALLET=1 NO_UPNP=1 NO_NATPMP=1 NO_USDT=1 NO_MULTIPROCESS=1 >/w/logs/depends-$ARCH.log 2>&1
HOST=$(ls -d depends/*-linux-* | grep -v '\.' | head -1 | xargs basename)
if [ ! -f config.status ]; then
  ./autogen.sh >/w/logs/autogen-$ARCH.log 2>&1
  CONFIG_SITE=$PWD/depends/$HOST/share/config.site ./configure --disable-wallet --with-gui=no --without-miniupnpc --without-natpmp \
    --disable-bench --disable-fuzz-binary --disable-tests --disable-gui-tests --enable-reduce-exports --disable-shared \
    LDFLAGS="-static-libgcc -static-libstdc++" >/w/logs/configure-$ARCH.log 2>&1
fi
# libtool drops a plain -static; -all-static links the programs fully static. (-static-pie
# builds crash at start, so these are plain static executables.)
make -j"$J" LIBTOOL_APP_LDFLAGS=-all-static >/w/logs/make-$ARCH.log 2>&1
mkdir -p /w/out/$ARCH
for b in bitcoincashIId bitcoincashII-cli; do strip -o /w/out/$ARCH/$b src/$b; done
file /w/out/$ARCH/*
if readelf -l /w/out/$ARCH/bitcoincashIId | grep -q INTERP; then echo "NOT STATIC $ARCH"; exit 1; fi
/w/out/$ARCH/bitcoincashIId -version | head -1
echo "BUILD OK $ARCH"
