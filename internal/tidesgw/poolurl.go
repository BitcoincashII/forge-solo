package tidesgw

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// PoolURL is Forge Pool's base URL, as the stratum and the dashboard both use it: DATUM_POOL_URL,
// else DefaultPoolURL. It is the one setting, read here for both, so the pool the miners' shares go
// to and the pool the dashboard reads cannot differ. The stratum also read datum.pool_url from its
// config file, which the dashboard never saw; no install sets it, and every install rewrites that
// file from its template at each start.
func PoolURL() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("DATUM_POOL_URL")), "/"); u != "" {
		return u
	}
	return DefaultPoolURL
}

// CheckPoolURL accepts the pool's base URL only over https. Requests to the pool are signed, but its
// answers are not: they carry the TIDES payout split this gateway writes into every block, so over
// plain http anyone on the way could change who a block pays. Plain http is accepted only for a pool
// on this machine, which is how the tests run one.
func CheckPoolURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("pool URL %q is not a URL like https://pool.bch2.org", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		h := u.Hostname()
		if ip := net.ParseIP(h); h == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("pool URL %q must use https://: the pool's answers carry the payout split, and over plain http anyone on the way could change it (plain http is accepted only for a pool on this machine)", raw)
	}
	return fmt.Errorf("pool URL %q must start with https://", raw)
}
