#!/bin/sh
# Forge Solo for Linux release tarballs:
#   dist/forge-solo-<VERSION>-linux-{x86_64,aarch64,armv7l}.tar.gz and dist/SHA256SUMS-linux
#
# The BCH2 node is built fully static (musl, Alpine 3.20, Bitcoin Core's depends) from the public
# bitcoincashII-core tag v27.0.2, so one binary runs on any Linux distribution, old or new, glibc
# or musl. The published node binaries need a recent glibc and libstdc++ (GLIBCXX_3.4.32) and do
# not start on Debian 12 or Ubuntu 22.04. The Go programs are static too (CGO_ENABLED=0; the
# database is SQLite, which is pure Go here).
#
# Needs Go and Docker. The ARM and RISC-V node builds run under QEMU (register it once with
# `docker run --privileged --rm tonistiigi/binfmt --install arm64,arm,riscv64`) and take hours.
# Node builds are kept in $WORK (default .linux-build) and reused.
#
# Usage: scripts/linux/build-release.sh VERSION [ARCH...]
#   ARCH: x86_64 aarch64 armv7l armv6l i686 riscv64 (default: all), named as `uname -m` names them
set -eu
VERSION=${1:?usage: scripts/linux/build-release.sh VERSION [ARCH...]}
shift
ARCHES=${*:-x86_64 aarch64 armv7l armv6l i686 riscv64}
NODE_REPO=https://github.com/BitcoincashII/bitcoincashII-core.git
NODE_TAG=v27.0.2
NODE_COMMIT=a1668c6ab9626156d9b4d3e38e92363f354e8e5a
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
WORK=${WORK:-$ROOT/.linux-build}
DIST=$ROOT/dist

mkdir -p "$WORK/logs" "$DIST"
if [ ! -d "$WORK/src" ]; then
  rm -rf "$WORK/src.tmp"
  git clone --quiet --depth 1 --branch "$NODE_TAG" "$NODE_REPO" "$WORK/src.tmp"
  got=$(git -C "$WORK/src.tmp" rev-parse HEAD)
  if [ "$got" != "$NODE_COMMIT" ]; then
    echo "$NODE_TAG is $got, expected $NODE_COMMIT: refusing to build" >&2
    exit 1
  fi
  rm -rf "$WORK/src.tmp/.git"
  mv "$WORK/src.tmp" "$WORK/src"
fi
cp "$ROOT/scripts/linux/build-node.sh" "$WORK/build-node.sh"

for arch in $ARCHES; do
  # Alpine 3.20, pinned per platform: the build runs in the image of the target platform, whose
  # compiler sets the baseline (armhf: ARMv6 with VFP, so armv6l also runs on every 32-bit ARM Pi).
  # linux32 makes a 32-bit x86 container report i686, as the node's build system reads it.
  pre= goarm=
  case $arch in
    x86_64) plat=linux/amd64 goarch=amd64 digest=c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e ;;
    aarch64) plat=linux/arm64 goarch=arm64 digest=45e09956dc667c5eff3583c9d94830261fb1ca0be10a0a7db36266edf5de9e1d ;;
    armv7l) plat=linux/arm/v7 goarch=arm goarm=7 digest=bd05c4d38cbeb5cfb34883906276560353a6b0282fb5c4b9dd3bd40f5143d7c3 ;;
    armv6l) plat=linux/arm/v6 goarch=arm goarm=6 digest=37753b2965043543542cd9d48cba044151052ca14fa72e6cbec4edf2a1490599 ;;
    i686) plat=linux/386 goarch=386 pre=linux32 digest=4ec3ead63e75e791660b5abc4dc3bd85ebb6fc24a10d649ba05991bf34b5e80e ;;
    riscv64) plat=linux/riscv64 goarch=riscv64 digest=c47cbcc0d9c7f68d8e19c8e4eb50252ff9722e504108d69e8c36d510e921fc26 ;;
    *) echo "unknown architecture $arch (x86_64 aarch64 armv7l armv6l i686 riscv64)" >&2; exit 1 ;;
  esac
  if [ ! -x "$WORK/out/$arch/bitcoincashIId" ]; then
    docker run --rm --platform "$plat" -e ARCH="$arch" -v "$WORK:/w" "alpine@sha256:$digest" $pre sh /w/build-node.sh \
      >"$WORK/logs/run-$arch.log" 2>&1 || { echo "node build failed for $arch: see $WORK/logs/" >&2; exit 1; }
  fi
  pkg=forge-solo-$VERSION-linux-$arch
  stage=$WORK/stage/$pkg
  rm -rf "$stage"
  mkdir -p "$stage/bin"
  for prog in stratum api; do
    (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm \
      go build -tags sqlite -trimpath -ldflags "-s -w" -o "$stage/bin/$prog" "./cmd/$prog")
  done
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$stage/forge-solo" ./cmd/forge-solo-linux)
  cp "$WORK/out/$arch/bitcoincashIId" "$WORK/out/$arch/bitcoincashII-cli" "$stage/bin/"
  cp "$WORK/src/COPYING" "$stage/bin/COPYING-bitcoincashII-core"
  cp -r "$ROOT/web/dist" "$stage/web"
  cp "$ROOT/packaging/linux/README.md" "$ROOT/LICENSE" "$stage/"
  chmod -R u=rwX,go=rX "$stage"
  tar -C "$WORK/stage" --owner=0 --group=0 --numeric-owner -czf "$DIST/$pkg.tar.gz" "$pkg"
  echo "built $DIST/$pkg.tar.gz"
done
(cd "$DIST" && sha256sum forge-solo-"$VERSION"-linux-*.tar.gz >SHA256SUMS-linux && cat SHA256SUMS-linux)
