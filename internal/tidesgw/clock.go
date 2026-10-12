package tidesgw

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Forge Pool refuses a signed request whose time is more than 2 minutes off its own clock: it
// answers 401 {"error":"request time is <how far> off the pool's clock"} (internal/datum/wire,
// Verify), and the gateway's reason carries that answer. The pool was reached: only setting this
// computer's clock right helps.
var clockText = regexp.MustCompile(`request time is (-?[0-9][0-9a-zµ.]*) off the pool's clock`)

// ClockRefusal reports whether reason is that refusal, and how far off the pool found this
// computer's clock: 0 when it does not say.
func ClockRefusal(reason string) (off time.Duration, refused bool) {
	if !strings.Contains(reason, " 401 ") || !strings.Contains(reason, "off the pool's clock") {
		return 0, false
	}
	m := clockText.FindStringSubmatch(reason)
	if m == nil {
		return 0, true
	}
	d, err := time.ParseDuration(m[1])
	if err != nil {
		return 0, true
	}
	if d < 0 {
		d = -d
	}
	return d, true
}

// HowFar is d in round words: about 7 hours, about 3 minutes, about 2 days; "" when it is not known.
func HowFar(d time.Duration) string {
	about := func(n time.Duration, one, many string) string {
		if n <= 1 {
			return "about " + one
		}
		return fmt.Sprintf("about %d %s", int64(n), many)
	}
	switch {
	case d <= 0:
		return ""
	case d >= 36*time.Hour:
		return about(d.Round(24*time.Hour)/(24*time.Hour), "a day", "days")
	case d >= 50*time.Minute:
		return about(d.Round(time.Hour)/time.Hour, "an hour", "hours")
	}
	return about(d.Round(time.Minute)/time.Minute, "a minute", "minutes")
}
