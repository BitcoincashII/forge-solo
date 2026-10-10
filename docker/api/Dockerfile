# Build the API from the (hardened) source in this repo.
FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# The database is the file DB_PATH names (forgesolo.db).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
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
