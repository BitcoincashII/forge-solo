# BCH2 (bitcoincashII) full node — the daemon that holds and pays out the real block
# reward, so its binary is pinned. The binaries are copied from the build context and
# then verified against a sha256 pin below: a tampered or stale build-context binary
# fails the build, and the exact artifact is recorded in git for reproducibility.
# (A checksum-verified HTTPS release fetch like docker/node1175 is preferable once a
# published BCH2 node release exists; the pin below is the equivalent guarantee until then.)
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY docker/node/bitcoincashIId    /usr/local/bin/bitcoincashIId
COPY docker/node/bitcoincashII-cli /usr/local/bin/bitcoincashII-cli
RUN echo "704d65b3fc907eaee7027e4a57e313e2608ea69f63b38272e9a626c1f846c258  /usr/local/bin/bitcoincashIId"    | sha256sum -c - \
 && echo "ba80fd4dd746e563231329b8adb9a999988ecc1f0853deffb3f47776f6ff99be  /usr/local/bin/bitcoincashII-cli" | sha256sum -c -
RUN chmod +x /usr/local/bin/bitcoincashIId /usr/local/bin/bitcoincashII-cli
COPY docker/node/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8333 28332
ENTRYPOINT ["/entrypoint.sh"]
