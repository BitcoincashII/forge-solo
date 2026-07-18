package rental

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"
)

var (
	startTime = time.Now()
	version   = "1.0.0"
)

// HealthStatus represents the detailed health check response
type HealthStatus struct {
	Status     string                     `json:"status"`
	Timestamp  time.Time                  `json:"timestamp"`
	Version    string                     `json:"version"`
	Uptime     string                     `json:"uptime"`
	Components map[string]ComponentHealth `json:"components"`
}

// ComponentHealth represents health of a single component
type ComponentHealth struct {
	Status  string `json:"status"`
	Latency string `json:"latency,omitempty"`
	Message string `json:"message,omitempty"`
}

// Metrics represents system and business metrics
type Metrics struct {
	// Runtime metrics
	Goroutines int    `json:"goroutines"`
	HeapAlloc  uint64 `json:"heap_alloc_bytes"`
	HeapSys    uint64 `json:"heap_sys_bytes"`
	NumGC      uint32 `json:"num_gc"`

	// Business metrics
	ActiveOrders       int64 `json:"active_orders"`
	PendingDeposits    int64 `json:"pending_deposits"`
	PendingWithdrawals int64 `json:"pending_withdrawals"`
	TotalCustomers     int64 `json:"total_customers"`

	// Financial metrics (in satoshis)
	TotalDeposits   int64 `json:"total_deposits_sat"`
	TotalWithdrawals int64 `json:"total_withdrawals_sat"`
	PoolProfit      int64 `json:"pool_profit_sat"`
}

// HealthHandler returns an HTTP handler for detailed health checks
func (s *Service) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		components := make(map[string]ComponentHealth)
		overallStatus := "healthy"

		// Check database
		dbStart := time.Now()
		dbHealthy := s.CheckDBHealth()
		dbLatency := time.Since(dbStart)
		if dbHealthy {
			components["database"] = ComponentHealth{
				Status:  "healthy",
				Latency: dbLatency.String(),
			}
		} else {
			components["database"] = ComponentHealth{
				Status:  "unhealthy",
				Message: "database connection failed",
			}
			overallStatus = "unhealthy"
		}

		// Check Braiins API
		braiinsStart := time.Now()
		braiinsHealthy := s.CheckBraiinsHealth()
		braiinsLatency := time.Since(braiinsStart)
		if braiinsHealthy {
			components["braiins_api"] = ComponentHealth{
				Status:  "healthy",
				Latency: braiinsLatency.String(),
			}
		} else {
			components["braiins_api"] = ComponentHealth{
				Status:  "degraded",
				Message: "braiins API not responding",
			}
			// Braiins being down degrades but doesn't kill the service
			if overallStatus == "healthy" {
				overallStatus = "degraded"
			}
		}

		health := HealthStatus{
			Status:     overallStatus,
			Timestamp:  time.Now().UTC(),
			Version:    version,
			Uptime:     time.Since(startTime).Round(time.Second).String(),
			Components: components,
		}

		w.Header().Set("Content-Type", "application/json")
		if overallStatus == "unhealthy" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(health)
	}
}

// MetricsHandler returns an HTTP handler for system and business metrics
func (s *Service) MetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		metrics := Metrics{
			Goroutines: runtime.NumGoroutine(),
			HeapAlloc:  m.HeapAlloc,
			HeapSys:    m.HeapSys,
			NumGC:      m.NumGC,
		}

		// Query business metrics (ignore errors, return 0 on failure)
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_orders WHERE status IN ('pending', 'active')`).Scan(&metrics.ActiveOrders)
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_deposits WHERE credited = FALSE`).Scan(&metrics.PendingDeposits)
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_withdrawals WHERE status = 'pending'`).Scan(&metrics.PendingWithdrawals)
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_customers`).Scan(&metrics.TotalCustomers)

		// Financial metrics
		s.db.QueryRow(`SELECT COALESCE(SUM(amount_sat), 0) FROM rental_ledger WHERE tx_type = 'deposit'`).Scan(&metrics.TotalDeposits)
		s.db.QueryRow(`SELECT COALESCE(SUM(amount_sat), 0) FROM rental_ledger WHERE tx_type = 'withdrawal'`).Scan(&metrics.TotalWithdrawals)
		s.db.QueryRow(`SELECT COALESCE(SUM(pool_margin_sat), 0) FROM rental_orders WHERE status = 'completed'`).Scan(&metrics.PoolProfit)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metrics)
	}
}
