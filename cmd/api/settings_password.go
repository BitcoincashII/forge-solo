package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// settingsPasswordHeader carries the app's password on a settings change. A page on another
// site cannot send it: a custom header needs a CORS preflight, and the API grants none.
const settingsPasswordHeader = "X-Forge-Password"

// settingsPasswordGateFromEnv builds the gate from SETTINGS_PASSWORD and where this API listens,
// and keeps the password in settingsPassword; the settings read tells the page whether one is set,
// and how long it is.
func settingsPasswordGateFromEnv() fiber.Handler {
	settingsPassword = strings.TrimSpace(os.Getenv("SETTINGS_PASSWORD"))
	required := settingsPasswordRequired(os.Getenv("API_LISTEN_HOST"))
	switch {
	case settingsPassword != "":
		log.Printf("🔒 settings changes need the app's password")
	case required:
		log.Printf("SETTINGS_PASSWORD is not set: settings cannot be changed until Forge Solo is restarted with its password")
	}
	return settingsPasswordGate(settingsPassword, required)
}

// settingsPasswordRequired: an API other machines can reach (Umbrel, where it listens on every
// interface) takes no settings change without the app's password.
func settingsPasswordRequired(listenHost string) bool {
	return !listenHostIsLoopback(listenHost)
}

// settingsPasswordGate guards every request that changes something.
//
// On Umbrel every app's containers share one network, so any other installed app can reach this
// API directly, without going through umbrelOS's login, and the home app has no login of its own.
// Without this gate a single request from another app could set the payout address that every
// block pays. There SETTINGS_PASSWORD is the app's own password: umbrelOS derives it for Forge
// Solo alone from the Umbrel's secret seed, hands it to this container, and shows it to the
// owner under the app's Default credentials.
//
// required is for an API other machines can reach: without a password it refuses every change
// rather than accept them unchecked. Forge Solo for Windows and Linux listens only on this
// machine, but other programs and accounts on it can still reach it, so their launchers set a
// password too.
//
// A wrong password is answered, not counted: the password is 64 hex characters, beyond guessing,
// and a lockout would let another app keep the owner out of their own settings.
func settingsPasswordGate(password string, required bool) fiber.Handler {
	want := sha256.Sum256([]byte(password))
	return func(c *fiber.Ctx) error {
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return c.Next()
		}
		if password == "" {
			if !required {
				return c.Next()
			}
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"success": false,
				"error": "Forge Solo was started without its password, so its settings cannot be changed. Restart Forge Solo from umbrelOS."})
		}
		got := strings.TrimSpace(c.Get(settingsPasswordHeader))
		if got == "" {
			// The current Settings page never posts without the password. A page from before the
			// password, still open or cached across an update, has no box for it and shows this
			// text as it is: it has to say what to do there.
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"success": false, "password_required": true,
				"error": "Enter Forge Solo's password to save. Nothing was saved. If there is no password box above the Save button, this page is from before an update: reload the page (F5) and save again."})
		}
		// Compared as hashes, so the time taken says nothing about the password's length.
		h := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(h[:], want[:]) != 1 {
			log.Printf("settings change refused: wrong password (%s %s from %s)", c.Method(), c.Path(), c.IP())
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"success": false, "password_required": true, "password_wrong": true,
				"error": "That is not Forge Solo's password. Nothing was saved."})
		}
		return c.Next()
	}
}
