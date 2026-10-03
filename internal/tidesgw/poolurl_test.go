package tidesgw

import "testing"

// The pool's answers carry the payout split and are not signed, so only TLS keeps them honest.
func TestPoolURLMustBeHTTPSUnlessLocal(t *testing.T) {
	for raw, ok := range map[string]bool{
		DefaultPoolURL:                 true,
		"https://pool.example:8443":    true,
		"http://127.0.0.1:13445":       true,
		"http://localhost:8080":        true,
		"http://[::1]:8080":            true,
		"http://pool.bch2.org":         false,
		"http://144.202.73.66":         false,
		"http://127.0.0.1.example.com": false,
		"pool.bch2.org":                false,
		"ftp://pool.bch2.org":          false,
		"":                             false,
	} {
		if err := CheckPoolURL(raw); (err == nil) != ok {
			t.Errorf("CheckPoolURL(%q) = %v, want accepted=%v", raw, err, ok)
		}
	}
}

// The stratum and the dashboard take Forge Pool's address from one place, DATUM_POOL_URL, read
// the same way for both: blanks and a trailing slash dropped, the public pool when it is unset.
func TestPoolURL(t *testing.T) {
	for _, tc := range []struct{ env, want, code string }{
		{"", DefaultPoolURL, "POOLURL-DEFAULT"},
		{"   ", DefaultPoolURL, "POOLURL-DEFAULT"},
		{"https://pool.example", "https://pool.example", "POOLURL-ENV"},
		{" https://pool.example/ ", "https://pool.example", "POOLURL-TRIM"},
		{"https://pool.example:8443//", "https://pool.example:8443", "POOLURL-TRIM"},
	} {
		t.Setenv("DATUM_POOL_URL", tc.env)
		if got := PoolURL(); got != tc.want {
			t.Errorf("%s: DATUM_POOL_URL=%q gives %q, want %q", tc.code, tc.env, got, tc.want)
		}
	}
}
