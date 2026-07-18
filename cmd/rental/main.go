package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/bch2/forge-pool/internal/rental"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Forge Pool Rental Service...")

	// Load email configuration
	smtpPort, _ := strconv.Atoi(getEnv("SMTP_PORT", "587"))
	emailConfig := &rental.EmailConfig{
		SMTPHost:     getEnv("SMTP_HOST", ""),
		SMTPPort:     smtpPort,
		SMTPUser:     getEnv("SMTP_USER", ""),
		SMTPPassword: getEnv("SMTP_PASSWORD", ""),
		FromAddress:  getEnv("SMTP_FROM", "noreply@hashforge.bch2.org"),
		FromName:     getEnv("SMTP_FROM_NAME", "HashForge"),
		BaseURL:      getEnv("BASE_URL", "https://hashforge.bch2.org"),
	}

	// Load configuration from environment
	config := &rental.Config{
		BraiinsAPIKey:         getEnv("BRAIINS_API_KEY", ""),
		PoolStratumURL:        getEnv("RENTAL_POOL_URL", "stratum+tcp://forge.bch2.org:3335"),
		DefaultMarginPct:      10.0,     // 10% margin
		MinOrderSat:           111000,   // 111k sats minimum (ensures 100k goes to Braiins after 10% margin)
		MaxOrderSat:           10000000, // 10M sats maximum
		RequiredConfirms:      3,        // 3 BTC confirmations
		XPub:                  getEnv("BTC_XPUB", ""),
		WalletSeed:            getEnv("WALLET_SEED", ""),
		BraiinsDepositAddress: getEnv("BRAIINS_DEPOSIT_ADDRESS", ""),
		Email:                 emailConfig,
		TurnstileSiteKey:      getEnv("TURNSTILE_SITE_KEY", ""),
		TurnstileSecret:       getEnv("TURNSTILE_SECRET", ""),
		SignerEndpoint:        getEnv("SIGNER_ENDPOINT", ""),
		SignerAPIKey:          getEnv("SIGNER_API_KEY", ""),
	}

	if config.BraiinsAPIKey == "" {
		log.Fatal("BRAIINS_API_KEY environment variable is required")
	}

	// Database connection
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "forge")
	dbPass := os.Getenv("DB_PASSWORD")
	dbName := getEnv("DB_NAME", "forgepool")

	// Require explicit database password in production
	if dbPass == "" {
		if os.Getenv("PRODUCTION") == "true" || os.Getenv("ENV") == "production" {
			log.Fatal("FATAL: DB_PASSWORD environment variable is required in production")
		}
		// Development only - allow weak default
		log.Println("WARNING: DB_PASSWORD not set - using development default (not for production)")
		dbPass = "forgepool"
	}

	// SSL configuration - default to require for security
	dbSSLMode := getEnv("DB_SSLMODE", "require")
	dbSSLRootCert := getEnv("DB_SSLROOTCERT", "")

	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		dbHost, dbPort, dbUser, dbPass, dbName, dbSSLMode,
	)

	// Add SSL root cert if specified (required for verify-ca and verify-full)
	if dbSSLRootCert != "" {
		connStr += fmt.Sprintf(" sslrootcert=%s", dbSSLRootCert)
	}

	// Log SSL mode for visibility (don't log cert path for security)
	if dbSSLMode == "disable" {
		// Allow localhost connections without SSL (traffic never leaves server)
		isLocal := dbHost == "localhost" || dbHost == "127.0.0.1"
		if os.Getenv("PRODUCTION") == "true" || os.Getenv("ENV") == "production" {
			if !isLocal {
				log.Fatal("FATAL: Database SSL must be enabled in production for remote connections (set DB_SSLMODE=require or higher)")
			}
			log.Println("Database SSL disabled for localhost connection (acceptable in production)")
		} else {
			log.Println("WARNING: Database SSL is disabled - not recommended for production")
		}
	} else {
		log.Printf("Database SSL mode: %s", dbSSLMode)
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("Connected to database")

	// Create rental service
	service, err := rental.NewService(db, config)
	if err != nil {
		log.Fatalf("Failed to create rental service: %v", err)
	}

	// Start background workers
	service.StartDepositWatcher()
	log.Println("Started deposit watcher and order sync")

	// Create API handler
	apiHandler := rental.NewAPIHandler(service)

	// Create Web handler
	templateDir := getEnv("TEMPLATE_DIR", "/opt/forge-pool/templates")
	staticDir := getEnv("STATIC_DIR", "/opt/forge-pool/static")
	webHandler, err := rental.NewWebHandler(service, templateDir, staticDir)
	if err != nil {
		log.Fatalf("Failed to create web handler: %v", err)
	}

	// Setup HTTP server
	mux := http.NewServeMux()

	// Register web routes first (handles /)
	webHandler.RegisterRoutes(mux)

	// Register API routes
	apiHandler.RegisterRoutes(mux)

	// Health check with detailed status
	mux.HandleFunc("/health", service.HealthHandler())

	// Metrics endpoint (admin auth required)
	mux.HandleFunc("/metrics", apiHandler.AdminAuthMiddleware(service.MetricsHandler()))

	// Apply middleware chain: panic recovery -> CORS -> handlers
	corsHandler := corsMiddleware(mux)
	recoveryHandler := panicRecoveryMiddleware(corsHandler)

	port := getEnv("RENTAL_PORT", "8081")
	// Bind to loopback by default: this auth/money service is fronted by nginx
	// (TLS) over 127.0.0.1:8081. Binding all interfaces (":"+port) exposed it on
	// plaintext HTTP directly to the internet. Override RENTAL_HOST only if the
	// reverse proxy runs on a different host.
	host := getEnv("RENTAL_HOST", "127.0.0.1")
	server := &http.Server{
		Addr:              host + ":" + port,
		Handler:           recoveryHandler,
		ReadTimeout:       30 * time.Second,  // Max time to read request
		WriteTimeout:      60 * time.Second,  // Max time to write response
		IdleTimeout:       120 * time.Second, // Max time for keep-alive
		ReadHeaderTimeout: 10 * time.Second,  // Max time to read headers (prevents slowloris)
		MaxHeaderBytes:    1 << 20,           // 1MB max header size
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("Shutting down...")
		service.Stop()
		server.Close()
	}()

	log.Printf("Rental API server listening on port %s", port)
	log.Printf("Customer Endpoints:")
	log.Printf("  POST /api/v1/rental/register        - Register new customer")
	log.Printf("  GET  /api/v1/rental/prices          - Get current prices")
	log.Printf("  GET  /api/v1/rental/balance         - Get balance (auth)")
	log.Printf("  GET  /api/v1/rental/deposit-address - Get deposit address (auth)")
	log.Printf("  GET  /api/v1/rental/transactions    - Transaction history (auth)")
	log.Printf("  GET  /api/v1/rental/deposits        - Deposit history (auth)")
	log.Printf("  POST /api/v1/rental/order           - Place order (auth) [1hr min, 1 active max]")
	log.Printf("  GET  /api/v1/rental/orders          - List orders (auth)")
	log.Printf("  GET  /api/v1/rental/order/:id       - Get order (auth)")
	log.Printf("  POST /api/v1/rental/order/:id/extend - Extend order time (auth)")
	log.Printf("  DELETE /api/v1/rental/order/:id     - Cancel order (auth)")
	log.Printf("  POST /api/v1/rental/withdraw        - Request withdrawal (auth+2FA)")
	log.Printf("  GET  /api/v1/rental/withdrawals     - List withdrawals (auth)")
	log.Printf("  GET  /api/v1/rental/withdrawal/:id  - Get withdrawal (auth)")
	log.Printf("2FA Endpoints:")
	log.Printf("  POST /api/v1/rental/2fa/setup       - Start 2FA setup (auth)")
	log.Printf("  POST /api/v1/rental/2fa/verify      - Verify and enable 2FA (auth)")
	log.Printf("  GET  /api/v1/rental/2fa/status      - Get 2FA status (auth)")
	log.Printf("  POST /api/v1/rental/2fa/disable     - Disable 2FA (auth+2FA)")
	log.Printf("  POST /api/v1/rental/2fa/backup-codes - Regenerate backup codes (auth+2FA)")
	log.Printf("Session Endpoints:")
	log.Printf("  POST /api/v1/rental/login           - Login with API key + 2FA")
	log.Printf("  POST /api/v1/rental/logout          - Invalidate session")
	log.Printf("Admin Endpoints:")
	log.Printf("  GET  /api/v1/admin/stats            - System statistics")
	log.Printf("  GET  /api/v1/admin/customers        - List all customers")
	log.Printf("  GET  /api/v1/admin/orders           - List all orders")
	log.Printf("  GET  /api/v1/admin/withdrawals      - List all withdrawals")
	log.Printf("  POST /api/v1/admin/withdrawal/:id/approve  - Approve withdrawal")
	log.Printf("  POST /api/v1/admin/withdrawal/:id/complete - Complete withdrawal")
	log.Printf("  POST /api/v1/admin/withdrawal/:id/reject   - Reject withdrawal")
	log.Printf("  POST /api/v1/admin/order/:id/cancel        - Cancel any order")
	log.Printf("  GET  /api/v1/admin/settings         - Get settings")
	log.Printf("  PUT  /api/v1/admin/settings         - Update setting")
	log.Printf("  GET  /api/v1/admin/braiins-status   - Braiins balance")

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Allowed origins for CORS
var allowedOrigins = map[string]bool{
	"https://hashforge.bch2.org": true,
	"https://bch2.org":           true,
	"https://www.bch2.org":       true,
	"http://localhost:8081":      true, // Development only
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Only set CORS headers for whitelisted origins
		if allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		}
		// If origin not in whitelist, don't set any CORS headers (browser will block)

		if r.Method == "OPTIONS" {
			if allowedOrigins[origin] {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// panicRecoveryMiddleware catches panics in handlers and returns a 500 error
// instead of crashing the server. Stack traces are logged for debugging.
func panicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				// Log the panic with full stack trace
				log.Printf("PANIC RECOVERED: %v\nMethod: %s\nPath: %s\nRemoteAddr: %s\nStack:\n%s",
					err, r.Method, r.URL.Path, r.RemoteAddr, debug.Stack())

				// Return 500 error to client
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"internal server error"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
