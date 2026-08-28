# Build the stratum from the (hardened) source in this repo.
FROM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stratum ./cmd/stratum

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates gettext-base netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/stratum /usr/local/bin/stratum
COPY docker/stratum/config.template.yaml /etc/forge/config.template.yaml
COPY docker/stratum/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 3333 3337
ENTRYPOINT ["/entrypoint.sh"]
