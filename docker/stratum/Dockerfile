# Build the stratum from the (hardened) source in this repo.
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stratum ./cmd/stratum

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates gettext-base netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/stratum /usr/local/bin/stratum
COPY docker/stratum/config.template.yaml /etc/forge/config.template.yaml
COPY docker/stratum/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 3333 3337
ENTRYPOINT ["/entrypoint.sh"]
