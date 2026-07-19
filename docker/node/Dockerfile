# BCH2 (bitcoincashII) full node — v27.0.2, the live-network version (v27.0.0/v27.0.1 are
# DO-NOT-USE upstream). Multi-arch (amd64 + arm64): buildx sets TARGETARCH and we fetch the
# matching release tarball, sha256-verified FAIL-CLOSED for BOTH arches. Mirrors the published
# Forge Pool node image. The v27.0.2 release binary does NOT need BerkeleyDB (only v27.0.0 did).
FROM ubuntu:24.04@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90

ARG BCH2_VERSION=27.0.2
ARG TARGETARCH

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl \
      libminiupnpc17 libnatpmp1 libevent-pthreads-2.1-7 libevent-2.1-7 libzmq5 libsqlite3-0 \
    && rm -rf /var/lib/apt/lists/*

# Per-arch release tarball sha256 (from the github v27.0.2 release assets).
ARG BCH2_SHA256_AMD64=d41465503f0322b2ab93e6f74621d08365d52f41136811c8cf39aeeb4866259f
ARG BCH2_SHA256_ARM64=1875591b5d6c7cce4635006f902b7fb7656cfc3e638782ee354471c8adb19414
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) ARCH_SUFFIX="linux64";        SHA="${BCH2_SHA256_AMD64}" ;; \
        arm64) ARCH_SUFFIX="linux-aarch64";  SHA="${BCH2_SHA256_ARM64}" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}"; exit 1 ;; \
    esac; \
    [ -n "$SHA" ] || { echo "FATAL: no sha256 pinned for arch ${TARGETARCH} — refusing to build unverified"; exit 1; }; \
    curl -fsSL "https://github.com/BitcoincashII/bitcoincashII-core/releases/download/v${BCH2_VERSION}/bitcoincashII-${BCH2_VERSION}-${ARCH_SUFFIX}.tar.gz" -o /tmp/bch2.tar.gz; \
    echo "${SHA}  /tmp/bch2.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/bch2.tar.gz -C /tmp; \
    install -m0755 /tmp/bitcoincashII-${BCH2_VERSION}-${ARCH_SUFFIX}/bin/bitcoincashIId    /usr/local/bin/bitcoincashIId; \
    install -m0755 /tmp/bitcoincashII-${BCH2_VERSION}-${ARCH_SUFFIX}/bin/bitcoincashII-cli /usr/local/bin/bitcoincashII-cli; \
    rm -rf /tmp/bch2.tar.gz /tmp/bitcoincashII-*

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8333 28332
ENTRYPOINT ["/entrypoint.sh"]
