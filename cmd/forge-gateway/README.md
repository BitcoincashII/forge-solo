# Forge Gateway

Forge Gateway's home, with its guide, release notes and downloads, is
[BitcoincashII/forge-gateway](https://github.com/BitcoincashII/forge-gateway). Its source is here,
where it shares the stratum and TIDES code with Forge Solo's TIDES mode; each release is built from
the commit named in that repository's `FORGE_SOLO_COMMIT`.

To build it (Go as in `go.mod`): `go build -trimpath -o forge-gateway ./cmd/forge-gateway`
