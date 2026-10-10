module forge-solo-launcher

go 1.26.0

// Built with the same Go as the rest of the app (see the top-level go.mod).
toolchain go1.27.2

require (
	fyne.io/systray v1.12.2
	golang.org/x/sys v0.48.0
)

require github.com/godbus/dbus/v5 v5.1.0 // indirect
