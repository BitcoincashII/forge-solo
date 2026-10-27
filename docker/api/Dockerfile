# Build the API from the (hardened) source in this repo.
FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# The database is the file DB_PATH names (forgesolo.db).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/api /usr/local/bin/api
# Drop root: this process binds 8080 and writes only the database's folder, which belongs to
# 10001:10001, the user and group the compose runs it as (the stratum's too).
RUN groupadd --system --gid 10001 forge \
    && useradd --system --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin forge
USER forge
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
