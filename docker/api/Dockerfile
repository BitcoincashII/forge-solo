# Build the API from the (hardened) source in this repo.
FROM golang:1.26.8-bookworm@sha256:a688600ca24f8a4d3ca77f95b0dd40704a9fc787c826660eb7ba0b641b8b175d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# The SQLite build: the database is the file DB_PATH names (forgesolo.db).
RUN CGO_ENABLED=0 go build -tags sqlite -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
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
