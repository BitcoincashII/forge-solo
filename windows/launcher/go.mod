module forge-solo-launcher

go 1.25.0

// Built with the same Go as the rest of the app (see the top-level go.mod).
toolchain go1.26.8

require fyne.io/systray v1.12.2

require (
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	golang.org/x/sys v0.15.0 // indirect
)
