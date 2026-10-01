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
	webRoot := ipath("web")
	apiURL, _ := url.Parse("http://127.0.0.1:" + apiPort)
	proxy := httputil.NewSingleHostReverseProxy(apiURL)
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
			http.ServeFile(w, r, filepath.Join(webRoot, "solo.html"))
		case p == "/settings":
			http.ServeFile(w, r, filepath.Join(webRoot, "settings.html"))
		case p == "/tides":
			http.ServeFile(w, r, filepath.Join(webRoot, "tides.html"))
		case p == "/index.html":
			http.Redirect(w, r, "/solo", http.StatusFound)
		default:
			fs.ServeHTTP(w, r)
		}
	})
	// The dashboard has no password, and listens on this machine only. A page on any site could
	// rebind its own name to 127.0.0.1 and send same-origin requests here, settings included; the
	// browser still sends that site's name as Host, so anything but this machine's own name is refused.
	_ = http.ListenAndServe("127.0.0.1:"+webPort, onlyLocalHost(mux))
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
