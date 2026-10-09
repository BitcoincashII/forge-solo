# Forge Gateway

Forge Gateway's home, with its guide, release notes and downloads, is
[BitcoincashII/forge-gateway](https://github.com/BitcoincashII/forge-gateway). Its source is here,
where it shares the stratum and TIDES code with Forge Solo's TIDES mode; each release is built from
the commit named in that repository's `FORGE_SOLO_COMMIT`.

To build it as its releases do, with the version stamped:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=<version>" -o forge-gateway ./cmd/forge-gateway
```

[Build from source](../../README.md#build-from-source) in the root README has the Go to use and
the rest.

Its Windows tray app and installer are in windows/gateway.
