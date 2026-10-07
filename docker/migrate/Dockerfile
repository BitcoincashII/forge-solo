# forge-solo-migrate: moves the PostgreSQL data of Forge Solo 1.0.12 and earlier into forgesolo.db,
# once, before the api and the stratum start (docker-compose.yml, service migrate). It is also the
# image of the stopped database service (postgres), which only runs "noop".
#
# It carries the migrator, built from this commit, and PostgreSQL 16.15: the server program and
# what it loads, nothing else. The migrator starts it on the old data, read-only and without TCP,
# as the owner of the data (postgres, uid 70, as in the database image 1.0.12 ran). TimescaleDB is
# not in it: the migrator starts the server with nothing preloaded and never reads the tables
# TimescaleDB keeps (the stored shares).
#
# Built with every release, from that release's commit (docker-build.yml), and tagged with its
# version: a migrator always knows the schema of the release it ships with.

# The migrator is compiled once, on the machine that builds the image, for each platform.
FROM --platform=$BUILDPLATFORM golang:1.26.8-bookworm@sha256:a688600ca24f8a4d3ca77f95b0dd40704a9fc787c826660eb7ba0b641b8b175d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
# The app version, such as 1.0.13: the migrator records it in what it writes.
ARG VERSION
RUN case "$VERSION" in ""|*[!0-9A-Za-z.+-]*) echo "build with --build-arg VERSION=<the app version>, such as 1.0.13; got '$VERSION'"; exit 1 ;; esac \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X main.version=$VERSION" -o /out/forge-solo-migrate ./cmd/forge-solo-migrate

# The server and what it loads, gathered under /out for the last stage: the postgres program, its
# modules (without the JIT, which needs LLVM, and the build files), its share files, the time zones
# and every library ldd lists for them. The C library is the last stage's own (the same Alpine).
FROM postgres:16.15-alpine3.23@sha256:621a761097839bdb50207afd6b87a72f38e2d718dd46c3d744828d8917c4f1e0 AS pg
RUN set -eu; \
    mkdir -p /out/usr/local/bin /out/usr/local/lib /out/usr/local/share /out/usr/share; \
    cp -a /usr/local/bin/postgres /out/usr/local/bin/; \
    cp -a /usr/local/lib/postgresql /out/usr/local/lib/; \
    rm -rf /out/usr/local/lib/postgresql/bitcode /out/usr/local/lib/postgresql/llvmjit.so \
           /out/usr/local/lib/postgresql/llvmjit_types.bc /out/usr/local/lib/postgresql/pgxs; \
    cp -a /usr/local/share/postgresql /out/usr/local/share/; \
    cp -a /usr/share/zoneinfo /out/usr/share/; \
    for f in /usr/local/bin/postgres /out/usr/local/lib/postgresql/*.so; do ldd "$f" 2>/dev/null || true; done \
      | awk '$2 == "=>" && $3 ~ /^\// { print $3 }' | grep -v '^/lib/ld-musl' | sort -u > /tmp/libs; \
    while read -r lib; do mkdir -p "/out$(dirname "$lib")"; cp -L "$lib" "/out$lib"; done < /tmp/libs; \
    test -s /tmp/libs

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
COPY --from=pg /out/ /
COPY --from=build /out/forge-solo-migrate /usr/local/bin/forge-solo-migrate
# PostgreSQL looks its user up by uid; the old data belongs to uid 70.
RUN addgroup -S -g 70 postgres \
 && adduser -S -D -H -u 70 -G postgres -h /var/lib/postgresql -s /sbin/nologin postgres \
 && [ "$(id -u postgres):$(id -g postgres)" = 70:70 ] \
 && postgres --version | grep -F ' 16.15' \
 && forge-solo-migrate plan --help
ENV LANG=en_US.utf8
ENTRYPOINT ["/usr/local/bin/forge-solo-migrate"]
