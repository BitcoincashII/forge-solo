package main

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
)

// serveDashboard hosts web/dist on 127.0.0.1:webPort and reverse-proxies /api/ to api.exe,
// replicating the app's nginx routing so the bundled dashboard works unchanged.
func serveDashboard() {
	_ = http.ListenAndServe("127.0.0.1:"+webPort, dashboardHandler(ipath("web"), "127.0.0.1:"+apiPort))
}

// dashboardHandler is everything the dashboard answers.
func dashboardHandler(webRoot, apiAddr string) http.Handler {
	// The dashboard has no password, and listens on this machine only. A page on any site could
	// rebind its own name to 127.0.0.1 and send same-origin requests here, settings included; the
	// browser still sends that site's name as Host, so anything but this machine's own name is refused.
	return securityHeaders(onlyLocalHost(dashboardMux(webRoot, apiAddr)))
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

// dashboardMux serves the dashboard's pages from webRoot and proxies /api/ to the API at apiAddr.
func dashboardMux(webRoot, apiAddr string) http.Handler {
	proxy := apiProxy(apiAddr)
	fs := http.FileServer(http.Dir(webRoot))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/api/"):
			proxy.ServeHTTP(w, r)
		case p == "/":
			http.Redirect(w, r, "/solo", http.StatusFound)
		case p == "/solo":
			page(w, r, filepath.Join(webRoot, "solo.html"))
		case p == "/settings":
			page(w, r, filepath.Join(webRoot, "settings.html"))
		case p == "/tides":
			page(w, r, filepath.Join(webRoot, "tides.html"))
		case p == "/index.html":
			http.Redirect(w, r, "/solo", http.StatusFound)
		default:
			if strings.HasSuffix(p, ".html") {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fs.ServeHTTP(w, r)
		}
	})
	return mux
}

// page serves one of the dashboard's pages, to be checked with the server each time it is shown,
// as Linux and Umbrel's nginx do. Without that a browser kept the page from before an update for
// hours (Inno Setup keeps the files' times), the old Settings page among them, which has no box
// for the password the new API asks for, so no save could succeed.
func page(w http.ResponseWriter, r *http.Request, file string) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, file)
}

// apiProxy forwards to the API on this machine, addressed to the API itself. The API's rate limit
// counts requests by X-Real-IP, so that is set here to the address the request came from: one the
// client sent is never passed on, and neither is its X-Forwarded-For.
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
