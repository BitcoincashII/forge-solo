# 1175 (ESF) full node — v29.1.0, the live-network version (difficulty re-anchor at activation
# 31733 + the transition guard that purges the bad low-diff 31806 chain). Multi-arch
# (amd64 + arm64): buildx sets TARGETARCH and we fetch the matching sha-verified release tarball.
# v29 release binaries are depends-built (static libs), so ubuntu:24.04 + ca-certificates suffices.
FROM ubuntu:24.04@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

ARG ESF_VERSION=29.1.0
ARG TARGETARCH
ARG ESF_SHA256_AMD64=444c75a4cd884e2c9b86b64e2ad6d5210004400486a309517d1b94269780be67
ARG ESF_SHA256_ARM64=021c94846cc82ee815b0dea244df1448a1b0588eadea7c04ef39f509364e17cf

RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) ARCH_SUFFIX="linux-x86_64"; SHA="${ESF_SHA256_AMD64}" ;; \
        arm64) ARCH_SUFFIX="linux-aarch64"; SHA="${ESF_SHA256_ARM64}" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/1175Dev/1175/releases/download/v${ESF_VERSION}/elevenseventyfive-${ESF_VERSION}-${ARCH_SUFFIX}.tar.gz" -o /tmp/esf.tar.gz; \
    echo "${SHA}  /tmp/esf.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/esf.tar.gz -C /tmp; \
    install -m0755 /tmp/elevenseventyfive-${ESF_VERSION}/bin/elevenseventyfived    /usr/local/bin/elevenseventyfived; \
    install -m0755 /tmp/elevenseventyfive-${ESF_VERSION}/bin/elevenseventyfive-cli /usr/local/bin/elevenseventyfive-cli; \
    rm -rf /tmp/esf.tar.gz /tmp/elevenseventyfive-${ESF_VERSION}

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 25360 25361
ENTRYPOINT ["/entrypoint.sh"]
