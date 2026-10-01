package main

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
)

// dashboardHandler serves the dashboard from webRoot and proxies /api/ to the API, routed as the
// Umbrel app's nginx routes it: / goes to /solo, and the pages are sent with Cache-Control:
// no-cache so an update shows at once.
//
// With a password, every request must carry it (HTTP Basic, user "forge"). The API has no login
// of its own -- on Umbrel it sits behind Umbrel's -- and anyone who reaches it can change the
// payout address, so a dashboard that listens beyond this machine must ask.
func dashboardHandler(webRoot, apiAddr, password string) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: apiAddr})
	files := http.FileServer(http.Dir(webRoot))
	pages := map[string]string{"/solo": "solo.html", "/settings": "settings.html", "/tides": "tides.html"}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/api/"):
			r.Header.Del("Authorization") // the dashboard password stays here
			proxy.ServeHTTP(w, r)
		case p == "/" || p == "/index.html":
			http.Redirect(w, r, "/solo", http.StatusFound)
		case pages[p] != "" || strings.HasPrefix(p, "/solo/"):
			page := pages[p]
			if page == "" {
				page = "solo.html"
			}
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(webRoot, page))
		case strings.HasSuffix(p, "/"):
			http.NotFound(w, r) // no directory listings
		default:
			if strings.HasSuffix(p, ".html") {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
		}
	})
	if password == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, pw, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte("forge")) != 1 ||
			subtle.ConstantTimeCompare([]byte(pw), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Forge Solo", charset="UTF-8"`)
			http.Error(w, "Sign in as forge, with DASHBOARD_PASSWORD from secrets.env in the Forge Solo data directory.", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// webNeedsPassword is true unless the dashboard listens on loopback only.
func webNeedsPassword(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true
	}
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}
