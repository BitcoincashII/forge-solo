# Forge Gateway for Windows: the build

The Windows installer of [Forge Gateway](https://github.com/BitcoincashII/forge-gateway): the
gateway program, `forge-gateway.exe` (`cmd/forge-gateway` in this repository), and a tray app that
runs it, keeps it running, and gives its status page and Settings password a place in the
notification area of the taskbar. To install and use it, see the forge-gateway README.

## Layout
- `launcher/`: the tray app, `forge-gateway-tray.exe`. Its own Go module, as Forge Solo's
  launcher is. It ships for Windows only; it also builds on Linux, where CI runs its tests with
  stand-ins for the Windows parts.
- `forge-gateway.iss`: the Inno Setup script. It installs for the Windows account that runs it, in
  `%LOCALAPPDATA%\Programs\ForgeGateway`; the tray app keeps the config, the key, the settings
  password and the logs in `%APPDATA%\ForgeGateway`. One elevated step adds the firewall rule for
  miners on port 3333, and with the user's Yes removes a Forge Gateway Windows service (1.0.0's).
- *(not tracked)* `bin/`: `forge-gateway-tray.exe`, `forge-gateway.exe` and `LICENSE.txt`, which the
  installer takes whole.

## Releases
Releases come from the forge-gateway repository's release workflow, built from the forge-solo
commit in its `FORGE_SOLO_COMMIT`. A tag builds both programs with the version, signs
`forge-gateway.exe`, compiles the installer with that signed program in it, and signs the
installer. `forge-gateway-tray.exe` is not signed. The zip with `forge-gateway.exe` alone stays,
for a Windows service or a Command Prompt.

## Building locally
Only needed to test a change; releases come from CI. Requires Go and Docker (Docker only to run
Inno Setup, which has no native Linux build). The image is pinned by digest, the one CI uses
(`INNOSETUP_IMAGE` in `.github/workflows/release.yml`), and runs with no network.

```sh
# From the repository root.
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=<version>" -o windows/gateway/bin/forge-gateway.exe ./cmd/forge-gateway
(cd windows/gateway/launcher && rsrc -ico forge-gateway.ico -arch amd64 -o rsrc.syso)
(cd windows/gateway/launcher && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H=windowsgui -s -w -X main.version=<version>" -o ../bin/forge-gateway-tray.exe .)
cp <forge-gateway repository>/LICENSE windows/gateway/bin/LICENSE.txt
docker run --rm --network none -v "$PWD":/work amake/innosetup:innosetup6@sha256:81713b854eb12278021045dcb57701fe35312030b2dc1d37710184f294a23f81 /DMyAppVersion=<version> windows/gateway/forge-gateway.iss
```

The installer is written to `windows/gateway/ForgeGateway-Setup-<version>.exe`. The image runs as
uid 1000: if that is not you, make `windows/gateway` writable for it first
(`chmod a+w windows/gateway`), as CI does.
