package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/bch2/forge-pool/internal/rental"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Processing pending withdrawals...")

	// Load config from environment
	smtpPort := 587
	emailConfig := &rental.EmailConfig{
		SMTPHost:     os.Getenv("SMTP_HOST"),
		SMTPPort:     smtpPort,
		SMTPUser:     os.Getenv("SMTP_USER"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"),
		FromAddress:  getEnv("SMTP_FROM", "noreply@hashforge.bch2.org"),
		FromName:     getEnv("SMTP_FROM_NAME", "HashForge"),
		BaseURL:      getEnv("BASE_URL", "https://hashforge.bch2.org"),
	}

	config := &rental.Config{
		BraiinsAPIKey:         os.Getenv("BRAIINS_API_KEY"),
		PoolStratumURL:        getEnv("RENTAL_POOL_URL", "stratum+tcp://forge.bch2.org:3335"),
		DefaultMarginPct:      10.0,
		MinOrderSat:           125000,
		MaxOrderSat:           10000000,
		RequiredConfirms:      3,
		XPub:                  os.Getenv("BTC_XPUB"),
		WalletSeed:            os.Getenv("WALLET_SEED"),
		BraiinsDepositAddress: os.Getenv("BRAIINS_DEPOSIT_ADDRESS"),
		Email:                 emailConfig,
		TurnstileSiteKey:      os.Getenv("TURNSTILE_SITE_KEY"),
		TurnstileSecret:       os.Getenv("TURNSTILE_SECRET"),
		SignerEndpoint:        os.Getenv("SIGNER_ENDPOINT"),
		SignerAPIKey:          os.Getenv("SIGNER_API_KEY"),
	}

	// Database connection
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "forge")
	dbPass := os.Getenv("DB_PASSWORD")
	dbName := getEnv("DB_NAME", "forgepool")
	dbSSLMode := getEnv("DB_SSLMODE", "disable")

	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		dbHost, dbPort, dbUser, dbPass, dbName, dbSSLMode,
	)

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

	// Serialize batch runs: only one withdrawal processor may execute at a time,
	// so two overlapping runs cannot each claim and broadcast the same row.
	var gotLock bool
	if err := db.QueryRow("SELECT pg_try_advisory_lock(731001)").Scan(&gotLock); err != nil {
		log.Fatalf("Failed to acquire withdrawal advisory lock: %v", err)
	}
	if !gotLock {
		log.Println("Another withdrawal batch is already running; exiting")
		return
	}
	defer db.Exec("SELECT pg_advisory_unlock(731001)")

	// Pre-flight: if the signer/wallet is unreachable, do not claim any rows. The
	// loop moves each row to 'processing' before sending, so a signer outage would
	// otherwise strand the entire queue in 'processing' with no auto-recovery.
	if !service.SignerHealthy() {
		log.Println("Signer health check failed; skipping this run (no withdrawals claimed)")
		return
	}

	// Get pending withdrawals
	rows, err := db.Query(`
		SELECT id, customer_id, amount_sat, btc_address
		FROM rental_withdrawals
		WHERE status IN ('pending', 'approved')
		ORDER BY id
	`)
	if err != nil {
		log.Fatalf("Failed to query withdrawals: %v", err)
	}
	defer rows.Close()

	type withdrawal struct {
		ID         int
		CustomerID int
		AmountSat  int64
		Address    string
	}

	var withdrawals []withdrawal
	for rows.Next() {
		var w withdrawal
		if err := rows.Scan(&w.ID, &w.CustomerID, &w.AmountSat, &w.Address); err != nil {
			log.Fatalf("Failed to scan withdrawal: %v", err)
		}
		withdrawals = append(withdrawals, w)
	}

	if len(withdrawals) == 0 {
		log.Println("No pending withdrawals found")
		return
	}

	log.Printf("Found %d pending withdrawals", len(withdrawals))

	// Process each withdrawal
	for _, w := range withdrawals {
		log.Printf("Processing withdrawal #%d: %d sats -> %s", w.ID, w.AmountSat, w.Address)

		// Atomically claim the row BEFORE broadcasting. Proceed only if we are the
		// one that moved it out of pending/approved; this makes the send idempotent,
		// so a re-run, crash-recovery run, or concurrent run cannot re-select and
		// re-broadcast a row that is already 'processing' or 'completed'.
		claim, err := db.Exec(`
			UPDATE rental_withdrawals
			SET status = 'processing', processed_by = 'system', processed_at = NOW()
			WHERE id = $1 AND status IN ('pending', 'approved')
		`, w.ID)
		if err != nil {
			log.Printf("ERROR: Failed to claim withdrawal #%d: %v", w.ID, err)
			continue
		}
		if n, _ := claim.RowsAffected(); n != 1 {
			log.Printf("SKIP: withdrawal #%d already claimed/processed (rows=%d)", w.ID, n)
			continue
		}

		// Row is now 'processing' and can never be re-selected. Broadcast once.
		txid, err := service.SendBitcoin(w.Address, w.AmountSat)
		if err != nil {
			// Ambiguous: the broadcast may or may not have reached the network. Do
			// NOT revert to 'pending' (that risks a double-send). Leave the row in
			// 'processing' for manual reconciliation against the mempool.
			log.Printf("ERROR: send failed for withdrawal #%d; left in 'processing' for manual review: %v", w.ID, err)
			continue
		}

		// Broadcast succeeded: finalize. If this UPDATE fails, the row stays
		// 'processing' (already sent) and needs manual completion, never a re-send.
		_, err = db.Exec(`
			UPDATE rental_withdrawals
			SET status = 'completed', txid = $1, processed_at = NOW()
			WHERE id = $2
		`, txid, w.ID)
		if err != nil {
			log.Printf("ERROR: withdrawal #%d BROADCAST as %s but status update failed; needs manual completion: %v", w.ID, txid, err)
			continue
		}

		log.Printf("SUCCESS: Withdrawal #%d completed. TXID: %s", w.ID, txid)

		// Notify the customer (in-app notification + preference-gated email),
		// matching the admin completion path. Synchronous: this batch job could
		// otherwise exit before a background send finishes.
		service.NotifyWithdrawalCompleted(w.CustomerID, w.AmountSat, w.Address, txid)

		// Small delay between transactions to avoid rate limits
		time.Sleep(2 * time.Second)
	}

	log.Println("Done processing withdrawals")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
