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
	proxy := apiProxy(apiAddr)
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
		// Listening on this machine only, with no password: a page on any site could rebind its
		// own name to 127.0.0.1 and send same-origin requests here, settings included. The browser
		// still sends that site's name as Host, so anything but this machine's own name is refused.
		return securityHeaders(onlyLocalHost(h))
	}
	return securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, pw, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte("forge")) != 1 ||
			subtle.ConstantTimeCompare([]byte(pw), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Forge Solo", charset="UTF-8"`)
			http.Error(w, "Sign in as forge, with DASHBOARD_PASSWORD from secrets.env in the Forge Solo data directory.", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
}

// securityHeaders sets on every response what the Umbrel app's nginx sends: no page may frame the
// dashboard (a framed, see-through Settings page can be clicked through by someone who never sees
// it), no content type is guessed, and no referrer is sent on.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("Content-Security-Policy", "frame-ancestors 'none'")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// apiProxy forwards to the API on this machine. The API answers only requests addressed to this
// machine's own name, so each one goes out addressed to the API, whatever name the browser used
// (with --web, the PC's name on the network, which the API refused). The API's rate limit counts
// requests by X-Real-IP, so that is set here to the address the request came from: one the client
// sent is never passed on, and neither is its X-Forwarded-For.
func apiProxy(apiAddr string) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: apiAddr}
	return &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.SetXForwarded()
		r.Out.Header.Del("X-Real-IP")
		if ip, _, err := net.SplitHostPort(r.In.RemoteAddr); err == nil {
			r.Out.Header.Set("X-Real-IP", ip)
		}
	}}
}

// onlyLocalHost answers 421 to a request whose Host is not this machine's own name.
func onlyLocalHost(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLocalHost(r.Host) {
			http.Error(w, "Forge Solo answers only at 127.0.0.1 or localhost.", http.StatusMisdirectedRequest)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// isLocalHost reports whether a Host header names this machine: localhost or a loopback address.
func isLocalHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// dashboardURL is where a browser opens the dashboard that listens on web. One that listens on
// every address (0.0.0.0, :: or no host) is there for other computers, which reach it at this
// machine's address on the network: http://0.0.0.0:3080 opens nothing.
func dashboardURL(web string) string {
	host, port, err := net.SplitHostPort(web)
	if err != nil {
		return "http://" + web
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		if host = lanAddress(); host == "" {
			host = "<this machine's address>"
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

// lanAddress is this machine's address on its network, the one its default route leaves from: ""
// if there is none. Nothing is sent. (A variable: the tests stand in a network.)
var lanAddress = func() string {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer c.Close()
	a, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || a.IP == nil || a.IP.IsLoopback() || a.IP.IsUnspecified() {
		return ""
	}
	return a.IP.String()
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
