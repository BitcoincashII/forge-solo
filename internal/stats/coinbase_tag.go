package stats

// legacyDefaultCoinbaseTag was the default coinbase tag up to 1.0.11. The Settings page put the
// default in the tag box and saved whatever the box held, so every install that saved its
// settings has it stored without anyone having chosen it. Read back as no tag at all, those
// installs carry the current default (mining.DefaultCoinbaseTag) instead of the old one for good.
const legacyDefaultCoinbaseTag = "Forge"

// chosenCoinbaseTag is the tag the user chose: the stored one, or "" for the default.
func chosenCoinbaseTag(stored string) string {
	if stored == legacyDefaultCoinbaseTag {
		return ""
	}
	return stored
}
