package main

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
)

//go:embed status.html
var statusPage []byte

// statusView is GET /api/status.
type statusView struct {
	Version  string   `json:"version"`
	Uptime   int64    `json:"uptime_seconds"`
	Payout   string   `json:"payout_address"`
	PoolOnly bool     `json:"pool_only"`
	Mode     string   `json:"mode"` // "tides", "solo" (fallen back), "waiting" (pool_only, pool unreachable), "starting"
	Pool     poolView `json:"pool"`
	Node     nodeView `json:"node"`
	Job      *jobView `json:"job,omitempty"`
	Stratum  struct {
		Listen      string `json:"listen"`
		Connections int64  `json:"connections"`
		Authorized  int64  `json:"authorized"`
		Accepted    int64  `json:"shares_accepted"`
		Rejected    int64  `json:"shares_rejected"`
	} `json:"stratum"`
	Workers []workerView `json:"workers"`
	Blocks  []foundBlock `json:"blocks"`
}

type poolView struct {
	tidesgw.Status
	URL string `json:"url"`
}

type nodeView struct {
	RPCURL        string  `json:"rpc_url"`
	Height        int64   `json:"template_height"`
	TemplateAge   float64 `json:"template_age_seconds"` // -1 before the first template
	NetworkDiff   float64 `json:"network_difficulty"`
	TemplateError string  `json:"template_error,omitempty"`
}

type jobView struct {
	ID         string `json:"id"`
	Height     int64  `json:"height"`
	Tides      bool   `json:"tides"`
	FinderSats int64  `json:"finder_sats"` // what a TIDES block pays the payout address itself: 0 when it has no work in the window
	Coinbase   int64  `json:"coinbase_sats"`
}

// gatewayState is what the status page needs from the running gateway.
type gatewayState struct {
	cfg     *Config
	started time.Time
	gw      *tidesgw.Gateway
	loop    *jobLoop
	srv     *stratum.Server
	proc    *processor
}

func (g *gatewayState) view(now time.Time) statusView {
	v := statusView{Version: version, Uptime: int64(now.Sub(g.started).Seconds()), Payout: g.cfg.Mining.PayoutAddress,
		PoolOnly: g.cfg.Mining.PoolOnly}
	v.Pool = poolView{Status: g.gw.Status(), URL: g.cfg.Pool.URL}
	v.Node = nodeView{RPCURL: g.cfg.Node.RPCURL, TemplateAge: -1}
	if t := g.loop.template.Load(); t != nil {
		v.Node.Height = t.Height
		v.Node.NetworkDiff = stratum.BitsToDifficulty(t.Bits)
		v.Node.TemplateAge = now.Sub(time.Unix(0, g.loop.templateAt.Load())).Seconds()
	}
	if e, _ := g.loop.lastErr.Load().(string); e != "" {
		v.Node.TemplateError = e
	}
	if j := g.loop.current.Load(); j != nil {
		v.Job = &jobView{ID: j.ID, Height: j.Height, Tides: j.Tides, FinderSats: j.TidesFinderSats, Coinbase: j.CoinbaseValue}
	}
	switch {
	case v.Job == nil && v.Pool.State == tidesgw.StateStarting:
		v.Mode = "starting"
	case g.cfg.Mining.PoolOnly && !g.loop.door.Load():
		v.Mode = "waiting"
	case v.Job != nil && v.Job.Tides:
		v.Mode = "tides"
	default:
		v.Mode = "solo"
	}
	st := g.srv.GetStats()
	v.Stratum.Listen = g.cfg.Stratum.Listen
	v.Stratum.Connections = st.ActiveConnections
	v.Stratum.Authorized = g.srv.CountAuthorized()
	v.Stratum.Accepted = st.ValidShares
	v.Stratum.Rejected = st.InvalidShares
	// Empty lists, not null, when there is nothing yet: the API's shape does not change.
	v.Workers = append([]workerView{}, g.proc.stats.view(now)...)
	v.Blocks = append([]foundBlock{}, g.proc.foundBlocks()...)
	return v
}

// statusHandler serves the page, its JSON, and /notify -- the target for the node's blocknotify,
// e.g. blocknotify=curl -s -X POST http://127.0.0.1:7152/notify
func (g *gatewayState) statusHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(statusPage)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(g.view(time.Now()))
	})
	mux.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		g.loop.wake()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// serveStatus runs the status server until stop closes.
func serveStatus(addr string, h http.Handler, stop <-chan struct{}) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	go func() {
		<-stop
		srv.Close()
	}()
	return srv, nil
}
