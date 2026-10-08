package main

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/netlisten"
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

// settingsNow is the config in use, and its setup problem: the engine's, or before the first apply
// the one asked for, which run gives before anything listens.
func (a *app) settingsNow() (e *engine, cfg *Config, problem string) {
	if e = a.eng.Load(); e != nil {
		return e, e.cfg, e.problem
	}
	if w := a.want.Load(); w != nil {
		return nil, w.cfg, w.problem
	}
	return nil, a.start, ""
}

func (a *app) view(now time.Time) statusView {
	_, cfg, _ := a.settingsNow()
	v := statusView{Version: version, Uptime: int64(now.Sub(a.started).Seconds()), Payout: cfg.Mining.PayoutAddress,
		PoolOnly: cfg.Mining.PoolOnly}
	v.Pool = poolView{Status: a.gw.Status(), URL: a.start.Pool.URL}
	v.Node = nodeView{RPCURL: cfg.Node.RPCURL, TemplateAge: -1}
	loop := a.currentLoop()
	if loop != nil {
		if t := loop.template.Load(); t != nil {
			v.Node.Height = t.Height
			v.Node.NetworkDiff = stratum.BitsToDifficulty(t.Bits)
			v.Node.TemplateAge = now.Sub(time.Unix(0, loop.templateAt.Load())).Seconds()
		}
		if e, _ := loop.lastErr.Load().(string); e != "" {
			v.Node.TemplateError = e
		}
		if j := loop.current.Load(); j != nil {
			v.Job = &jobView{ID: j.ID, Height: j.Height, Tides: j.Tides, FinderSats: j.TidesFinderSats, Coinbase: j.CoinbaseValue}
		}
	}
	switch {
	case v.Job == nil && v.Pool.State == tidesgw.StateStarting:
		v.Mode = "starting"
	case loop != nil && cfg.Mining.PoolOnly && !loop.door.Load():
		v.Mode = "waiting"
	case v.Job != nil && v.Job.Tides:
		v.Mode = "tides"
	default:
		v.Mode = "solo"
	}
	st := a.srv.GetStats()
	v.Stratum.Listen = a.start.Stratum.Listen
	v.Stratum.Connections = st.ActiveConnections
	v.Stratum.Authorized = a.srv.CountAuthorized()
	v.Stratum.Accepted = st.ValidShares
	v.Stratum.Rejected = st.InvalidShares
	// Empty lists, not null, when there is nothing yet: the API's shape does not change.
	v.Workers = append([]workerView{}, a.proc.stats.view(now)...)
	v.Blocks = append([]foundBlock{}, a.proc.foundBlocks()...)
	return v
}

// statusHandler serves the page, its JSON, and /notify -- the target for the node's blocknotify,
// e.g. blocknotify=curl -s -X POST http://127.0.0.1:3090/notify
func (a *app) statusHandler() http.Handler {
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
		json.NewEncoder(w).Encode(a.view(time.Now()))
	})
	mux.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if l := a.currentLoop(); l != nil {
			l.wake()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// listenStatus is the status page's listener, with exclusive address use on Windows: no other
// program can bind its port beside it and answer the page, and ask for the password, in its place.
func listenStatus(addr string) (net.Listener, error) {
	return netlisten.Listen("tcp", addr)
}
