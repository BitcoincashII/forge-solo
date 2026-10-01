package main

import (
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
	_ = http.ListenAndServe("127.0.0.1:"+webPort, mux)
}
