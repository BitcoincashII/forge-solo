# 1175 (ESF) full node — the hardened v29.0.0 release, fetched and SHA256-verified
# (never an unchecked curl|tar). Fixes the supply-chain gap from the old app.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

ARG ESF_VERSION=29.0.0
ARG ESF_URL=https://github.com/1175Dev/1175/releases/download/v${ESF_VERSION}/elevenseventyfive-${ESF_VERSION}-linux-x86_64.tar.gz
ARG ESF_SHA256=ffaba974dae6f0dc48c3dbc5a28073769a5eaffd882ffc549a5262a1163d7efe

RUN set -eux; \
    curl -fsSL "${ESF_URL}" -o /tmp/esf.tar.gz; \
    echo "${ESF_SHA256}  /tmp/esf.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/esf.tar.gz -C /tmp; \
    install -m0755 /tmp/release-linux/bin/elevenseventyfived    /usr/local/bin/elevenseventyfived; \
    install -m0755 /tmp/release-linux/bin/elevenseventyfive-cli /usr/local/bin/elevenseventyfive-cli; \
    rm -rf /tmp/esf.tar.gz /tmp/release-linux

COPY docker/node1175/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 25360
ENTRYPOINT ["/entrypoint.sh"]
