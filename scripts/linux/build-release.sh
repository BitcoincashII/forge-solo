#!/bin/sh
# Forge Solo for Linux release tarballs:
#   dist/forge-solo-<VERSION>-linux-{x86_64,aarch64,armv7l,armv6l,i686,riscv64}.tar.gz and
#   dist/SHA256SUMS-linux
#
# The BCH2 node is built fully static (musl, Alpine 3.20, Bitcoin Core's depends) from the public
# bitcoincashII-core tag v27.0.2, so one binary runs on any Linux distribution, old or new, glibc
# or musl. The published node binaries need a recent glibc and libstdc++ (GLIBCXX_3.4.32) and do
# not start on Debian 12 or Ubuntu 22.04. The Go programs are static too (CGO_ENABLED=0; the
# database is SQLite, which is pure Go here).
#
# Needs Go and Docker. The ARM and RISC-V node builds run under QEMU (register it once with
# `docker run --privileged --rm tonistiigi/binfmt --install arm64,arm,riscv64`) and take hours.
# Node builds are kept in $WORK (default .linux-build) and reused. Every node binary that goes
# into a tarball must have the SHA-256 pinned below, whether it was just built or reused: a
# release ships exactly the node that was checked, and anything else stops the script.
#
# Usage: scripts/linux/build-release.sh VERSION [ARCH...]
#   ARCH: x86_64 aarch64 armv7l armv6l i686 riscv64 (default: all), named as `uname -m` names them
# Run it on a clean checkout of the tag vVERSION; it refuses anything else.
set -eu
VERSION=${1:?usage: scripts/linux/build-release.sh VERSION [ARCH...]}
shift
ARCHES=${*:-x86_64 aarch64 armv7l armv6l i686 riscv64}
NODE_REPO=https://github.com/BitcoincashII/bitcoincashII-core.git
NODE_TAG=v27.0.2
NODE_COMMIT=a1668c6ab9626156d9b4d3e38e92363f354e8e5a
# The node binaries the releases ship: built from NODE_COMMIT by build-node.sh, and the same bytes
# as in the published 1.0.12 downloads. A build from source gives other bytes (it is not
# reproducible): check such a build before pinning it here.
node_sha256() {
  case $1 in
    x86_64/bitcoincashIId) echo 6c9b21f0371fe2922772d842a65e416c25a1aa17908ba464b02f46d523684719 ;;
    x86_64/bitcoincashII-cli) echo e64f9845b2417408ebdd5391a85e35252ce134f4e4069e06ed91b1fff8011966 ;;
    aarch64/bitcoincashIId) echo 38376800aef050e79ca29472ed86ecc564b7d012d0e30dd6a547c42c27b6830c ;;
    aarch64/bitcoincashII-cli) echo 5da2a772c55558a0eee7487516c24aee196657efb1d7a1a8f9205f6781e0b101 ;;
    armv7l/bitcoincashIId) echo 8ec78949b29f09c44023ad0cdb20c6ae6c4e5df561cbeb5472d24072fbbcbf61 ;;
    armv7l/bitcoincashII-cli) echo 3c98b13c777f0e7eca1f75b3a776339580760e939d11cdc4d2c9288d418823d8 ;;
    armv6l/bitcoincashIId) echo 2ba97d121ee2808f789017d05e965380efbffba375838a9456782ce38715204f ;;
    armv6l/bitcoincashII-cli) echo 1bcecb7421f70cb18bfc9a6b26f2885d5ecc29cd715bba173786b70f321498e2 ;;
    i686/bitcoincashIId) echo 0925a6ba5657cb3615cb5f27543dbdbce481212f3f86dcc1023bbe8c2458cbbd ;;
    i686/bitcoincashII-cli) echo 574a49aa12a8380c403b266f9739759c4c06e73639416dd5a676c86b704ba677 ;;
    riscv64/bitcoincashIId) echo 0689803e17a2603969091eeabef9ad2a4972e0cdd951a8535f891520e5270113 ;;
    riscv64/bitcoincashII-cli) echo 39906888207a44824831121f58ced1d4452454f1b128751e713355bfac7fa29b ;;
    *) echo none ;;
  esac
}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)

# A release is built from its tag and nothing else: no uncommitted change, no other commit, and the
# version umbrel-app.yml gives. The Go programs and the dashboard come from the working tree.
manifest=$(sed -n 's/^version: *"\{0,1\}\([^"]*\)"\{0,1\}.*/\1/p' "$ROOT/umbrel-app.yml" | head -1)
if [ "$manifest" != "$VERSION" ]; then
  echo "umbrel-app.yml says version $manifest, not $VERSION: refusing to build" >&2
  exit 1
fi
if ! tag=$(git -C "$ROOT" rev-parse -q --verify "refs/tags/v$VERSION^{commit}"); then
  echo "there is no tag v$VERSION: tag the release, then build it from the tag" >&2
  exit 1
fi
if [ "$(git -C "$ROOT" rev-parse HEAD)" != "$tag" ]; then
  echo "the checkout is not v$VERSION: run git checkout v$VERSION, then build" >&2
  exit 1
fi
if [ -n "$(git -C "$ROOT" status --porcelain)" ]; then
  echo "the working tree has changes v$VERSION does not: commit or stash them, then build" >&2
  git -C "$ROOT" status --short >&2
  exit 1
fi

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
  pre='' goarm=''
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
  for prog in bitcoincashIId bitcoincashII-cli; do
    want=$(node_sha256 "$arch/$prog")
    got=$(sha256sum "$WORK/out/$arch/$prog" | cut -d' ' -f1)
    if [ "$got" != "$want" ]; then
      echo "$WORK/out/$arch/$prog has SHA-256 $got, but the release ships $want: refusing to package it" >&2
      exit 1
    fi
  done
  pkg=forge-solo-$VERSION-linux-$arch
  stage=$WORK/stage/$pkg
  rm -rf "$stage"
  mkdir -p "$stage/bin"
  for prog in stratum api; do
    (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm \
      go build -trimpath -ldflags "-s -w" -o "$stage/bin/$prog" "./cmd/$prog")
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
