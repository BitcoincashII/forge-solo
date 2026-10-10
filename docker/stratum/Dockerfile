# Build the stratum from the (hardened) source in this repo.
FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# The database is the file DB_PATH names (forgesolo.db).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stratum ./cmd/stratum

FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates gettext-base netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/stratum /usr/local/bin/stratum
COPY docker/stratum/config.template.yaml /etc/forge/config.template.yaml
COPY docker/stratum/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
# Drop root. This service is the one published to the internet (3333 and 3335), and it needs
# no privilege: the ports are above 1024 and it writes only the rendered config and the
# database's folder, which belongs to 10001:10001, the user and group the compose runs it as.
# /etc/forge must be writable because the entrypoint renders config.yaml there at start.
RUN groupadd --system --gid 10001 forge \
    && useradd --system --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin forge \
    && chown -R forge:forge /etc/forge
USER forge
EXPOSE 3333 3335 3337
ENTRYPOINT ["/entrypoint.sh"]
