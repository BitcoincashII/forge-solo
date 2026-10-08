module forge-gateway-launcher

go 1.26.0

// Built with the same Go as the rest of the app (see the top-level go.mod).
toolchain go1.26.8

require (
	fyne.io/systray v1.12.2
	golang.org/x/sys v0.48.0
)

require github.com/godbus/dbus/v5 v5.1.0 // indirect
