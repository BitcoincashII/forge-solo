package rental

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

// Config holds rental service configuration
type Config struct {
	BraiinsAPIKey         string
	PoolStratumURL        string  // stratum+tcp://forge.bch2.org:3335
	DefaultMarginPct      float64 // e.g., 5.0 for 5%
	MinOrderSat           int64   // Minimum order size
	MaxOrderSat           int64   // Maximum order size
	RequiredConfirms      int     // BTC confirmations required
	XPub                  string  // HD wallet xpub for address generation
	WalletSeed            string  // BIP39 mnemonic for hot wallet (auto-funding) - DEPRECATED: use SignerEndpoint
	BraiinsDepositAddress string  // Braiins BTC deposit address
	Email                 *EmailConfig
	TurnstileSiteKey      string // Cloudflare Turnstile site key
	TurnstileSecret       string // Cloudflare Turnstile secret key
	SignerEndpoint        string // Remote signing service URL (e.g., http://10.0.0.x:8443/api/v1/sign)
	SignerAPIKey          string // API key for remote signing service
}

// Service handles all rental operations
type Service struct {
	config       *Config
	db           *sql.DB
	braiins      *BraiinsClient
	btc          *BTCWatcher
	wallet       *HDWallet
	hotWallet    *Wallet       // Hot wallet for auto-funding Braiins (local signing - deprecated)
	remoteSigner *RemoteSigner // Remote signing service (preferred)
	email        *EmailSender
	wsHub        *WSHub
	mu           sync.RWMutex
	shutdown     chan struct{}

	// Address derivation index counter
	nextDerivationIndex int
}

// NewService creates a new rental service
func NewService(db *sql.DB, config *Config) (*Service, error) {
	// Initialize HD wallet if xpub provided
	var wallet *HDWallet
	if config.XPub != "" {
		var err error
		wallet, err = NewHDWallet(config.XPub)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize HD wallet: %w", err)
		}
		log.Printf("HD wallet initialized from xpub")
	} else {
		log.Printf("WARNING: No BTC_XPUB configured - using placeholder addresses")
	}

	// Initialize remote signer if configured (preferred over local hot wallet)
	var remoteSigner *RemoteSigner
	if config.SignerEndpoint != "" && config.SignerAPIKey != "" {
		// Validate endpoint uses TLS in production (unless internal network)
		if os.Getenv("PRODUCTION") == "true" || os.Getenv("ENV") == "production" {
			if !strings.HasPrefix(config.SignerEndpoint, "https://") {
				// Allow non-TLS only for internal/private IPs (VPC communication)
				isInternal := strings.Contains(config.SignerEndpoint, "10.") ||
					strings.Contains(config.SignerEndpoint, "192.168.") ||
					strings.Contains(config.SignerEndpoint, "172.16.") ||
					strings.Contains(config.SignerEndpoint, "localhost") ||
					strings.Contains(config.SignerEndpoint, "127.0.0.1")
				if !isInternal {
					log.Printf("SECURITY WARNING: Remote signer endpoint uses HTTP over public network")
					log.Printf("SECURITY WARNING: Consider using HTTPS or VPC/private networking")
				} else {
					log.Printf("Remote signer using HTTP over internal network (acceptable)")
				}
			}
		}
		remoteSigner = NewRemoteSigner(config.SignerEndpoint, config.SignerAPIKey)
		if err := remoteSigner.HealthCheck(); err != nil {
			log.Printf("WARNING: Remote signer health check failed: %v", err)
			// Don't fail startup - might become available later
		} else {
			log.Printf("Remote signer initialized: %s", config.SignerEndpoint)
		}
	}

	// Initialize hot wallet for auto-funding if seed provided (fallback if no remote signer)
	var hotWallet *Wallet
	if config.WalletSeed != "" && remoteSigner == nil {
		var err error
		hotWallet, err = NewWallet(config.WalletSeed)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize hot wallet: %w", err)
		}
		// Verify the hot wallet matches the xpub by checking external chain address
		// Hot wallet uses external chain (m/0'/0/x), deposits use change chain (m/0'/1/x)
		if wallet != nil {
			expectedAddr, _ := wallet.DeriveExternalAddress(0)
			if hotWallet.VerifyAddress(0, expectedAddr) {
				log.Printf("Hot wallet initialized and verified (auto-funding enabled)")
			} else {
				return nil, fmt.Errorf("hot wallet seed does not match xpub - addresses don't match")
			}
		}
		if config.BraiinsDepositAddress == "" {
			log.Printf("WARNING: Hot wallet configured but no BRAIINS_DEPOSIT_ADDRESS set")
		}
	} else if remoteSigner == nil && config.WalletSeed == "" {
		log.Printf("WARNING: No signing method configured (no SIGNER_ENDPOINT or WALLET_SEED)")
	}

	return &Service{
		config:       config,
		db:           db,
		braiins:      NewBraiinsClient(config.BraiinsAPIKey),
		btc:          NewBTCWatcher(config.XPub),
		wallet:       wallet,
		hotWallet:    hotWallet,
		remoteSigner: remoteSigner,
		email:        NewEmailSender(config.Email),
		shutdown:     make(chan struct{}),
	}, nil
}

// FinancialTxTimeout is the maximum duration for financial transactions
// This prevents hung transactions from blocking the database
const FinancialTxTimeout = 30 * time.Second

// beginFinancialTx starts a database transaction with SERIALIZABLE isolation level
// Use this for financial operations (orders, withdrawals, deposits) to prevent race conditions
// Returns the transaction and a cancel function that MUST be deferred by the caller
func (s *Service) beginFinancialTx() (*sql.Tx, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), FinancialTxTimeout)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelSerializable,
	})
	if err != nil {
		cancel() // Clean up on error
		return nil, nil, err
	}
	return tx, cancel, nil
}

// generateAPIKey generates a random API key
func generateAPIKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// RegisterCustomer creates a new customer account with a deposit address
// Email and password are required; email must be verified before the account can be used
func (s *Service) RegisterCustomer(email, password string) (*RegisterResponse, error) {
	// Validate and normalize email
	email = NormalizeEmail(email)
	if err := ValidateEmail(email); err != nil {
		return nil, err
	}

	// Validate password
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}

	// Hash password
	passwordHash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Check if email already exists
	var existingID int
	err = s.db.QueryRow(`SELECT id FROM rental_customers WHERE LOWER(email) = $1`, email).Scan(&existingID)
	if err == nil {
		// Anti-enumeration: do not reveal that the email is already registered.
		// Return the same generic response as a fresh signup, creating no duplicate
		// account. (The existing owner can log in or reset their password instead.)
		return &RegisterResponse{Message: "If that email address can be registered, a verification link is on its way."}, nil
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to check email: %w", err)
	}

	apiKey, err := generateAPIKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate API key: %w", err)
	}

	// Hash API key for storage (key cannot be retrieved after this)
	apiKeyHash := HashAPIKey(apiKey)

	// Generate email verification token
	verifyToken, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate verification token: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Create customer (email_verified = false by default)
	// API key is stored as hash - cannot be retrieved later
	var customerID int
	err = tx.QueryRow(`
		INSERT INTO rental_customers (api_key, email, password_hash, email_verified)
		VALUES ($1, $2, $3, FALSE)
		RETURNING id
	`, apiKeyHash, email, passwordHash).Scan(&customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create customer: %w", err)
	}

	// Store verification token hash (for security, only store hash in database)
	tokenHash := HashToken(verifyToken)
	_, err = tx.Exec(`
		INSERT INTO rental_email_tokens (customer_id, token, token_type, expires_at)
		VALUES ($1, $2, 'verify', $3)
	`, customerID, tokenHash, time.Now().Add(EmailTokenExpiry))
	if err != nil {
		return nil, fmt.Errorf("failed to store verification token: %w", err)
	}

	// Get next derivation index
	var maxIndex sql.NullInt64
	err = tx.QueryRow(`SELECT MAX(derivation_index) FROM rental_addresses`).Scan(&maxIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to get derivation index: %w", err)
	}

	// Deposit addresses use change chain (m/0'/1/x), hot wallet uses external chain (m/0'/0/x)
	// So we can safely start at index 0
	derivationIndex := 0
	if maxIndex.Valid {
		derivationIndex = int(maxIndex.Int64) + 1
	}

	// Generate BTC address from xpub
	btcAddress, err := s.deriveAddress(derivationIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to derive address: %w", err)
	}

	// Store address
	_, err = tx.Exec(`
		INSERT INTO rental_addresses (customer_id, btc_address, derivation_index)
		VALUES ($1, $2, $3)
	`, customerID, btcAddress, derivationIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to store address: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Send verification email (non-blocking, log errors)
	go func() {
		if err := s.email.SendVerificationEmail(email, verifyToken); err != nil {
			log.Printf("Failed to send verification email to %s: %v", email, err)
		}
	}()

	return &RegisterResponse{
		BTCAddress: btcAddress,
		Message:    "Please check your email to verify your account",
	}, nil
}

// EmailVerifyResult contains customer info after successful email verification
type EmailVerifyResult struct {
	BTCAddress string
}

// VerifyEmail verifies a customer's email using their verification token
func (s *Service) VerifyEmail(token string) (*EmailVerifyResult, error) {
	if token == "" {
		return nil, fmt.Errorf("verification token is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Hash the token for lookup (tokens are stored as hashes)
	tokenHash := HashToken(token)

	// Find valid token
	var customerID int
	var email string
	err = tx.QueryRow(`
		SELECT t.customer_id, c.email
		FROM rental_email_tokens t
		JOIN rental_customers c ON c.id = t.customer_id
		WHERE t.token = $1
		  AND t.token_type = 'verify'
		  AND t.used_at IS NULL
		  AND t.expires_at > NOW()
	`, tokenHash).Scan(&customerID, &email)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("invalid or expired verification link")
	} else if err != nil {
		return nil, fmt.Errorf("failed to verify token: %w", err)
	}

	// Get BTC address
	var btcAddress string
	err = tx.QueryRow(`
		SELECT btc_address FROM rental_addresses WHERE customer_id = $1 LIMIT 1
	`, customerID).Scan(&btcAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get deposit address: %w", err)
	}

	// Mark token as used (use tokenHash since tokens are stored as hashes)
	_, err = tx.Exec(`
		UPDATE rental_email_tokens
		SET used_at = NOW()
		WHERE token = $1
	`, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("failed to mark token used: %w", err)
	}

	// Mark email as verified
	_, err = tx.Exec(`
		UPDATE rental_customers
		SET email_verified = TRUE, email_verified_at = NOW()
		WHERE id = $1
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to verify email: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Send welcome email
	go func() {
		if err := s.email.SendWelcomeEmail(email); err != nil {
			log.Printf("Failed to send welcome email to %s: %v", email, err)
		}
	}()

	return &EmailVerifyResult{
		BTCAddress: btcAddress,
	}, nil
}

// ResendVerificationEmail resends the verification email
func (s *Service) ResendVerificationEmail(email string) error {
	email = NormalizeEmail(email)
	if err := ValidateEmail(email); err != nil {
		return err
	}

	// Find customer by email
	var customerID int
	var emailVerified bool
	err := s.db.QueryRow(`
		SELECT id, email_verified
		FROM rental_customers
		WHERE LOWER(email) = $1
	`, email).Scan(&customerID, &emailVerified)
	if err == sql.ErrNoRows {
		// Don't reveal if email exists
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to lookup customer: %w", err)
	}

	if emailVerified {
		return fmt.Errorf("email is already verified")
	}

	// Invalidate old tokens
	_, err = s.db.Exec(`
		UPDATE rental_email_tokens
		SET used_at = NOW()
		WHERE customer_id = $1 AND token_type = 'verify' AND used_at IS NULL
	`, customerID)
	if err != nil {
		return fmt.Errorf("failed to invalidate old tokens: %w", err)
	}

	// Generate new token
	token, err := GenerateToken()
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}

	// Store token hash (for security, only store hash in database)
	tokenHash := HashToken(token)
	_, err = s.db.Exec(`
		INSERT INTO rental_email_tokens (customer_id, token, token_type, expires_at)
		VALUES ($1, $2, 'verify', $3)
	`, customerID, tokenHash, time.Now().Add(EmailTokenExpiry))
	if err != nil {
		return fmt.Errorf("failed to store token: %w", err)
	}

	// Send email (with original unhashed token)
	go func() {
		if err := s.email.SendVerificationEmail(email, token); err != nil {
			log.Printf("Failed to send verification email to %s: %v", email, err)
		}
	}()

	return nil
}

// IsEmailVerified checks if a customer's email is verified
func (s *Service) IsEmailVerified(customerID int) (bool, error) {
	var verified bool
	err := s.db.QueryRow(`
		SELECT COALESCE(email_verified, FALSE)
		FROM rental_customers
		WHERE id = $1
	`, customerID).Scan(&verified)
	if err != nil {
		return false, err
	}
	return verified, nil
}

// Password reset token expiry
const PasswordResetExpiry = 1 * time.Hour

// RequestPasswordReset generates a reset token and sends email
func (s *Service) RequestPasswordReset(email string) error {
	email = NormalizeEmail(email)
	if err := ValidateEmail(email); err != nil {
		return nil // Don't reveal if email exists
	}

	// Find customer
	var customerID int
	err := s.db.QueryRow(`
		SELECT id FROM rental_customers WHERE LOWER(email) = $1
	`, email).Scan(&customerID)
	if err == sql.ErrNoRows {
		return nil // Don't reveal if email exists
	} else if err != nil {
		return fmt.Errorf("database error: %w", err)
	}

	// Invalidate old reset tokens
	_, err = s.db.Exec(`
		UPDATE rental_email_tokens
		SET used_at = NOW()
		WHERE customer_id = $1 AND token_type = 'reset' AND used_at IS NULL
	`, customerID)
	if err != nil {
		return fmt.Errorf("failed to invalidate old tokens: %w", err)
	}

	// Generate new token
	token, err := GenerateToken()
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}

	// Store token hash (for security, only store hash in database)
	tokenHash := HashToken(token)
	_, err = s.db.Exec(`
		INSERT INTO rental_email_tokens (customer_id, token, token_type, expires_at)
		VALUES ($1, $2, 'reset', $3)
	`, customerID, tokenHash, time.Now().Add(PasswordResetExpiry))
	if err != nil {
		return fmt.Errorf("failed to store token: %w", err)
	}

	// Log the password reset request
	s.LogSecurityEvent(customerID, "password_reset_request", "", "", map[string]interface{}{
		"email": email,
	})

	// Send email
	go func() {
		if err := s.email.SendPasswordResetEmail(email, token); err != nil {
			log.Printf("Failed to send password reset email to %s: %v", email, err)
		}
	}()

	return nil
}

// ResetPassword validates token and sets new password
func (s *Service) ResetPassword(token, newPassword string) error {
	if token == "" {
		return fmt.Errorf("reset token is required")
	}

	// Validate new password
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}

	// Hash new password
	passwordHash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Hash the token for lookup (tokens are stored as hashes)
	tokenHash := HashToken(token)

	// Find valid token
	var customerID int
	err = tx.QueryRow(`
		SELECT customer_id
		FROM rental_email_tokens
		WHERE token = $1
		  AND token_type = 'reset'
		  AND used_at IS NULL
		  AND expires_at > NOW()
	`, tokenHash).Scan(&customerID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("invalid or expired reset link")
	} else if err != nil {
		return fmt.Errorf("failed to verify token: %w", err)
	}

	// Mark token as used (use tokenHash since tokens are stored as hashes)
	_, err = tx.Exec(`
		UPDATE rental_email_tokens
		SET used_at = NOW()
		WHERE token = $1
	`, tokenHash)
	if err != nil {
		return fmt.Errorf("failed to mark token used: %w", err)
	}

	// Update password
	_, err = tx.Exec(`
		UPDATE rental_customers
		SET password_hash = $1
		WHERE id = $2
	`, passwordHash, customerID)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	// SECURITY: Invalidate ALL sessions after password reset
	// This ensures any attacker who compromised the old password loses access
	result, err := tx.Exec(`DELETE FROM rental_sessions WHERE customer_id = $1`, customerID)
	if err != nil {
		return fmt.Errorf("failed to invalidate sessions: %w", err)
	}
	sessionsInvalidated, _ := result.RowsAffected()

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	// Log the successful password reset with session invalidation count
	s.LogSecurityEvent(customerID, "password_reset_complete", "", "", map[string]interface{}{
		"sessions_invalidated": sessionsInvalidated,
	})

	return nil
}

// GetCustomerByEmail looks up a customer by their email address
func (s *Service) GetCustomerByEmail(email string) (*Customer, error) {
	email = NormalizeEmail(email)
	var c Customer
	err := s.db.QueryRow(`
		SELECT id, api_key, email, COALESCE(password_hash, ''), COALESCE(email_verified, FALSE), created_at
		FROM rental_customers
		WHERE LOWER(email) = $1
	`, email).Scan(&c.ID, &c.APIKey, &c.Email, &c.PasswordHash, &c.EmailVerified, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("invalid credentials")
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	return &c, nil
}

// deriveAddress derives a BTC address from xpub at given index
func (s *Service) deriveAddress(index int) (string, error) {
	if s.wallet == nil {
		// Check if we're in production mode - fail if wallet not configured
		if os.Getenv("PRODUCTION") == "true" || os.Getenv("ENV") == "production" {
			return "", fmt.Errorf("BTC_XPUB must be configured in production")
		}
		// Fallback to placeholder only in development
		log.Printf("WARNING: Using placeholder address - configure BTC_XPUB for real addresses")
		return fmt.Sprintf("bc1q_placeholder_%d", index), nil
	}

	// Derive native SegWit (bc1q...) address
	address, err := s.wallet.DeriveAddress(uint32(index))
	if err != nil {
		return "", fmt.Errorf("failed to derive address: %w", err)
	}

	return address, nil
}

// GetCustomerByAPIKey looks up a customer by their API key
// The API key is hashed before lookup for security
func (s *Service) GetCustomerByAPIKey(apiKey string) (*Customer, error) {
	// Hash the provided API key for comparison
	apiKeyHash := HashAPIKey(apiKey)

	var c Customer
	err := s.db.QueryRow(`
		SELECT id, api_key, email, COALESCE(email_verified, FALSE), created_at
		FROM rental_customers
		WHERE api_key = $1
	`, apiKeyHash).Scan(&c.ID, &c.APIKey, &c.Email, &c.EmailVerified, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("invalid API key")
	}
	if err != nil {
		log.Printf("GetCustomerByAPIKey: database error: %v", err)
		return nil, fmt.Errorf("database error: %w", err)
	}
	log.Printf("GetCustomerByAPIKey: found customer id=%d email=%s", c.ID, c.Email)
	return &c, nil
}

// GetCustomerByID looks up a customer by their ID
func (s *Service) GetCustomerByID(customerID int) (*Customer, error) {
	var c Customer
	var bch2Addr sql.NullString
	err := s.db.QueryRow(`
		SELECT id, api_key, email, COALESCE(email_verified, FALSE), COALESCE(bch2_address, ''), created_at
		FROM rental_customers
		WHERE id = $1
	`, customerID).Scan(&c.ID, &c.APIKey, &c.Email, &c.EmailVerified, &bch2Addr, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("customer not found")
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	c.BCH2Address = bch2Addr.String
	return &c, nil
}

// ValidateBCH2Address validates a BCH2 address format
func ValidateBCH2Address(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("BCH2 address is required")
	}
	// BCH2 uses bitcoincash-style addresses (qaddr or paddr format)
	// Accept addresses starting with bitcoincashii:q or just q (legacy format)
	addr = strings.ToLower(addr)
	if strings.HasPrefix(addr, "bitcoincashii:") {
		addr = strings.TrimPrefix(addr, "bitcoincashii:")
	}
	if !strings.HasPrefix(addr, "q") && !strings.HasPrefix(addr, "p") {
		return fmt.Errorf("invalid BCH2 address format - must start with 'q' or 'bitcoincashii:q'")
	}
	if len(addr) < 30 || len(addr) > 60 {
		return fmt.Errorf("invalid BCH2 address length")
	}
	return nil
}

// UpdateBCH2Address updates the customer's BCH2 payout address
func (s *Service) UpdateBCH2Address(customerID int, addr string) error {
	addr = strings.TrimSpace(addr)
	if err := ValidateBCH2Address(addr); err != nil {
		return err
	}

	// Normalize address - always store WITH bitcoincashii: prefix (required for Braiins)
	addr = strings.ToLower(addr)
	if !strings.HasPrefix(addr, "bitcoincashii:") {
		addr = "bitcoincashii:" + addr
	}

	_, err := s.db.Exec(`
		UPDATE rental_customers
		SET bch2_address = $1, updated_at = NOW()
		WHERE id = $2
	`, addr, customerID)
	if err != nil {
		return fmt.Errorf("failed to update BCH2 address: %w", err)
	}
	return nil
}

// GetBalance returns the customer's current balance
func (s *Service) GetBalance(customerID int) (*BalanceResponse, error) {
	var balanceSat int64
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(amount_sat), 0)
		FROM rental_ledger
		WHERE customer_id = $1
	`, customerID).Scan(&balanceSat)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	// Get pending deposits
	var pendingSat int64
	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(amount_sat), 0)
		FROM rental_deposits
		WHERE customer_id = $1 AND credited = FALSE
	`, customerID).Scan(&pendingSat)
	if err != nil {
		pendingSat = 0
	}

	return &BalanceResponse{
		BalanceSat:     balanceSat,
		BalanceBTC:     fmt.Sprintf("%.8f", float64(balanceSat)/100000000),
		PendingDeposit: pendingSat,
	}, nil
}

// GetDepositAddress returns the customer's BTC deposit address
func (s *Service) GetDepositAddress(customerID int) (string, error) {
	var address string
	err := s.db.QueryRow(`
		SELECT btc_address
		FROM rental_addresses
		WHERE customer_id = $1
		ORDER BY id DESC
		LIMIT 1
	`, customerID).Scan(&address)
	if err != nil {
		return "", fmt.Errorf("no deposit address found: %w", err)
	}
	return address, nil
}

// GetPrices returns current hashrate prices
func (s *Service) GetPrices() (*PriceResponse, error) {
	orderbook, err := s.braiins.GetOrderbook()
	if err != nil {
		return nil, fmt.Errorf("failed to get orderbook: %w", err)
	}

	// Calculate pool price with margin using integer arithmetic
	// Pool charges customers MORE than market (pool price > market price)
	// Formula: poolPrice = marketPrice * (10000 + marginBasisPoints) / 10000
	marginBasisPoints := int64(s.config.DefaultMarginPct * 100)
	poolPrice := (orderbook.BestAskSat * (10000 + marginBasisPoints)) / 10000
	// Align to tick size (round up to nearest 1000)
	poolPrice = ((poolPrice + 999) / 1000) * 1000

	return &PriceResponse{
		BestAskSat:   orderbook.BestAskSat, // Market price (what we pay Braiins)
		PoolPriceSat: poolPrice,            // Customer price (what customer pays us)
		MarginPct:    s.config.DefaultMarginPct,
		AvailablePH:  orderbook.AvailablePH,
		PriceUnit:    "sat/EH/day",
	}, nil
}

// PlaceOrder creates a new rental order
func (s *Service) PlaceOrder(customerID int, req *PlaceOrderRequest) (*PlaceOrderResponse, error) {
	// Check if customer already has an active order (only 1 active order at a time)
	var activeOrderID int
	err := s.db.QueryRow(`
		SELECT id FROM rental_orders
		WHERE customer_id = $1 AND status IN ('pending', 'active')
		LIMIT 1
	`, customerID).Scan(&activeOrderID)
	if err == nil {
		return nil, fmt.Errorf("you already have an active order (#%d) - use /order/%d/extend to add time, or cancel it first", activeOrderID, activeOrderID)
	}

	// Validate request
	if req.BudgetSat < s.config.MinOrderSat {
		return nil, fmt.Errorf("minimum order is %d sats", s.config.MinOrderSat)
	}
	if req.BudgetSat > s.config.MaxOrderSat {
		return nil, fmt.Errorf("maximum order is %d sats", s.config.MaxOrderSat)
	}
	if math.IsNaN(req.SpeedLimitPH) || math.IsInf(req.SpeedLimitPH, 0) {
		return nil, fmt.Errorf("invalid hashrate")
	}
	if req.SpeedLimitPH == 0 {
		req.SpeedLimitPH = 1.0 // Default to 1 PH/s
	} else if req.SpeedLimitPH < 1.0 {
		return nil, fmt.Errorf("minimum hashrate is 1 PH/s")
	} else if req.SpeedLimitPH > 500.0 {
		return nil, fmt.Errorf("maximum hashrate is 500 PH/s")
	}

	// Validate worker name (basic sanitization)
	if req.WorkerName != "" {
		if len(req.WorkerName) > 50 {
			return nil, fmt.Errorf("worker_name too long")
		}
		for _, c := range req.WorkerName {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
				return nil, fmt.Errorf("worker_name contains invalid characters")
			}
		}
	}

	// Validate mining mode
	if req.MiningMode == "" {
		req.MiningMode = "pplns"
	}
	if req.MiningMode != "pplns" && req.MiningMode != "solo" {
		return nil, fmt.Errorf("invalid mining mode: must be 'pplns' or 'solo'")
	}

	// Get current prices
	prices, err := s.GetPrices()
	if err != nil {
		return nil, fmt.Errorf("failed to get prices: %w", err)
	}

	// Minimum 1 hour of mining required
	// Price is in sat/EH/day, speed is in PH
	// Cost per hour = (price_sat * speed_PH / 1000) / 24
	minBudgetFor1Hour := int64(float64(prices.PoolPriceSat) * req.SpeedLimitPH / 1000.0 / 24.0)
	if minBudgetFor1Hour < 10000 {
		minBudgetFor1Hour = 10000 // Absolute minimum 10k sats
	}
	if req.BudgetSat < minBudgetFor1Hour {
		return nil, fmt.Errorf("minimum 1 hour of mining required: need at least %d sats for %.2f PH at current prices", minBudgetFor1Hour, req.SpeedLimitPH)
	}

	// Calculate margin properly:
	// Customer pays: budget_sat at pool_price (higher)
	// We pay Braiins: braiins_budget at market_price (lower)
	// Margin = budget_sat - braiins_budget
	//
	// Example: Customer pays 1M at 45M/EH pool price
	// Market price is 43M/EH
	// Customer gets: 1M / 45M = 0.0222 EH worth
	// We pay Braiins: 0.0222 * 43M = 955k sats
	// Margin: 1M - 955k = 45k sats
	// Use integer arithmetic to avoid floating point precision loss
	// Formula: braiinsBudget = customerBudget * 10000 / (10000 + marginPct * 100)
	marginBasisPoints := int64(s.config.DefaultMarginPct * 100) // 25% = 2500 basis points

	// SECURITY: Check for integer overflow before multiplication
	// MaxInt64 / 10000 = 922337203685477 sats (~9223 BTC)
	const maxSafeBudget int64 = 922337203685477
	if req.BudgetSat > maxSafeBudget {
		return nil, fmt.Errorf("order budget exceeds maximum safe value")
	}

	braiinsBudget := (req.BudgetSat * 10000) / (10000 + marginBasisPoints)
	poolMargin := req.BudgetSat - braiinsBudget

	// Get customer's BCH2 address - required for mining payouts
	customer, err := s.GetCustomerByID(customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get customer: %w", err)
	}
	if customer.BCH2Address == "" {
		return nil, fmt.Errorf("BCH2 payout address required - please set your BCH2 address in Settings before placing an order")
	}

	// Build target identity using customer's BCH2 address (already has bitcoincashii: prefix)
	workerName := req.WorkerName
	if workerName == "" {
		workerName = "rental"
	}
	// Format: bch2address.workername (the stratum will parse this)
	targetIdentity := fmt.Sprintf("%s.%s", customer.BCH2Address, workerName)

	// Start SERIALIZABLE transaction for financial operation
	tx, cancel, err := s.beginFinancialTx()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer cancel()
	defer tx.Rollback()

	// Lock the customer row first to serialize operations
	var customerExists bool
	err = tx.QueryRow(`
		SELECT TRUE FROM rental_customers WHERE id = $1 FOR UPDATE
	`, customerID).Scan(&customerExists)
	if err != nil {
		return nil, fmt.Errorf("failed to lock customer: %w", err)
	}

	// Now safely calculate balance (no concurrent modifications possible)
	var balanceSat int64
	err = tx.QueryRow(`
		SELECT COALESCE(SUM(amount_sat), 0)
		FROM rental_ledger
		WHERE customer_id = $1
	`, customerID).Scan(&balanceSat)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	if balanceSat < req.BudgetSat {
		return nil, fmt.Errorf("insufficient balance: have %d sats, need %d sats", balanceSat, req.BudgetSat)
	}

	// Create order record with proper margin tracking
	var orderID int
	err = tx.QueryRow(`
		INSERT INTO rental_orders (
			customer_id, target_pool_url, target_identity,
			budget_sat, price_sat, market_price_sat, braiins_budget_sat, pool_margin_sat,
			speed_limit_ph, margin_pct, mining_mode, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending')
		RETURNING id
	`, customerID, s.config.PoolStratumURL, targetIdentity,
		req.BudgetSat, prices.PoolPriceSat, prices.BestAskSat, braiinsBudget, poolMargin,
		req.SpeedLimitPH, s.config.DefaultMarginPct, req.MiningMode,
	).Scan(&orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to create order: %w", err)
	}

	// Deduct from balance (reserve funds)
	_, err = tx.Exec(`
		INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, order_id, memo)
		VALUES ($1, 'order_charge', $2, $3, 'Reserved for order')
	`, customerID, -req.BudgetSat, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to reserve funds: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Trigger async order execution
	go s.executeOrder(orderID)

	log.Printf("Order %d created: budget=%d, braiins_budget=%d, margin=%d",
		orderID, req.BudgetSat, braiinsBudget, poolMargin)

	return &PlaceOrderResponse{
		OrderID:    orderID,
		Status:     OrderStatusPending,
		BudgetSat:  req.BudgetSat,
		PriceSat:   prices.PoolPriceSat,
		MiningMode: req.MiningMode,
		Message:    "Order placed, execution starting",
	}, nil
}

// executeOrder places the order on Braiins
func (s *Service) executeOrder(orderID int) {
	// Fetch order details including margin fields
	var order Order
	var braiinsBudget, marketPrice sql.NullInt64
	err := s.db.QueryRow(`
		SELECT id, customer_id, target_pool_url, target_identity,
		       budget_sat, price_sat, COALESCE(market_price_sat, price_sat),
		       COALESCE(braiins_budget_sat, budget_sat), speed_limit_ph, mining_mode
		FROM rental_orders
		WHERE id = $1
	`, orderID).Scan(
		&order.ID, &order.CustomerID, &order.TargetPoolURL, &order.TargetIdentity,
		&order.BudgetSat, &order.PriceSat, &marketPrice,
		&braiinsBudget, &order.SpeedLimitPH, &order.MiningMode,
	)
	if err != nil {
		log.Printf("Failed to fetch order %d: %v", orderID, err)
		s.updateOrderStatus(orderID, OrderStatusFailed, "Failed to fetch order details")
		return
	}

	// Use braiins_budget_sat (margin already deducted) for Braiins
	// Use market_price_sat as the price limit, with a small premium to ensure fill
	bidBudget := order.BudgetSat
	bidPrice := order.PriceSat
	if braiinsBudget.Valid && braiinsBudget.Int64 > 0 {
		bidBudget = braiinsBudget.Int64
	}
	if marketPrice.Valid && marketPrice.Int64 > 0 {
		bidPrice = marketPrice.Int64
	}

	// Add 3.5% premium to bid price to ensure order fills
	// Higher buffer needed for large orders (10+ PH/s)
	bidPrice = (bidPrice * 1035) / 1000

	// Align bid price to tick size (1000 sats) - round UP to ensure competitive pricing
	const tickSize int64 = 1000
	originalPrice := bidPrice
	if bidPrice%tickSize != 0 {
		bidPrice = ((bidPrice / tickSize) + 1) * tickSize
	}
	log.Printf("Order %d bid price: original=%d, after_premium=%d, aligned=%d (tick=%d)",
		orderID, order.PriceSat, originalPrice, bidPrice, tickSize)

	// Place bid on Braiins with REDUCED budget (margin kept by pool)
	memo := fmt.Sprintf("Forge Pool rental order #%d", orderID)
	clOrderID := fmt.Sprintf("FP-%d", orderID)
	bidID, err := s.braiins.PlaceBidWithClientID(
		order.TargetPoolURL,
		order.TargetIdentity,
		bidBudget, // Use braiins_budget (customer budget minus margin)
		bidPrice,  // Use market price (not inflated pool price)
		order.SpeedLimitPH,
		memo,
		clOrderID,
	)
	if err != nil {
		log.Printf("Failed to place Braiins bid for order %d: %v", orderID, err)
		s.updateOrderStatus(orderID, OrderStatusFailed, err.Error())
		s.refundOrder(orderID, order.CustomerID, order.BudgetSat)
		return
	}

	// Update order with Braiins bid ID and client order ID.
	// Guard on status='pending': if the customer cancelled between placing the bid
	// above and this update, CancelOrder already refunded them and could not see
	// this bid id (not yet persisted). Activating anyway would leave a live,
	// pool-funded bid on a refunded order, so if we lost that race, cancel the bid.
	now := time.Now()
	res, err := s.db.Exec(`
		UPDATE rental_orders
		SET braiins_bid_id = $1, cl_order_id = $2, status = 'active', started_at = $3
		WHERE id = $4 AND status = 'pending'
	`, bidID, clOrderID, now, orderID)
	if err != nil {
		log.Printf("Failed to update order %d with bid ID: %v", orderID, err)
	} else if n, _ := res.RowsAffected(); n == 0 {
		log.Printf("Order %d no longer pending after bid %s placed (cancelled mid-flight); cancelling bid to avoid a live bid on a refunded order", orderID, bidID)
		s.braiins.CancelBid(bidID)
		return
	}

	log.Printf("Order %d started: Braiins bid %s (budget=%d, braiins_budget=%d)",
		orderID, bidID, order.BudgetSat, bidBudget)

	// Set mining mode on the pool based on order type
	if order.MiningMode == "solo" {
		if err := s.setMinerSoloMode(order.TargetIdentity, true); err != nil {
			log.Printf("Warning: failed to enable solo mode for order %d: %v", orderID, err)
		}
	} else {
		// Disable solo mode for PPLNS orders (in case miner was previously solo)
		if err := s.setMinerSoloMode(order.TargetIdentity, false); err != nil {
			log.Printf("Warning: failed to disable solo mode for order %d: %v", orderID, err)
		}
	}

	// Note: Auto-funding Braiins happens when order COMPLETES, not when it starts
	// This way we only pay for actual spent amount, not the full budget

	// Send order started email
	go s.sendOrderStartedEmail(orderID, order.CustomerID, order.SpeedLimitPH, order.BudgetSat, order.MiningMode)
}

// sendOrderStartedEmail sends order started notification (fire-and-forget)
func (s *Service) sendOrderStartedEmail(orderID, customerID int, hashratePH float64, budgetSat int64, miningMode string) {
	budgetBTC := fmt.Sprintf("%.8f", float64(budgetSat)/100000000)

	// Create in-app notification
	s.CreateNotification(customerID, "order_started", "Order Started",
		fmt.Sprintf("Order #%d (%.0f PH/s) is now mining.", orderID, hashratePH),
		fmt.Sprintf("/order/%d", orderID))

	// Send email if preferences allow
	if s.email == nil || !s.ShouldSendEmail(customerID, "order") {
		return
	}
	customer, err := s.GetCustomerByID(customerID)
	if err != nil || customer.Email == "" {
		return
	}
	if err := s.email.SendOrderStartedEmail(customer.Email, orderID, hashratePH, budgetBTC, miningMode); err != nil {
		log.Printf("Failed to send order started email to %s: %v", customer.Email, err)
	}
}

// autoFundBraiins sends BTC to Braiins to cover an order's budget
func (s *Service) autoFundBraiins(orderID int, amountSat int64) {
	// Check if any signing method is available
	if s.remoteSigner == nil && s.hotWallet == nil {
		log.Printf("Auto-fund: No signing method available for order %d", orderID)
		return
	}
	if s.config.BraiinsDepositAddress == "" {
		log.Printf("Auto-fund: No Braiins deposit address configured")
		return
	}

	// Get the current highest derivation index from database
	var maxIndex int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(derivation_index), 0) FROM rental_addresses`).Scan(&maxIndex)
	if err != nil {
		log.Printf("Auto-fund: Failed to get max derivation index: %v", err)
		maxIndex = 10 // Fallback
	}

	// Add buffer for any addresses not yet in DB
	maxIndex += 5

	log.Printf("Auto-fund: Sending %d sats to Braiins for order %d", amountSat, orderID)

	var txid string

	// Prefer remote signer if available
	if s.remoteSigner != nil {
		txid, err = s.sendViaRemoteSigner(s.config.BraiinsDepositAddress, amountSat, maxIndex)
	} else {
		txid, err = s.hotWallet.SendToAddress(s.config.BraiinsDepositAddress, amountSat, maxIndex)
	}

	if err != nil {
		log.Printf("Auto-fund FAILED for order %d: %v", orderID, err)
		// Log to audit table for admin visibility
		s.db.Exec(`
			INSERT INTO rental_audit_log (event_type, details, created_at)
			VALUES ('auto_fund_failed', $1, NOW())
		`, fmt.Sprintf("Order %d: failed to send %d sats to Braiins: %v", orderID, amountSat, err))
		return
	}

	log.Printf("Auto-fund SUCCESS for order %d: txid %s (%d sats)", orderID, txid, amountSat)

	// Log successful auto-fund
	s.db.Exec(`
		INSERT INTO rental_audit_log (event_type, details, created_at)
		VALUES ('auto_fund_success', $1, NOW())
	`, fmt.Sprintf("Order %d: sent %d sats to Braiins, txid: %s", orderID, amountSat, txid))
}

// sendViaRemoteSigner sends BTC using the remote signing service
func (s *Service) sendViaRemoteSigner(destAddress string, amountSat int64, maxIndex int) (string, error) {
	if s.wallet == nil {
		return "", fmt.Errorf("HD wallet (xpub) required for UTXO fetching")
	}

	// Get all UTXOs using the HD wallet (watch-only, no seed needed)
	utxos, addressToChainIndex, err := s.wallet.GetAllUTXOs(maxIndex)
	if err != nil {
		return "", fmt.Errorf("failed to get UTXOs: %w", err)
	}

	// Filter confirmed UTXOs
	var confirmedUTXOs []UTXO
	for _, utxo := range utxos {
		if utxo.Status.Confirmed {
			confirmedUTXOs = append(confirmedUTXOs, utxo)
		}
	}

	if len(confirmedUTXOs) == 0 {
		return "", fmt.Errorf("no confirmed UTXOs available")
	}

	// Get next change index for external chain (chain 0)
	// IMPORTANT: Use chain 0 for change to avoid collision with customer deposit
	// addresses which use chain 1. Customer addresses are on chain 1 (xpub/1/index),
	// so change must go to chain 0 (xpub/0/index) to prevent the deposit watcher
	// from incorrectly crediting change as new customer deposits.
	var changeIndex int
	err = s.db.QueryRow(`SELECT COALESCE(MAX(derivation_index), -1) + 1 FROM wallet_change_addresses WHERE chain_index = 0`).Scan(&changeIndex)
	if err != nil {
		// Fallback: use maxIndex but on different chain, so no collision
		changeIndex = maxIndex + 1
	}

	// Derive the change address to record it
	changeAddr, err := s.wallet.DeriveExternalAddress(uint32(changeIndex))
	if err != nil {
		log.Printf("Warning: failed to derive change address for tracking: %v", err)
	}

	// Use remote signer with chain 0 for change (separate from customer addresses on chain 1)
	txid, err := s.remoteSigner.SignAndBroadcast(destAddress, amountSat, confirmedUTXOs, addressToChainIndex, 0, uint32(changeIndex))
	if err != nil {
		return "", err
	}

	// Record the change address used (for deposit filtering)
	if changeAddr != "" {
		_, dbErr := s.db.Exec(`
			INSERT INTO wallet_change_addresses (btc_address, chain_index, derivation_index, btc_txid)
			VALUES ($1, 0, $2, $3)
			ON CONFLICT (btc_address) DO UPDATE SET btc_txid = $3
		`, changeAddr, changeIndex, txid)
		if dbErr != nil {
			log.Printf("Warning: failed to record change address: %v", dbErr)
		}
	}

	return txid, nil
}

// SendBitcoin sends Bitcoin to an arbitrary address using the remote signer or hot wallet
// This is an exported wrapper for sendViaRemoteSigner for use by admin tools
func (s *Service) SendBitcoin(destAddress string, amountSat int64) (string, error) {
	// Get current max deposit index from rental_addresses
	var maxIndex int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(derivation_index), 0) FROM rental_addresses`).Scan(&maxIndex)
	if err != nil {
		return "", fmt.Errorf("failed to get max derivation index: %w", err)
	}
	// Add buffer to scan more addresses
	maxIndex += 10

	var txid string
	if s.remoteSigner != nil {
		txid, err = s.sendViaRemoteSigner(destAddress, amountSat, maxIndex)
	} else if s.hotWallet != nil {
		txid, err = s.hotWallet.SendToAddress(destAddress, amountSat, maxIndex)
	} else {
		return "", fmt.Errorf("no wallet available for sending")
	}

	if err != nil {
		return "", err
	}

	log.Printf("SendBitcoin: Sent %d sats to %s, txid: %s", amountSat, destAddress, txid)
	return txid, nil
}

// updateOrderStatus updates the order status
func (s *Service) updateOrderStatus(orderID int, status, errorMsg string) {
	var err error
	if status == OrderStatusCompleted || status == OrderStatusFailed || status == OrderStatusCancelled {
		_, err = s.db.Exec(`
			UPDATE rental_orders
			SET status = $1, error_message = $2, completed_at = NOW()
			WHERE id = $3
		`, status, errorMsg, orderID)
	} else {
		_, err = s.db.Exec(`
			UPDATE rental_orders
			SET status = $1, error_message = $2
			WHERE id = $3
		`, status, errorMsg, orderID)
	}
	if err != nil {
		log.Printf("Failed to update order %d status: %v", orderID, err)
	}
}

// AdminAutoFund manually triggers auto-fund for a completed/cancelled order
func (s *Service) AdminAutoFund(orderID int) error {
	// Fetch order details
	var spentSat int64
	var status string
	err := s.db.QueryRow(`
		SELECT amount_spent_sat, status FROM rental_orders WHERE id = $1
	`, orderID).Scan(&spentSat, &status)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}

	if status != "completed" && status != "cancelled" {
		return fmt.Errorf("order must be completed or cancelled to auto-fund (status: %s)", status)
	}

	if spentSat <= 0 {
		return fmt.Errorf("no spent amount to fund (spent: %d sats)", spentSat)
	}

	if s.hotWallet == nil {
		return fmt.Errorf("hot wallet not configured")
	}

	if s.config.BraiinsDepositAddress == "" {
		return fmt.Errorf("Braiins deposit address not configured")
	}

	// Trigger auto-fund
	go s.autoFundBraiins(orderID, spentSat)
	log.Printf("Admin triggered auto-fund for order %d: %d sats", orderID, spentSat)
	return nil
}

// setMinerSoloMode sets or clears the solo mining flag for a miner on the pool
func (s *Service) setMinerSoloMode(targetIdentity string, solo bool) error {
	// Extract address from target_identity (format: address.workername)
	parts := strings.Split(targetIdentity, ".")
	address := parts[0]
	if !strings.HasPrefix(address, "bitcoincashii:") {
		address = "bitcoincashii:" + address
	}

	// Update the miners table in the pool database
	_, err := s.db.Exec(`
		INSERT INTO miners (address, solo_mining, manual_diff, min_payout, updated_at)
		VALUES ($1, $2, 0, 0, NOW())
		ON CONFLICT (address) DO UPDATE SET
			solo_mining = $2,
			updated_at = NOW()
	`, address, solo)
	if err != nil {
		log.Printf("Failed to set solo_mining=%v for %s: %v", solo, address, err)
		return err
	}
	log.Printf("Set solo_mining=%v for miner %s", solo, address)
	return nil
}

// refundOrder refunds the order amount to customer (idempotent)
func (s *Service) refundOrder(orderID, customerID int, amountSat int64) {
	// Check if refund already exists for this order to prevent double refund
	var refundExists bool
	err := s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM rental_ledger
			WHERE order_id = $1 AND tx_type = 'order_refund'
		)
	`, orderID).Scan(&refundExists)
	if err != nil {
		log.Printf("Failed to check refund existence for order %d: %v", orderID, err)
		return
	}

	if refundExists {
		log.Printf("Refund already exists for order %d, skipping", orderID)
		return
	}

	_, err = s.db.Exec(`
		INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, order_id, memo)
		VALUES ($1, 'order_refund', $2, $3, 'Order failed - refund')
	`, customerID, amountSat, orderID)
	if err != nil {
		log.Printf("Failed to refund order %d: %v", orderID, err)
	}
}

// GetOrder returns order details
func (s *Service) GetOrder(customerID, orderID int) (*OrderResponse, error) {
	var order Order
	err := s.db.QueryRow(`
		SELECT id, customer_id, COALESCE(braiins_bid_id, ''), target_pool_url, target_identity,
		       budget_sat, price_sat, speed_limit_ph, margin_pct, COALESCE(mining_mode, 'pplns'), status,
		       amount_spent_sat, COALESCE(error_message, ''), created_at, started_at, completed_at
		FROM rental_orders
		WHERE id = $1 AND customer_id = $2
	`, orderID, customerID).Scan(
		&order.ID, &order.CustomerID, &order.BraiinsBidID, &order.TargetPoolURL, &order.TargetIdentity,
		&order.BudgetSat, &order.PriceSat, &order.SpeedLimitPH, &order.MarginPct, &order.MiningMode, &order.Status,
		&order.AmountSpentSat, &order.ErrorMessage, &order.CreatedAt, &order.StartedAt, &order.CompletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("order not found")
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	response := &OrderResponse{
		Order:           &order,
		AmountRemaining: order.BudgetSat - order.AmountSpentSat,
	}

	// If order is active, fetch real-time data from Braiins
	if order.Status == OrderStatusActive && order.BraiinsBidID != "" {
		bid, err := s.braiins.GetBidDetail(order.BraiinsBidID)
		if err == nil {
			response.ProgressPct = bid.ProgressPct
			response.CurrentSpeedPH = bid.AvgSpeedPH
			response.AmountRemaining = order.BudgetSat - bid.AmountSpentSat
		}
	}

	return response, nil
}

// GetOrders returns all orders for a customer
func (s *Service) GetOrders(customerID int) ([]Order, error) {
	rows, err := s.db.Query(`
		SELECT id, customer_id, COALESCE(braiins_bid_id, ''), target_pool_url, target_identity,
		       budget_sat, price_sat, speed_limit_ph, margin_pct, COALESCE(mining_mode, 'pplns'), status,
		       amount_spent_sat, COALESCE(error_message, ''), created_at, started_at, completed_at
		FROM rental_orders
		WHERE customer_id = $1
		ORDER BY created_at DESC
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		err := rows.Scan(
			&o.ID, &o.CustomerID, &o.BraiinsBidID, &o.TargetPoolURL, &o.TargetIdentity,
			&o.BudgetSat, &o.PriceSat, &o.SpeedLimitPH, &o.MarginPct, &o.MiningMode, &o.Status,
			&o.AmountSpentSat, &o.ErrorMessage, &o.CreatedAt, &o.StartedAt, &o.CompletedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		orders = append(orders, o)
	}

	return orders, nil
}

// CancelOrder cancels an active order
func (s *Service) CancelOrder(customerID, orderID int) error {
	// Start transaction to prevent double-cancel race condition
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Lock the order row and check status atomically
	var order Order
	var braiinsBidID sql.NullString
	var startedAt sql.NullTime
	err = tx.QueryRow(`
		SELECT id, customer_id, braiins_bid_id, budget_sat, amount_spent_sat, status, started_at, mining_mode, target_identity
		FROM rental_orders
		WHERE id = $1 AND customer_id = $2
		FOR UPDATE
	`, orderID, customerID).Scan(
		&order.ID, &order.CustomerID, &braiinsBidID, &order.BudgetSat, &order.AmountSpentSat, &order.Status, &startedAt,
		&order.MiningMode, &order.TargetIdentity,
	)
	if err == sql.ErrNoRows {
		return fmt.Errorf("order not found")
	}
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}
	if braiinsBidID.Valid {
		order.BraiinsBidID = braiinsBidID.String
	}

	if order.Status != OrderStatusActive && order.Status != OrderStatusPending {
		return fmt.Errorf("order cannot be cancelled (status: %s)", order.Status)
	}

	// Cancel on Braiins if active (outside transaction since it's external)
	if order.BraiinsBidID != "" {
		// Get actual spend from Braiins before cancelling
		bid, _ := s.braiins.GetBidDetail(order.BraiinsBidID)
		if bid != nil {
			order.AmountSpentSat = bid.AmountSpentSat
		}

		err = s.braiins.CancelBid(order.BraiinsBidID)
		if err != nil {
			log.Printf("Error: failed to cancel Braiins bid %s: %v", order.BraiinsBidID, err)
			// CRITICAL: Do not proceed with local cancellation if Braiins cancellation fails
			// This prevents refunding the user while the bid continues running
			return fmt.Errorf("failed to cancel order on hashrate marketplace - please try again in 30 seconds")
		}
	}

	// Update order status within transaction
	_, err = tx.Exec(`
		UPDATE rental_orders
		SET status = $1, error_message = $2, completed_at = NOW(), amount_spent_sat = $3
		WHERE id = $4
	`, OrderStatusCancelled, "Cancelled by user", order.AmountSpentSat, orderID)
	if err != nil {
		return fmt.Errorf("failed to update order status: %w", err)
	}

	// Refund unused amount within same transaction
	// First verify a charge exists for this order (prevents refunding orders that were never charged)
	refundAmount := order.BudgetSat - order.AmountSpentSat
	if refundAmount > 0 {
		var chargeExists bool
		err = tx.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM rental_ledger
				WHERE order_id = $1 AND tx_type = 'order_charge'
			)
		`, orderID).Scan(&chargeExists)
		if err != nil {
			return fmt.Errorf("failed to check charge existence: %w", err)
		}

		if !chargeExists {
			log.Printf("WARNING: Order %d has no charge record, skipping refund", orderID)
		} else {
			// Check if refund already exists to prevent double refund
			var refundExists bool
			err = tx.QueryRow(`
				SELECT EXISTS(
					SELECT 1 FROM rental_ledger
					WHERE order_id = $1 AND tx_type = 'order_refund'
				)
			`, orderID).Scan(&refundExists)
			if err != nil {
				return fmt.Errorf("failed to check refund existence: %w", err)
			}

			if refundExists {
				log.Printf("Refund already exists for order %d, skipping", orderID)
			} else {
				_, err = tx.Exec(`
					INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, order_id, memo)
					VALUES ($1, 'order_refund', $2, $3, 'Order cancelled - refund unused')
				`, customerID, refundAmount, orderID)
				if err != nil {
					return fmt.Errorf("failed to refund: %w", err)
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	// Disable solo mining mode on the pool for cancelled solo orders
	if order.MiningMode == "solo" {
		if err := s.setMinerSoloMode(order.TargetIdentity, false); err != nil {
			log.Printf("Warning: failed to disable solo mode for cancelled order %d: %v", orderID, err)
		}
	}

	log.Printf("Order %d cancelled, refunded %d sats", orderID, refundAmount)
	return nil
}

// ExtendOrder adds more budget/time to an active order
func (s *Service) ExtendOrder(customerID, orderID int, additionalBudgetSat int64) (*OrderResponse, error) {
	if additionalBudgetSat <= 0 {
		return nil, fmt.Errorf("additional_budget_sat must be positive")
	}
	if additionalBudgetSat > s.config.MaxOrderSat {
		return nil, fmt.Errorf("maximum additional budget is %d sats", s.config.MaxOrderSat)
	}

	// Start SERIALIZABLE transaction for financial operation
	tx, cancel, err := s.beginFinancialTx()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer cancel()
	defer tx.Rollback()

	// Lock customer and order rows
	var customerExists bool
	err = tx.QueryRow(`SELECT TRUE FROM rental_customers WHERE id = $1 FOR UPDATE`, customerID).Scan(&customerExists)
	if err != nil {
		return nil, fmt.Errorf("failed to lock customer: %w", err)
	}

	// Get and lock order
	var order Order
	var braiinsBidID sql.NullString
	err = tx.QueryRow(`
		SELECT id, customer_id, braiins_bid_id, target_pool_url, target_identity,
		       budget_sat, price_sat, COALESCE(market_price_sat, price_sat), speed_limit_ph,
		       amount_spent_sat, margin_pct, status
		FROM rental_orders
		WHERE id = $1 AND customer_id = $2
		FOR UPDATE
	`, orderID, customerID).Scan(
		&order.ID, &order.CustomerID, &braiinsBidID, &order.TargetPoolURL, &order.TargetIdentity,
		&order.BudgetSat, &order.PriceSat, &order.MarketPriceSat, &order.SpeedLimitPH,
		&order.AmountSpentSat, &order.MarginPct, &order.Status,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("order not found")
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	if braiinsBidID.Valid {
		order.BraiinsBidID = braiinsBidID.String
	}

	if order.Status != OrderStatusActive {
		return nil, fmt.Errorf("can only extend active orders (current status: %s)", order.Status)
	}

	// Check customer balance
	var balanceSat int64
	err = tx.QueryRow(`
		SELECT COALESCE(SUM(amount_sat), 0) FROM rental_ledger WHERE customer_id = $1
	`, customerID).Scan(&balanceSat)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	if balanceSat < additionalBudgetSat {
		return nil, fmt.Errorf("insufficient balance: have %d sats, need %d sats", balanceSat, additionalBudgetSat)
	}

	// SECURITY: Check for integer overflow before multiplication
	const maxSafeBudget int64 = 922337203685477
	if additionalBudgetSat > maxSafeBudget {
		return nil, fmt.Errorf("additional budget exceeds maximum safe value")
	}

	// Calculate margin for additional budget using integer arithmetic
	marginBasisPoints := int64(order.MarginPct * 100) // 25% = 2500 basis points
	additionalBraiinsBudget := (additionalBudgetSat * 10000) / (10000 + marginBasisPoints)
	additionalMargin := additionalBudgetSat - additionalBraiinsBudget

	// Get current spend from Braiins
	var currentSpent int64
	if order.BraiinsBidID != "" {
		bid, err := s.braiins.GetBidDetail(order.BraiinsBidID)
		if err == nil {
			currentSpent = bid.AmountSpentSat
		}
	}

	// Calculate new totals using integer arithmetic
	newTotalBudget := order.BudgetSat + additionalBudgetSat
	newBraiinsBudget := (newTotalBudget * 10000) / (10000 + marginBasisPoints)

	// Cancel old Braiins bid and create new one with increased budget
	if order.BraiinsBidID != "" {
		// Cancel old bid
		s.braiins.CancelBid(order.BraiinsBidID)
	}

	// Calculate remaining Braiins budget (what hasn't been spent yet + new)
	remainingBraiinsBudget := newBraiinsBudget - currentSpent
	if remainingBraiinsBudget <= 0 {
		return nil, fmt.Errorf("order already fully consumed")
	}

	// Place new bid with remaining + additional budget
	// Skip Braiins API for test orders (fake bid IDs starting with "test-")
	var newBidID string
	if strings.HasPrefix(order.BraiinsBidID, "test-") {
		newBidID = order.BraiinsBidID // Keep same test bid ID
		log.Printf("Test mode: skipping Braiins API for order %d extend", orderID)
	} else {
		memo := fmt.Sprintf("Forge Pool rental order #%d (extended)", orderID)
		clOrderID := fmt.Sprintf("FP-%d-ext", orderID)
		// Add 5% premium to bid price to ensure order fills
		// Market often clears above best ask due to competition
		bidPrice := (order.MarketPriceSat * 105) / 100
		// Align bid price to tick size (1000 sats) - round UP to ensure competitive pricing
		const tickSize int64 = 1000
		if bidPrice%tickSize != 0 {
			bidPrice = ((bidPrice / tickSize) + 1) * tickSize
		}
		var err error
		newBidID, err = s.braiins.PlaceBidWithClientID(
			order.TargetPoolURL,
			order.TargetIdentity,
			remainingBraiinsBudget,
			bidPrice,
			order.SpeedLimitPH,
			memo,
			clOrderID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to place extended bid on Braiins: %w", err)
		}
	}

	// If the DB transaction below does not commit, the bid just placed on Braiins
	// would be a live orphan (no order row, no customer charge) draining pool
	// balance. Cancel it on any non-commit return path, as executeOrder does on a
	// lost race. Set committed=true only once the tx has durably committed.
	committed := false
	defer func() {
		if !committed && newBidID != "" && !strings.HasPrefix(newBidID, "test-") {
			s.braiins.CancelBid(newBidID)
		}
	}()

	// Update order with new budget and bid ID
	_, err = tx.Exec(`
		UPDATE rental_orders
		SET budget_sat = $1, braiins_budget_sat = $2, pool_margin_sat = pool_margin_sat + $3,
		    braiins_bid_id = $4, amount_spent_sat = $5
		WHERE id = $6
	`, newTotalBudget, newBraiinsBudget, additionalMargin, newBidID, currentSpent, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to update order: %w", err)
	}

	// Deduct additional budget from balance
	_, err = tx.Exec(`
		INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, order_id, memo)
		VALUES ($1, 'order_charge', $2, $3, 'Extended order - additional budget')
	`, customerID, -additionalBudgetSat, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to charge: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}
	committed = true

	log.Printf("Order %d extended: +%d sats (new total: %d), new bid: %s",
		orderID, additionalBudgetSat, newTotalBudget, newBidID)

	// Return updated order
	return s.GetOrder(customerID, orderID)
}

// StartDepositWatcher starts the background deposit monitoring
func (s *Service) StartDepositWatcher() {
	go s.watchDeposits()
	go s.syncOrderStatus()
	go s.monitorBraiinsBalance()
	go s.cleanupExpiredData()
	s.startBraiinsPaymentSync()
}

// cleanupExpiredData periodically cleans up expired sessions, tokens, and rate limits
func (s *Service) cleanupExpiredData() {
	// Run cleanup every 15 minutes
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	// Initial cleanup after 2 minutes
	time.Sleep(2 * time.Minute)
	s.runCleanup()

	for {
		select {
		case <-s.shutdown:
			return
		case <-ticker.C:
			s.runCleanup()
		}
	}
}

// runCleanup performs the actual cleanup operations
func (s *Service) runCleanup() {
	// Clean expired customer sessions
	result, _ := s.db.Exec(`DELETE FROM rental_sessions WHERE expires_at < NOW()`)
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("Cleaned up %d expired customer sessions", rows)
	}

	// Clean expired admin sessions
	result, _ = s.db.Exec(`DELETE FROM rental_admin_sessions WHERE expires_at < NOW()`)
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("Cleaned up %d expired admin sessions", rows)
	}

	// Clean expired email verification tokens (older than 24 hours)
	result, _ = s.db.Exec(`DELETE FROM rental_email_tokens WHERE created_at < NOW() - INTERVAL '24 hours'`)
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("Cleaned up %d expired email tokens", rows)
	}

	// Clean expired password reset tokens (older than 1 hour)
	result, _ = s.db.Exec(`DELETE FROM rental_password_resets WHERE created_at < NOW() - INTERVAL '1 hour'`)
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("Cleaned up %d expired password reset tokens", rows)
	}

	// Clean old rate limit entries (older than 1 hour)
	result, _ = s.db.Exec(`DELETE FROM rental_rate_limits WHERE window_start < NOW() - INTERVAL '1 hour'`)
	if rows, _ := result.RowsAffected(); rows > 0 {
		log.Printf("Cleaned up %d old rate limit entries", rows)
	}
}

// monitorBraiinsBalance periodically checks Braiins balance and alerts if low
func (s *Service) monitorBraiinsBalance() {
	// Check every 15 minutes
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	// Initial check after 1 minute
	time.Sleep(1 * time.Minute)
	s.CheckBraiinsBalanceAndAlert()

	for {
		select {
		case <-s.shutdown:
			return
		case <-ticker.C:
			s.CheckBraiinsBalanceAndAlert()
		}
	}
}

// watchDeposits monitors for new BTC deposits
func (s *Service) watchDeposits() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdown:
			return
		case <-ticker.C:
			s.checkDeposits()
		}
	}
}

// checkDeposits checks all customer addresses for new deposits
func (s *Service) checkDeposits() {
	rows, err := s.db.Query(`
		SELECT ra.id, ra.customer_id, ra.btc_address
		FROM rental_addresses ra
		JOIN rental_customers rc ON ra.customer_id = rc.id
	`)
	if err != nil {
		log.Printf("Failed to fetch addresses: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var addrID, customerID int
		var btcAddress string
		if err := rows.Scan(&addrID, &customerID, &btcAddress); err != nil {
			continue
		}

		deposits, err := s.btc.FindDepositsToAddress(btcAddress)
		if err != nil {
			log.Printf("Failed to check deposits for %s: %v", btcAddress, err)
			continue
		}

		for _, dep := range deposits {
			s.processDeposit(customerID, dep)
		}
	}
}

// processDeposit processes a single deposit
func (s *Service) processDeposit(customerID int, dep DepositInfo) {
	// CRITICAL: Check if this transaction is a system-generated transaction
	// (Braiins payment, withdrawal, or wallet change). If so, this is NOT
	// a real external deposit - it's internal change that ended up at a
	// customer address due to address collision. Do not credit it.
	var isSystemTx bool
	err := s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM braiins_payments WHERE btc_txid = $1
			UNION ALL
			SELECT 1 FROM rental_withdrawals WHERE txid = $1
			UNION ALL
			SELECT 1 FROM wallet_change_addresses WHERE btc_txid = $1
		)
	`, dep.TxID).Scan(&isSystemTx)
	if err != nil {
		log.Printf("Error checking if tx is system-generated: %v", err)
		// Continue cautiously - don't credit if we can't verify
		return
	}
	if isSystemTx {
		log.Printf("WARNING: Skipping deposit %s - this is a system-generated transaction (change/payment), not an external deposit", dep.TxID[:16])
		return
	}

	// Check if we've already processed this deposit
	var exists bool
	var credited bool
	err = s.db.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM rental_deposits WHERE btc_txid = $1 AND vout = $2)
	`, dep.TxID, dep.Vout).Scan(&exists)
	if err != nil {
		log.Printf("Error checking deposit existence: %v", err)
		return
	}

	if exists {
		// Update confirmations and check if it needs crediting
		err = s.db.QueryRow(`
			UPDATE rental_deposits SET confirmations = $1 WHERE btc_txid = $2 AND vout = $3
			RETURNING credited
		`, dep.Confirmations, dep.TxID, dep.Vout).Scan(&credited)
		if err != nil {
			log.Printf("Failed to update deposit confirmations: %v", err)
			return
		}

		// If not credited yet and has enough confirmations, try to credit now
		if !credited && dep.Confirmations >= s.config.RequiredConfirms {
			if err := s.creditDeposit(customerID, dep.TxID, dep.Vout, dep.AmountSat); err != nil {
				log.Printf("Failed to credit deposit %s on retry: %v", dep.TxID[:16], err)
			}
		}
		return
	}

	// Insert new deposit record
	_, err = s.db.Exec(`
		INSERT INTO rental_deposits (btc_txid, vout, btc_address, amount_sat, confirmations, customer_id)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, dep.TxID, dep.Vout, dep.Address, dep.AmountSat, dep.Confirmations, customerID)
	if err != nil {
		log.Printf("Failed to record deposit %s: %v", dep.TxID, err)
		return
	}

	log.Printf("New deposit detected: %d sats from %s (tx: %s, %d confirms)",
		dep.AmountSat, dep.Address, dep.TxID[:16], dep.Confirmations)

	// Credit if confirmed
	if dep.Confirmations >= s.config.RequiredConfirms {
		if err := s.creditDeposit(customerID, dep.TxID, dep.Vout, dep.AmountSat); err != nil {
			log.Printf("Failed to credit deposit %s: %v", dep.TxID[:16], err)
		}
	}
}

// creditDeposit credits a confirmed deposit to customer balance
// Returns nil if already credited, or error if crediting failed
func (s *Service) creditDeposit(customerID int, txid string, vout uint32, amountSat int64) error {
	// Use SERIALIZABLE isolation for financial transaction with timeout
	tx, cancel, err := s.beginFinancialTx()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer cancel()
	defer tx.Rollback()

	// Check not already credited (with row lock)
	var credited bool
	err = tx.QueryRow(`SELECT credited FROM rental_deposits WHERE btc_txid = $1 AND vout = $2 FOR UPDATE`, txid, vout).Scan(&credited)
	if err != nil {
		return fmt.Errorf("failed to check deposit status: %w", err)
	}
	if credited {
		// Already credited - not an error, just no-op
		return nil
	}

	// Credit balance
	_, err = tx.Exec(`
		INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, btc_txid, memo)
		VALUES ($1, 'deposit', $2, $3, 'BTC deposit')
	`, customerID, amountSat, txid)
	if err != nil {
		return fmt.Errorf("failed to credit balance: %w", err)
	}

	// Mark as credited
	_, err = tx.Exec(`
		UPDATE rental_deposits SET credited = TRUE, confirmed_at = NOW()
		WHERE btc_txid = $1 AND vout = $2
	`, txid, vout)
	if err != nil {
		return fmt.Errorf("failed to mark deposit credited: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	log.Printf("Credited %d sats to customer %d (tx: %s)", amountSat, customerID, txid[:16])

	// Send deposit confirmation email
	go s.sendDepositEmail(customerID, amountSat, txid)

	return nil
}

// sendDepositEmail sends deposit confirmation email (fire-and-forget)
func (s *Service) sendDepositEmail(customerID int, amountSat int64, txid string) {
	amountBTC := fmt.Sprintf("%.8f", float64(amountSat)/100000000)

	// Broadcast real-time balance update via WebSocket
	s.broadcastBalanceUpdate(customerID)

	// Create in-app notification
	s.CreateNotification(customerID, "deposit", "Deposit Confirmed",
		fmt.Sprintf("Your deposit of %s BTC has been confirmed.", amountBTC), "/dashboard")

	// Send email if preferences allow
	if s.email == nil || !s.ShouldSendEmail(customerID, "deposit") {
		return
	}
	customer, err := s.GetCustomerByID(customerID)
	if err != nil || customer.Email == "" {
		return
	}
	if err := s.email.SendDepositConfirmedEmail(customer.Email, amountBTC, txid); err != nil {
		log.Printf("Failed to send deposit email to %s: %v", customer.Email, err)
	}
}

// syncOrderStatus syncs active orders with Braiins
func (s *Service) syncOrderStatus() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdown:
			return
		case <-ticker.C:
			s.checkActiveOrders()
		}
	}
}

// checkActiveOrders updates status of active orders
func (s *Service) checkActiveOrders() {
	rows, err := s.db.Query(`
		SELECT id, braiins_bid_id, budget_sat, customer_id, mining_mode, target_identity
		FROM rental_orders
		WHERE status = 'active' AND braiins_bid_id IS NOT NULL
	`)
	if err != nil {
		log.Printf("Failed to fetch active orders: %v", err)
		return
	}
	defer rows.Close()

	// Collect orders to process (can't use transaction while iterating)
	type orderInfo struct {
		ID             int
		BidID          string
		BudgetSat      int64
		CustomerID     int
		MiningMode     string
		TargetIdentity string
	}
	var orders []orderInfo
	for rows.Next() {
		var o orderInfo
		if err := rows.Scan(&o.ID, &o.BidID, &o.BudgetSat, &o.CustomerID, &o.MiningMode, &o.TargetIdentity); err != nil {
			continue
		}
		orders = append(orders, o)
	}
	rows.Close()

	// Process each order
	for _, order := range orders {
		s.syncSingleOrder(order.ID, order.BidID, order.BudgetSat, order.CustomerID, order.MiningMode, order.TargetIdentity)
	}
}

// syncSingleOrder syncs a single order with Braiins - uses transaction to prevent double refund
func (s *Service) syncSingleOrder(orderID int, bidID string, budgetSat int64, customerID int, miningMode, targetIdentity string) {
	bid, err := s.braiins.GetBidDetail(bidID)
	if err != nil {
		log.Printf("Failed to get bid %s status: %v", bidID, err)
		return
	}

	// Update spent amount only if it increased (Braiins may return 0 for cancelled orders)
	// Use GREATEST to preserve the highest known spent amount
	if bid.AmountSpentSat > 0 {
		s.db.Exec(`
			UPDATE rental_orders SET amount_spent_sat = GREATEST(amount_spent_sat, $1) WHERE id = $2
		`, bid.AmountSpentSat, orderID)
	}

	// Broadcast real-time progress update via WebSocket
	s.broadcastOrderUpdate(customerID, orderID, OrderStatusActive, bid.AmountSpentSat, 0)

	// Auto-escalate bid price if Braiins can't fill at current price
	// Check if bid is active but not getting hashrate
	if bid.Status == "BID_STATUS_ACTIVE" && bid.AvgSpeedPH == 0 && bid.AmountRemaining > 0 {
		// Escalate if Braiins explicitly paused due to price
		if strings.Contains(bid.LastPauseReason, "not possible to deliver") {
			log.Printf("Order %d: bid %s paused due to price (%d sat), attempting auto-escalation",
				orderID, bidID, bid.PriceSat)
			s.escalateBidPrice(orderID, bidID, bid)
			return
		}

		// Also escalate if order has been active for >5 minutes with zero hashrate
		// This handles cases where Braiins doesn't pause but higher-priced bids get priority
		var startedAt time.Time
		err := s.db.QueryRow(`SELECT started_at FROM rental_orders WHERE id = $1`, orderID).Scan(&startedAt)
		if err == nil && !startedAt.IsZero() {
			idleMinutes := time.Since(startedAt).Minutes()
			if idleMinutes > 5 && bid.AmountSpentSat == 0 {
				log.Printf("Order %d: bid %s idle for %.1f minutes with 0 hashrate, attempting auto-escalation",
					orderID, bidID, idleMinutes)
				s.escalateBidPrice(orderID, bidID, bid)
				return
			}
		}
	}

	// Check if completed or cancelled - need transaction for status change + refund
	if bid.Status == "BID_STATUS_FULFILLED" || bid.Status == "BID_STATUS_CANCELED" {
		tx, err := s.db.Begin()
		if err != nil {
			log.Printf("Failed to begin transaction for order %d: %v", orderID, err)
			return
		}
		defer tx.Rollback()

		// Lock and check order status to prevent double processing
		var currentStatus string
		err = tx.QueryRow(`
			SELECT status FROM rental_orders WHERE id = $1 FOR UPDATE
		`, orderID).Scan(&currentStatus)
		if err != nil {
			return
		}

		// Only process if still active
		if currentStatus != OrderStatusActive {
			return // Already processed by CancelOrder or another sync
		}

		newStatus := OrderStatusCompleted
		if bid.Status == "BID_STATUS_CANCELED" {
			newStatus = OrderStatusCancelled
		}

		// Update order status - use GREATEST to preserve spent amount if Braiins returns 0
		_, err = tx.Exec(`
			UPDATE rental_orders
			SET status = $1, amount_spent_sat = GREATEST(amount_spent_sat, $2), completed_at = NOW()
			WHERE id = $3
		`, newStatus, bid.AmountSpentSat, orderID)
		if err != nil {
			log.Printf("Failed to update order %d: %v", orderID, err)
			return
		}

		// Refund unused budget ONLY for cancelled orders
		// Completed orders = customer paid for service and received it, pool keeps the margin
		// Fetch the actual spent amount from DB (may be higher than bid.AmountSpentSat if Braiins returned 0)
		var actualSpent int64
		tx.QueryRow(`SELECT amount_spent_sat FROM rental_orders WHERE id = $1`, orderID).Scan(&actualSpent)

		if newStatus == OrderStatusCancelled {
			refund := budgetSat - actualSpent
			if refund > 0 {
				// First verify a charge exists for this order (prevents refunding orders that were never charged)
				var chargeExists bool
				tx.QueryRow(`
					SELECT EXISTS(
						SELECT 1 FROM rental_ledger
						WHERE order_id = $1 AND tx_type = 'order_charge'
					)
				`, orderID).Scan(&chargeExists)

				if !chargeExists {
					log.Printf("WARNING: Order %d has no charge record, skipping refund in sync", orderID)
				} else {
					// Check if refund already exists for this order
					var refundExists bool
					tx.QueryRow(`
						SELECT EXISTS(
							SELECT 1 FROM rental_ledger
							WHERE order_id = $1 AND tx_type = 'order_refund'
						)
					`, orderID).Scan(&refundExists)

					if !refundExists {
						_, err = tx.Exec(`
							INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, order_id, memo)
							VALUES ($1, 'order_refund', $2, $3, 'Order cancelled by Braiins - refund unused')
						`, customerID, refund, orderID)
						if err != nil {
							log.Printf("Failed to refund order %d: %v", orderID, err)
							return
						}
						log.Printf("Refunded %d sats for cancelled order %d", refund, orderID)
					}
				}
			}
		}

		if err := tx.Commit(); err != nil {
			log.Printf("Failed to commit order %d sync: %v", orderID, err)
			return
		}

		log.Printf("Order %d synced (status: %s, spent: %d sats)", orderID, newStatus, actualSpent)

		// Disable solo mining mode on the pool for completed solo orders
		if miningMode == "solo" {
			if err := s.setMinerSoloMode(targetIdentity, false); err != nil {
				log.Printf("Warning: failed to disable solo mode for order %d: %v", orderID, err)
			}
		}

		// Send order completed email
		refundSat := int64(0)
		if newStatus == OrderStatusCancelled {
			refundSat = budgetSat - actualSpent
			if refundSat < 0 {
				refundSat = 0
			}
		}
		go s.sendOrderCompletedEmail(orderID, customerID, actualSpent, refundSat)

		// Payment to Braiins is now handled by the braiins_payments tracking system
		// which processes payments in batch after syncing completed orders
	}
}

// escalateBidPrice cancels current bid and creates a new one with higher price
func (s *Service) escalateBidPrice(orderID int, bidID string, currentBid *BraiinsBid) {
	// Get order details for creating new bid
	var order struct {
		TargetPoolURL  string
		TargetIdentity string
		MarketPriceSat int64
	}
	err := s.db.QueryRow(`
		SELECT target_pool_url, target_identity, COALESCE(market_price_sat, price_sat)
		FROM rental_orders WHERE id = $1
	`, orderID).Scan(&order.TargetPoolURL, &order.TargetIdentity, &order.MarketPriceSat)
	if err != nil {
		log.Printf("Order %d: failed to get order details for escalation: %v", orderID, err)
		return
	}

	// Calculate new price: current price + 5%, aligned to tick size
	newPrice := (currentBid.PriceSat * 105) / 100
	const tickSize int64 = 1000
	if newPrice%tickSize != 0 {
		newPrice = ((newPrice / tickSize) + 1) * tickSize
	}

	// Cap price escalation at 20% above original market price to prevent runaway costs
	maxPrice := (order.MarketPriceSat * 120) / 100
	if newPrice > maxPrice {
		log.Printf("Order %d: price escalation capped at 20%% above market (%d > %d), not escalating further",
			orderID, newPrice, maxPrice)
		return
	}

	log.Printf("Order %d: escalating bid from %d to %d sat", orderID, currentBid.PriceSat, newPrice)

	// Cancel current bid
	if err := s.braiins.CancelBid(bidID); err != nil {
		log.Printf("Order %d: failed to cancel bid %s: %v", orderID, bidID, err)
		return
	}

	// Create new bid with higher price
	memo := fmt.Sprintf("Forge Pool rental order #%d (escalated)", orderID)
	clOrderID := fmt.Sprintf("FP-%d-esc", orderID)
	newBidID, err := s.braiins.PlaceBidWithClientID(
		order.TargetPoolURL,
		order.TargetIdentity,
		currentBid.AmountRemaining, // Use remaining budget
		newPrice,
		currentBid.SpeedLimitPH,
		memo,
		clOrderID,
	)
	if err != nil {
		log.Printf("Order %d: failed to place escalated bid: %v", orderID, err)
		// Try to restore original bid
		s.braiins.PlaceBidWithClientID(
			order.TargetPoolURL,
			order.TargetIdentity,
			currentBid.AmountRemaining,
			currentBid.PriceSat,
			currentBid.SpeedLimitPH,
			currentBid.Memo,
			fmt.Sprintf("FP-%d", orderID),
		)
		return
	}

	// Update database with new bid ID
	_, err = s.db.Exec(`
		UPDATE rental_orders SET braiins_bid_id = $1 WHERE id = $2
	`, newBidID, orderID)
	if err != nil {
		log.Printf("Order %d: failed to update bid ID: %v", orderID, err)
	}

	log.Printf("Order %d: successfully escalated to bid %s at %d sat", orderID, newBidID, newPrice)
}

// broadcastOrderUpdate sends a real-time order update via WebSocket
func (s *Service) broadcastOrderUpdate(customerID, orderID int, status string, spentSat, refundSat int64) {
	if s.wsHub == nil {
		return
	}
	s.wsHub.SendOrderUpdate(customerID, &OrderUpdate{
		OrderID:   orderID,
		Status:    status,
		SpentBTC:  fmt.Sprintf("%.8f", float64(spentSat)/100000000),
		RefundBTC: fmt.Sprintf("%.8f", float64(refundSat)/100000000),
	})
}

// broadcastBalanceUpdate sends a real-time balance update via WebSocket
func (s *Service) broadcastBalanceUpdate(customerID int) {
	if s.wsHub == nil {
		return
	}
	balance, err := s.GetBalance(customerID)
	if err != nil {
		return
	}
	pendingBTC := fmt.Sprintf("%.8f", float64(balance.PendingDeposit)/100000000)
	s.wsHub.SendBalanceUpdate(customerID, &BalanceUpdate{
		BalanceBTC:   balance.BalanceBTC,
		AvailableBTC: balance.BalanceBTC,
		PendingBTC:   pendingBTC,
	})
}

// sendOrderCompletedEmail sends order completed notification (fire-and-forget)
func (s *Service) sendOrderCompletedEmail(orderID, customerID int, spentSat, refundSat int64) {
	spentBTC := fmt.Sprintf("%.8f", float64(spentSat)/100000000)
	refundBTC := fmt.Sprintf("%.8f", float64(refundSat)/100000000)

	// Broadcast real-time update via WebSocket
	s.broadcastOrderUpdate(customerID, orderID, OrderStatusCompleted, spentSat, refundSat)
	s.broadcastBalanceUpdate(customerID)

	// Check and send balance alert if applicable
	s.CheckAndSendBalanceAlert(customerID)

	// Create in-app notification
	msg := fmt.Sprintf("Order #%d completed. Spent: %s BTC", orderID, spentBTC)
	if refundSat > 0 {
		msg += fmt.Sprintf(", Refund: %s BTC", refundBTC)
	}
	s.CreateNotification(customerID, "order_completed", "Order Completed", msg,
		fmt.Sprintf("/mining/%d", orderID))

	// Send email if preferences allow
	if s.email == nil || !s.ShouldSendEmail(customerID, "order") {
		return
	}
	customer, err := s.GetCustomerByID(customerID)
	if err != nil || customer.Email == "" {
		return
	}
	if err := s.email.SendOrderCompletedEmail(customer.Email, orderID, spentBTC, refundBTC); err != nil {
		log.Printf("Failed to send order completed email to %s: %v", customer.Email, err)
	}
}

// Stop gracefully stops the service
func (s *Service) Stop() {
	close(s.shutdown)
}

// GetBraiinsBalance returns the pool's Braiins balance
func (s *Service) GetBraiinsBalance() (*BraiinsBalance, error) {
	return s.braiins.GetBalance()
}

// ============================================================================
// Withdrawal Methods
// ============================================================================

// RequestWithdrawal creates a withdrawal request
func (s *Service) RequestWithdrawal(customerID int, req *WithdrawRequest) (*Withdrawal, error) {
	// 2FA is mandatory for withdrawals
	if !s.Is2FASetup(customerID) {
		return nil, fmt.Errorf("2FA must be enabled before withdrawing - call /2fa/setup first")
	}

	if req.TOTPCode == "" {
		return nil, fmt.Errorf("totp_code is required for withdrawals")
	}

	// Verify 2FA code
	valid, err := s.Verify2FACode(customerID, req.TOTPCode)
	if err != nil {
		return nil, fmt.Errorf("2FA verification failed: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("invalid 2FA code")
	}

	// Get settings
	minWithdrawal := s.getSetting("min_withdrawal_sat", 50000)
	maxWithdrawal := s.getSetting("max_withdrawal_sat", 10000000)
	withdrawalFee := s.getSetting("withdrawal_fee_sat", 1000)

	if !s.isWithdrawalsEnabled() {
		return nil, fmt.Errorf("withdrawals are temporarily disabled")
	}

	// Check for existing pending withdrawals (limit to 3 pending per customer)
	var pendingCount int
	s.db.QueryRow(`
		SELECT COUNT(*) FROM rental_withdrawals
		WHERE customer_id = $1 AND status IN ('pending', 'approved')
	`, customerID).Scan(&pendingCount)
	if pendingCount >= 3 {
		return nil, fmt.Errorf("you have %d pending withdrawals - please wait for them to be processed", pendingCount)
	}

	// Validate address format
	if req.BTCAddress == "" {
		return nil, fmt.Errorf("BTC address is required")
	}
	if len(req.BTCAddress) < 26 || len(req.BTCAddress) > 90 {
		return nil, fmt.Errorf("invalid BTC address format")
	}
	// Basic format validation with full checksum verification
	if !isValidBTCAddressFormat(req.BTCAddress) {
		return nil, fmt.Errorf("invalid BTC address - please verify the address is correct")
	}

	// Opt-in withdrawal address lock: once a customer has at least one effectively
	// verified whitelisted address (explicitly verified, or added more than 24h
	// ago), restrict withdrawals to their whitelisted addresses. Customers with no
	// such entry are unaffected, so this is opt-in and strands no one.
	var activeWhitelistCount int
	s.db.QueryRow(`
		SELECT COUNT(*) FROM rental_withdrawal_whitelist
		WHERE customer_id = $1 AND (verified = TRUE OR created_at <= NOW() - INTERVAL '24 hours')
	`, customerID).Scan(&activeWhitelistCount)
	if activeWhitelistCount > 0 {
		allowed, err := s.IsAddressWhitelisted(customerID, req.BTCAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to check withdrawal whitelist: %w", err)
		}
		if !allowed {
			return nil, fmt.Errorf("this address is not in your verified withdrawal whitelist")
		}
	}

	// Start SERIALIZABLE transaction for financial operation
	tx, cancel, err := s.beginFinancialTx()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer cancel()
	defer tx.Rollback()

	// Lock the customer row first to serialize operations
	var customerExists bool
	err = tx.QueryRow(`
		SELECT TRUE FROM rental_customers WHERE id = $1 FOR UPDATE
	`, customerID).Scan(&customerExists)
	if err != nil {
		return nil, fmt.Errorf("failed to lock customer: %w", err)
	}

	// Now safely calculate balance
	var balanceSat int64
	err = tx.QueryRow(`
		SELECT COALESCE(SUM(amount_sat), 0)
		FROM rental_ledger
		WHERE customer_id = $1
	`, customerID).Scan(&balanceSat)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	// Determine withdrawal amount
	amount := req.AmountSat
	if amount == 0 {
		// Withdraw all - ensure we don't go negative
		if balanceSat <= withdrawalFee {
			return nil, fmt.Errorf("insufficient balance for withdrawal: need at least %d sats (fee) plus minimum withdrawal", withdrawalFee)
		}
		amount = balanceSat - withdrawalFee
	}

	if amount < minWithdrawal {
		return nil, fmt.Errorf("minimum withdrawal is %d sats", minWithdrawal)
	}
	if amount > maxWithdrawal {
		return nil, fmt.Errorf("maximum withdrawal is %d sats", maxWithdrawal)
	}

	totalDeduction := amount + withdrawalFee
	if balanceSat < totalDeduction {
		return nil, fmt.Errorf("insufficient balance: have %d sats, need %d sats (including %d sat fee)",
			balanceSat, totalDeduction, withdrawalFee)
	}

	// Create withdrawal request
	var withdrawalID int
	err = tx.QueryRow(`
		INSERT INTO rental_withdrawals (customer_id, amount_sat, btc_address, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id
	`, customerID, amount, req.BTCAddress).Scan(&withdrawalID)
	if err != nil {
		return nil, fmt.Errorf("failed to create withdrawal request: %w", err)
	}

	// Deduct from balance (reserve funds including fee)
	_, err = tx.Exec(`
		INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, memo)
		VALUES ($1, 'withdrawal', $2, $3)
	`, customerID, -totalDeduction, fmt.Sprintf("Withdrawal request #%d (including %d sat fee)", withdrawalID, withdrawalFee))
	if err != nil {
		return nil, fmt.Errorf("failed to reserve funds: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	withdrawal := &Withdrawal{
		ID:         withdrawalID,
		CustomerID: customerID,
		AmountSat:  amount,
		BTCAddress: req.BTCAddress,
		Status:     WithdrawalStatusPending,
	}

	// Log withdrawal request for audit trail
	s.LogSecurityEvent(customerID, "withdrawal_request", "", "", map[string]interface{}{
		"withdrawal_id": withdrawalID,
		"amount_sat":    amount,
		"btc_address":   req.BTCAddress,
		"fee_sat":       withdrawalFee,
	})

	log.Printf("Withdrawal request #%d created: %d sats to %s", withdrawalID, amount, req.BTCAddress)
	return withdrawal, nil
}

// isValidBTCAddressFormat validates BTC address with full checksum verification
// Uses btcutil to decode and validate the address, preventing typos from causing fund loss
func isValidBTCAddressFormat(addr string) bool {
	// Use btcutil to decode and validate the address
	// This verifies the checksum for both Base58Check and Bech32 addresses
	_, err := btcutil.DecodeAddress(addr, &chaincfg.MainNetParams)
	return err == nil
}

// Legacy validation functions kept for reference but no longer used
func isBase58(s string) bool {
	const base58Chars = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for _, c := range s {
		found := false
		for _, b := range base58Chars {
			if c == b {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func isBech32(s string) bool {
	const bech32Chars = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	for _, c := range s {
		found := false
		for _, b := range bech32Chars {
			if c == b {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// GetWithdrawals returns customer's withdrawal history
func (s *Service) GetWithdrawals(customerID int) ([]Withdrawal, error) {
	rows, err := s.db.Query(`
		SELECT id, customer_id, amount_sat, btc_address, status,
		       COALESCE(txid, ''), created_at, processed_at,
		       COALESCE(processed_by, ''), COALESCE(rejection_reason, '')
		FROM rental_withdrawals
		WHERE customer_id = $1
		ORDER BY created_at DESC
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	var withdrawals []Withdrawal
	for rows.Next() {
		var w Withdrawal
		err := rows.Scan(
			&w.ID, &w.CustomerID, &w.AmountSat, &w.BTCAddress, &w.Status,
			&w.TxID, &w.CreatedAt, &w.ProcessedAt,
			&w.ProcessedBy, &w.RejectionReason,
		)
		if err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		withdrawals = append(withdrawals, w)
	}

	return withdrawals, nil
}

// GetWithdrawal returns a single withdrawal
func (s *Service) GetWithdrawal(customerID, withdrawalID int) (*Withdrawal, error) {
	var w Withdrawal
	err := s.db.QueryRow(`
		SELECT id, customer_id, amount_sat, btc_address, status,
		       COALESCE(txid, ''), created_at, processed_at,
		       COALESCE(processed_by, ''), COALESCE(rejection_reason, '')
		FROM rental_withdrawals
		WHERE id = $1 AND customer_id = $2
	`, withdrawalID, customerID).Scan(
		&w.ID, &w.CustomerID, &w.AmountSat, &w.BTCAddress, &w.Status,
		&w.TxID, &w.CreatedAt, &w.ProcessedAt,
		&w.ProcessedBy, &w.RejectionReason,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("withdrawal not found")
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	return &w, nil
}

// ============================================================================
// Admin Methods
// ============================================================================

// HashAPIKey hashes an API key using SHA256
// API keys are stored as hashes to prevent exposure if database is compromised
func HashAPIKey(apiKey string) string {
	hash := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(hash[:])
}

// GetAdminByAPIKey looks up an admin by their API key
// The API key is hashed before lookup for security
func (s *Service) GetAdminByAPIKey(apiKey string) (*Admin, error) {
	// Hash the provided API key for comparison
	apiKeyHash := HashAPIKey(apiKey)

	var a Admin
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, api_key, role, created_at, last_login
		FROM rental_admins
		WHERE api_key = $1
	`, apiKeyHash).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.APIKey, &a.Role, &a.CreatedAt, &a.LastLogin)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("invalid admin API key")
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	return &a, nil
}

// GetSystemStats returns system-wide statistics
func (s *Service) GetSystemStats() (*SystemStats, error) {
	stats := &SystemStats{}

	// Total customers
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_customers`).Scan(&stats.TotalCustomers)

	// Total deposits
	s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(amount_sat), 0) FROM rental_deposits WHERE credited = TRUE`).
		Scan(&stats.TotalDeposits, &stats.TotalDepositsSat)

	// Orders
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_orders`).Scan(&stats.TotalOrders)
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_orders WHERE status = 'active'`).Scan(&stats.ActiveOrders)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount_spent_sat), 0) FROM rental_orders`).Scan(&stats.TotalSpentSat)
	s.db.QueryRow(`SELECT COALESCE(SUM(speed_limit_ph), 0) FROM rental_orders WHERE status = 'active'`).Scan(&stats.TotalActiveHashrate)

	// Withdrawals
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_withdrawals WHERE status = 'pending'`).Scan(&stats.PendingWithdrawals)
	s.db.QueryRow(`SELECT COALESCE(SUM(amount_sat), 0) FROM rental_withdrawals WHERE status = 'completed'`).
		Scan(&stats.TotalWithdrawnSat)

	return stats, nil
}

// GetAdminStats is an alias for GetSystemStats (for web handler compatibility)
func (s *Service) GetAdminStats() (*SystemStats, error) {
	return s.GetSystemStats()
}

// CheckDBHealth verifies database connectivity
// SignerHealthy reports whether the remote signing service is reachable. Used as
// a pre-flight so the batch withdrawal processor does not claim rows (moving them
// to 'processing') when the signer is down, which would strand the queue.
func (s *Service) SignerHealthy() bool {
	if s.remoteSigner == nil {
		return false
	}
	return s.remoteSigner.HealthCheck() == nil
}

func (s *Service) CheckDBHealth() bool {
	err := s.db.Ping()
	return err == nil
}

// CheckBraiinsHealth verifies Braiins API connectivity
func (s *Service) CheckBraiinsHealth() bool {
	_, err := s.braiins.GetBalance()
	return err == nil
}

// AdminListCustomers returns all customers with balances
func (s *Service) AdminListCustomers(limit, offset int) (*CustomerListResponse, error) {
	if limit <= 0 {
		limit = 50
	}

	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_customers`).Scan(&total)

	rows, err := s.db.Query(`
		SELECT c.id, c.api_key, c.email, c.created_at,
		       COALESCE(SUM(l.amount_sat), 0) as balance,
		       COALESCE(a.btc_address, '') as btc_address
		FROM rental_customers c
		LEFT JOIN rental_ledger l ON l.customer_id = c.id
		LEFT JOIN rental_addresses a ON a.customer_id = c.id
		GROUP BY c.id, a.btc_address
		ORDER BY c.created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	var customers []CustomerWithBalance
	for rows.Next() {
		var c CustomerWithBalance
		err := rows.Scan(&c.ID, &c.APIKey, &c.Email, &c.CreatedAt, &c.BalanceSat, &c.BTCAddress)
		if err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		customers = append(customers, c)
	}

	return &CustomerListResponse{
		Customers: customers,
		Total:     total,
	}, nil
}

// AdminListOrders returns all orders
func (s *Service) AdminListOrders(status string, limit, offset int) (*OrderListResponse, error) {
	if limit <= 0 {
		limit = 50
	}

	var total int
	var rows *sql.Rows
	var err error

	if status != "" {
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_orders WHERE status = $1`, status).Scan(&total)
		rows, err = s.db.Query(`
			SELECT id, customer_id, COALESCE(braiins_bid_id, ''), target_pool_url, target_identity,
			       budget_sat, price_sat, speed_limit_ph, margin_pct, status,
			       amount_spent_sat, COALESCE(error_message, ''), created_at, started_at, completed_at
			FROM rental_orders
			WHERE status = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		`, status, limit, offset)
	} else {
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_orders`).Scan(&total)
		rows, err = s.db.Query(`
			SELECT id, customer_id, COALESCE(braiins_bid_id, ''), target_pool_url, target_identity,
			       budget_sat, price_sat, speed_limit_ph, margin_pct, status,
			       amount_spent_sat, COALESCE(error_message, ''), created_at, started_at, completed_at
			FROM rental_orders
			ORDER BY created_at DESC
			LIMIT $1 OFFSET $2
		`, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		err := rows.Scan(
			&o.ID, &o.CustomerID, &o.BraiinsBidID, &o.TargetPoolURL, &o.TargetIdentity,
			&o.BudgetSat, &o.PriceSat, &o.SpeedLimitPH, &o.MarginPct, &o.Status,
			&o.AmountSpentSat, &o.ErrorMessage, &o.CreatedAt, &o.StartedAt, &o.CompletedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		orders = append(orders, o)
	}

	return &OrderListResponse{
		Orders: orders,
		Total:  total,
	}, nil
}

// AdminListWithdrawals returns all withdrawals
func (s *Service) AdminListWithdrawals(status string, limit, offset int) (*WithdrawalListResponse, error) {
	if limit <= 0 {
		limit = 50
	}

	var total int
	var rows *sql.Rows
	var err error

	if status != "" {
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_withdrawals WHERE status = $1`, status).Scan(&total)
		rows, err = s.db.Query(`
			SELECT id, customer_id, amount_sat, btc_address, status,
			       COALESCE(txid, ''), created_at, processed_at,
			       COALESCE(processed_by, ''), COALESCE(rejection_reason, '')
			FROM rental_withdrawals
			WHERE status = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		`, status, limit, offset)
	} else {
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_withdrawals`).Scan(&total)
		rows, err = s.db.Query(`
			SELECT id, customer_id, amount_sat, btc_address, status,
			       COALESCE(txid, ''), created_at, processed_at,
			       COALESCE(processed_by, ''), COALESCE(rejection_reason, '')
			FROM rental_withdrawals
			ORDER BY created_at DESC
			LIMIT $1 OFFSET $2
		`, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	var withdrawals []Withdrawal
	for rows.Next() {
		var w Withdrawal
		err := rows.Scan(
			&w.ID, &w.CustomerID, &w.AmountSat, &w.BTCAddress, &w.Status,
			&w.TxID, &w.CreatedAt, &w.ProcessedAt,
			&w.ProcessedBy, &w.RejectionReason,
		)
		if err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		withdrawals = append(withdrawals, w)
	}

	return &WithdrawalListResponse{
		Withdrawals: withdrawals,
		Total:       total,
	}, nil
}

// AdminApproveWithdrawal marks a withdrawal as approved (ready for manual processing)
func (s *Service) AdminApproveWithdrawal(adminUsername string, withdrawalID int) error {
	result, err := s.db.Exec(`
		UPDATE rental_withdrawals
		SET status = 'approved', processed_by = $1, processed_at = NOW()
		WHERE id = $2 AND status = 'pending'
	`, adminUsername, withdrawalID)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("withdrawal not found or not pending")
	}

	s.logAuditAction(adminUsername, "withdrawal_approved", "withdrawal", withdrawalID, "")
	log.Printf("Withdrawal #%d approved by %s", withdrawalID, adminUsername)
	return nil
}

// AdminCompleteWithdrawal marks withdrawal as completed with txid
func (s *Service) AdminCompleteWithdrawal(adminUsername string, withdrawalID int, txid string) error {
	if txid == "" {
		return fmt.Errorf("txid is required")
	}

	// Get withdrawal details for email notification
	var customerID int
	var amountSat int64
	var btcAddress string
	err := s.db.QueryRow(`
		SELECT customer_id, amount_sat, btc_address
		FROM rental_withdrawals
		WHERE id = $1 AND status IN ('pending', 'approved', 'processing')
	`, withdrawalID).Scan(&customerID, &amountSat, &btcAddress)
	if err != nil {
		return fmt.Errorf("withdrawal not found or already processed")
	}

	result, err := s.db.Exec(`
		UPDATE rental_withdrawals
		SET status = 'completed', txid = $1, processed_by = $2, processed_at = NOW()
		WHERE id = $3 AND status IN ('pending', 'approved', 'processing')
	`, txid, adminUsername, withdrawalID)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("withdrawal not found or already processed")
	}

	s.logAuditAction(adminUsername, "withdrawal_completed", "withdrawal", withdrawalID, txid)
	log.Printf("Withdrawal #%d completed by %s (txid: %s)", withdrawalID, adminUsername, txid)

	// Send withdrawal processed email
	go s.sendWithdrawalProcessedEmail(customerID, amountSat, btcAddress, txid)

	return nil
}

// sendWithdrawalProcessedEmail sends withdrawal processed notification (fire-and-forget)
func (s *Service) sendWithdrawalProcessedEmail(customerID int, amountSat int64, btcAddress, txid string) {
	amountBTC := fmt.Sprintf("%.8f", float64(amountSat)/100000000)

	// Create in-app notification
	s.CreateNotification(customerID, "withdrawal", "Withdrawal Sent",
		fmt.Sprintf("Your withdrawal of %s BTC has been sent.", amountBTC), "/transactions")

	// Send email if preferences allow
	if s.email == nil || !s.ShouldSendEmail(customerID, "withdrawal") {
		return
	}
	customer, err := s.GetCustomerByID(customerID)
	if err != nil || customer.Email == "" {
		return
	}
	if err := s.email.SendWithdrawalProcessedEmail(customer.Email, amountBTC, btcAddress, txid); err != nil {
		log.Printf("Failed to send withdrawal email to %s: %v", customer.Email, err)
	}
}

// NotifyWithdrawalCompleted sends the same in-app notification + (preference-
// gated) email as the admin completion path, for the automated 'system'
// completion path in cmd/process-withdrawals (which previously completed
// silently -> "where's my withdrawal" tickets). Synchronous by design: the
// caller is a short-lived batch job that must not exit before the send.
func (s *Service) NotifyWithdrawalCompleted(customerID int, amountSat int64, btcAddress, txid string) {
	s.sendWithdrawalProcessedEmail(customerID, amountSat, btcAddress, txid)
}

// AdminRejectWithdrawal rejects a withdrawal and refunds the customer
func (s *Service) AdminRejectWithdrawal(adminUsername string, withdrawalID int, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get withdrawal details
	var customerID int
	var amountSat int64
	var status string
	err = tx.QueryRow(`
		SELECT customer_id, amount_sat, status
		FROM rental_withdrawals
		WHERE id = $1
		FOR UPDATE
	`, withdrawalID).Scan(&customerID, &amountSat, &status)
	if err != nil {
		return fmt.Errorf("withdrawal not found: %w", err)
	}

	// 'processing' rows are stuck mid-send; an admin may reconcile them here after
	// confirming on-chain that no broadcast occurred (else use AdminComplete).
	if status != "pending" && status != "approved" && status != "processing" {
		return fmt.Errorf("withdrawal cannot be rejected (status: %s)", status)
	}

	// Update withdrawal status
	_, err = tx.Exec(`
		UPDATE rental_withdrawals
		SET status = 'rejected', rejection_reason = $1, processed_by = $2, processed_at = NOW()
		WHERE id = $3
	`, reason, adminUsername, withdrawalID)
	if err != nil {
		return fmt.Errorf("failed to update withdrawal: %w", err)
	}

	// Refund to customer (amount + fee)
	withdrawalFee := s.getSetting("withdrawal_fee_sat", 1000)
	refundAmount := amountSat + withdrawalFee

	_, err = tx.Exec(`
		INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, memo)
		VALUES ($1, 'deposit', $2, $3)
	`, customerID, refundAmount, fmt.Sprintf("Withdrawal #%d rejected - refund", withdrawalID))
	if err != nil {
		return fmt.Errorf("failed to refund: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	s.logAuditAction(adminUsername, "withdrawal_rejected", "withdrawal", withdrawalID, reason)
	log.Printf("Withdrawal #%d rejected by %s: %s (refunded %d sats)", withdrawalID, adminUsername, reason, refundAmount)

	// Send withdrawal rejected email
	go s.sendWithdrawalRejectedEmail(customerID, amountSat, reason)

	return nil
}

// sendWithdrawalRejectedEmail sends withdrawal rejected notification (fire-and-forget)
func (s *Service) sendWithdrawalRejectedEmail(customerID int, amountSat int64, reason string) {
	amountBTC := fmt.Sprintf("%.8f", float64(amountSat)/100000000)

	// Create in-app notification
	s.CreateNotification(customerID, "withdrawal", "Withdrawal Rejected",
		fmt.Sprintf("Your withdrawal of %s BTC was rejected: %s. Funds returned to balance.", amountBTC, reason),
		"/dashboard")

	// Send email if preferences allow
	if s.email == nil || !s.ShouldSendEmail(customerID, "withdrawal") {
		return
	}
	customer, err := s.GetCustomerByID(customerID)
	if err != nil || customer.Email == "" {
		return
	}
	if err := s.email.SendWithdrawalRejectedEmail(customer.Email, amountBTC, reason); err != nil {
		log.Printf("Failed to send withdrawal rejected email to %s: %v", customer.Email, err)
	}
}

// AdminCancelOrder cancels any order (admin override)
func (s *Service) AdminCancelOrder(adminUsername string, orderID int) error {
	// Start transaction to prevent race conditions
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Lock the order row
	var order Order
	var braiinsBidID sql.NullString
	err = tx.QueryRow(`
		SELECT id, customer_id, braiins_bid_id, budget_sat, amount_spent_sat, status, mining_mode, target_identity
		FROM rental_orders
		WHERE id = $1
		FOR UPDATE
	`, orderID).Scan(
		&order.ID, &order.CustomerID, &braiinsBidID, &order.BudgetSat, &order.AmountSpentSat, &order.Status,
		&order.MiningMode, &order.TargetIdentity,
	)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}
	if braiinsBidID.Valid {
		order.BraiinsBidID = braiinsBidID.String
	}

	if order.Status != OrderStatusActive && order.Status != OrderStatusPending {
		return fmt.Errorf("order cannot be cancelled (status: %s)", order.Status)
	}

	// Cancel on Braiins if active
	if order.BraiinsBidID != "" {
		bid, _ := s.braiins.GetBidDetail(order.BraiinsBidID)
		if bid != nil {
			order.AmountSpentSat = bid.AmountSpentSat
		}
		s.braiins.CancelBid(order.BraiinsBidID)
	}

	// Update order status within transaction
	_, err = tx.Exec(`
		UPDATE rental_orders
		SET status = $1, error_message = $2, completed_at = NOW(), amount_spent_sat = $3
		WHERE id = $4
	`, OrderStatusCancelled, "Cancelled by admin: "+adminUsername, order.AmountSpentSat, orderID)
	if err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}

	// Refund unused amount within transaction
	refundAmount := order.BudgetSat - order.AmountSpentSat
	if refundAmount > 0 {
		// Guard against double-refund (every other refund path checks this): a prior
		// failure or cancellation may already have refunded this order.
		var refundExists bool
		if err = tx.QueryRow(`
			SELECT EXISTS(SELECT 1 FROM rental_ledger WHERE order_id = $1 AND tx_type = 'order_refund')
		`, orderID).Scan(&refundExists); err != nil {
			return fmt.Errorf("failed to check existing refund: %w", err)
		}
		if !refundExists {
			_, err = tx.Exec(`
				INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, order_id, memo)
				VALUES ($1, 'order_refund', $2, $3, 'Admin cancelled - refund')
			`, order.CustomerID, refundAmount, orderID)
			if err != nil {
				return fmt.Errorf("failed to refund: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	// Disable solo mining mode on the pool for cancelled solo orders
	if order.MiningMode == "solo" {
		if err := s.setMinerSoloMode(order.TargetIdentity, false); err != nil {
			log.Printf("Warning: failed to disable solo mode for admin-cancelled order %d: %v", orderID, err)
		}
	}

	s.logAuditAction(adminUsername, "order_cancelled", "order", orderID, "")
	log.Printf("Order #%d cancelled by admin %s, refunded %d sats", orderID, adminUsername, refundAmount)
	return nil
}

// ============================================================================
// Settings Methods
// ============================================================================

// getSetting retrieves a setting value
func (s *Service) getSetting(key string, defaultVal int64) int64 {
	var value string
	err := s.db.QueryRow(`SELECT value FROM rental_settings WHERE key = $1`, key).Scan(&value)
	if err != nil {
		return defaultVal
	}
	var v int64
	n, err := fmt.Sscanf(value, "%d", &v)
	if err != nil || n != 1 {
		log.Printf("WARNING: Invalid setting %s='%s', using default %d", key, value, defaultVal)
		return defaultVal
	}
	// Sanity check: settings should be non-negative for amounts/limits
	if v < 0 {
		log.Printf("WARNING: Negative setting %s=%d, using default %d", key, v, defaultVal)
		return defaultVal
	}
	return v
}

// isWithdrawalsEnabled checks if withdrawals are enabled
func (s *Service) isWithdrawalsEnabled() bool {
	var value string
	err := s.db.QueryRow(`SELECT value FROM rental_settings WHERE key = 'withdrawals_enabled'`).Scan(&value)
	if err != nil {
		return true
	}
	return value == "true"
}

// AdminGetSettings returns all settings
func (s *Service) AdminGetSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM rental_settings`)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		settings[key] = value
	}

	return settings, nil
}

// AdminUpdateSetting updates a setting
func (s *Service) AdminUpdateSetting(adminUsername, key, value string) error {
	result, err := s.db.Exec(`
		UPDATE rental_settings SET value = $1, updated_at = NOW() WHERE key = $2
	`, value, key)
	if err != nil {
		return fmt.Errorf("database error: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("setting not found: %s", key)
	}

	s.logAuditAction(adminUsername, "setting_updated", "setting", 0, fmt.Sprintf("%s=%s", key, value))
	log.Printf("Setting %s updated to %s by %s", key, value, adminUsername)
	return nil
}

// logAuditAction logs an admin action
func (s *Service) logAuditAction(adminUsername, action, entityType string, entityID int, details string) {
	s.db.Exec(`
		INSERT INTO rental_audit_log (admin_id, action, entity_type, entity_id, details)
		SELECT id, $2, $3, $4, $5 FROM rental_admins WHERE username = $1
	`, adminUsername, action, entityType, entityID, details)
}

// LogSecurityEvent logs a customer security event for audit trail
// Event types: login_success, login_failed, logout, password_reset_request, password_reset_complete,
// 2fa_enabled, 2fa_disabled, 2fa_backup_used, withdrawal_request, api_key_regenerated, session_created
func (s *Service) LogSecurityEvent(customerID int, eventType, ipAddress, userAgent string, details map[string]interface{}) {
	detailsJSON := "{}"
	if details != nil {
		if jsonBytes, err := json.Marshal(details); err == nil {
			detailsJSON = string(jsonBytes)
		}
	}

	_, err := s.db.Exec(`
		INSERT INTO rental_security_log (customer_id, event_type, ip_address, user_agent, details)
		VALUES ($1, $2, $3, $4, $5::jsonb)
	`, customerID, eventType, ipAddress, userAgent, detailsJSON)
	if err != nil {
		log.Printf("Failed to log security event %s for customer %d: %v", eventType, customerID, err)
	}
}

// CreateAdmin creates a new admin user (for initial setup)
func (s *Service) CreateAdmin(username, passwordHash, role string) (*Admin, error) {
	apiKey, err := generateAPIKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate API key: %w", err)
	}

	var adminID int
	err = s.db.QueryRow(`
		INSERT INTO rental_admins (username, password_hash, api_key, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, username, passwordHash, HashAPIKey(apiKey), role).Scan(&adminID)
	if err != nil {
		return nil, fmt.Errorf("failed to create admin: %w", err)
	}

	return &Admin{
		ID:       adminID,
		Username: username,
		APIKey:   apiKey,
		Role:     role,
	}, nil
}

// ============================================================================
// Transaction History
// ============================================================================

// GetTransactionHistory returns customer's ledger entries
func (s *Service) GetTransactionHistory(customerID int, limit, offset int) ([]LedgerEntry, int, error) {
	if limit <= 0 {
		limit = 50
	}

	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_ledger WHERE customer_id = $1`, customerID).Scan(&total)

	rows, err := s.db.Query(`
		SELECT id, customer_id, tx_type, amount_sat,
		       COALESCE(btc_txid, ''), order_id, COALESCE(memo, ''), created_at
		FROM rental_ledger
		WHERE customer_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, customerID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		err := rows.Scan(
			&e.ID, &e.CustomerID, &e.TxType, &e.AmountSat,
			&e.BTCTxID, &e.OrderID, &e.Memo, &e.CreatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scan error: %w", err)
		}
		entries = append(entries, e)
	}

	return entries, total, nil
}

// GetDeposits returns customer's deposit history
func (s *Service) GetDeposits(customerID int) ([]Deposit, error) {
	rows, err := s.db.Query(`
		SELECT id, btc_txid, btc_address, amount_sat, confirmations, credited, customer_id, created_at, confirmed_at
		FROM rental_deposits
		WHERE customer_id = $1
		ORDER BY created_at DESC
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	defer rows.Close()

	var deposits []Deposit
	for rows.Next() {
		var d Deposit
		err := rows.Scan(
			&d.ID, &d.BTCTxID, &d.BTCAddress, &d.AmountSat, &d.Confirmations,
			&d.Credited, &d.CustomerID, &d.CreatedAt, &d.ConfirmedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		deposits = append(deposits, d)
	}

	return deposits, nil
}

// GetRequiredConfirmations returns the number of confirmations required for deposits
func (s *Service) GetRequiredConfirmations() int {
	return s.config.RequiredConfirms
}

// ============================================================================
// Rate Limiting
// ============================================================================

// CheckRateLimit checks if an identifier has exceeded rate limits
// Returns true if allowed, false if rate limited
// Uses atomic INSERT ... ON CONFLICT to prevent race conditions
func (s *Service) CheckRateLimit(identifier, endpoint string, maxRequests int, windowSeconds int) bool {
	now := time.Now()
	windowStart := now.Add(-time.Duration(windowSeconds) * time.Second)

	// Periodically clean up old entries (1% of requests)
	// This is separate from the main check to avoid locking overhead
	if now.UnixNano()%100 == 0 {
		s.db.Exec(`DELETE FROM rental_rate_limits WHERE window_start < $1`, windowStart)
	}

	// Atomic upsert: insert new entry or increment existing, then check limit
	// This single statement is atomic and race-condition free
	var newCount int
	err := s.db.QueryRow(`
		INSERT INTO rental_rate_limits (identifier, endpoint, request_count, window_start)
		VALUES ($1, $2, 1, $3)
		ON CONFLICT (identifier, endpoint) DO UPDATE
		SET request_count = CASE
			WHEN rental_rate_limits.window_start < $4 THEN 1
			ELSE rental_rate_limits.request_count + 1
		END,
		window_start = CASE
			WHEN rental_rate_limits.window_start < $4 THEN $3
			ELSE rental_rate_limits.window_start
		END
		RETURNING request_count
	`, identifier, endpoint, now, windowStart).Scan(&newCount)

	if err != nil {
		log.Printf("Rate limit check failed: %v", err)
		return true // Allow on error
	}

	return newCount <= maxRequests
}

// ============================================================================
// Two-Factor Authentication
// ============================================================================

// Setup2FA generates a TOTP secret for a customer
func (s *Service) Setup2FA(customerID int, email string) (*TwoFactorSetupResponse, error) {
	// Check if already enabled
	var enabled bool
	err := s.db.QueryRow(`
		SELECT COALESCE(totp_enabled, FALSE) FROM rental_customers WHERE id = $1
	`, customerID).Scan(&enabled)
	if err != nil {
		return nil, fmt.Errorf("customer not found: %w", err)
	}
	if enabled {
		return nil, fmt.Errorf("2FA is already enabled")
	}

	// Generate new secret
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return nil, fmt.Errorf("failed to generate secret: %w", err)
	}

	// Encrypt secret for storage (keeps plaintext for QR code)
	encryptedSecret, err := EncryptTOTPSecret(secret)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt secret: %w", err)
	}

	// Store encrypted secret (not enabled yet until verified)
	_, err = s.db.Exec(`
		UPDATE rental_customers SET totp_secret = $1 WHERE id = $2
	`, encryptedSecret, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to store secret: %w", err)
	}

	// Generate URI for QR code (using plaintext secret)
	uri := GenerateTOTPURI(secret, email, "ForgePool")

	// Generate one preview backup code
	codes, err := GenerateBackupCodes(1)
	if err != nil {
		return nil, fmt.Errorf("failed to generate backup code: %w", err)
	}

	return &TwoFactorSetupResponse{
		Secret:     secret,
		QRCodeURI:  uri,
		BackupCode: codes[0],
	}, nil
}

// Verify2FA verifies a TOTP code and enables 2FA
func (s *Service) Verify2FA(customerID int, code string) error {
	// Get the stored secret
	var encryptedSecret sql.NullString
	var enabled bool
	err := s.db.QueryRow(`
		SELECT totp_secret, COALESCE(totp_enabled, FALSE)
		FROM rental_customers WHERE id = $1
	`, customerID).Scan(&encryptedSecret, &enabled)
	if err != nil {
		return fmt.Errorf("customer not found: %w", err)
	}
	if enabled {
		return fmt.Errorf("2FA is already enabled")
	}
	if !encryptedSecret.Valid || encryptedSecret.String == "" {
		return fmt.Errorf("2FA setup not started - call setup first")
	}

	// Decrypt the secret
	secret, err := DecryptTOTPSecret(encryptedSecret.String)
	if err != nil {
		return fmt.Errorf("failed to decrypt secret: %w", err)
	}

	// Validate the code
	if !ValidateTOTPCode(secret, code) {
		return fmt.Errorf("invalid verification code")
	}

	// Enable 2FA
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		UPDATE rental_customers
		SET totp_enabled = TRUE, totp_verified_at = NOW()
		WHERE id = $1
	`, customerID)
	if err != nil {
		return fmt.Errorf("failed to enable 2FA: %w", err)
	}

	// Generate and store backup codes
	codes, err := GenerateBackupCodes(backupCodeCount)
	if err != nil {
		return fmt.Errorf("failed to generate backup codes: %w", err)
	}

	for _, code := range codes {
		hash := HashBackupCode(code)
		_, err = tx.Exec(`
			INSERT INTO rental_backup_codes (customer_id, code_hash)
			VALUES ($1, $2)
		`, customerID, hash)
		if err != nil {
			return fmt.Errorf("failed to store backup code: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	// Log 2FA enabled event
	s.LogSecurityEvent(customerID, "2fa_enabled", "", "", nil)
	log.Printf("2FA enabled for customer %d", customerID)
	return nil
}

// Get2FAStatus returns the 2FA status for a customer
func (s *Service) Get2FAStatus(customerID int) (*TwoFactorStatusResponse, error) {
	var enabled bool
	var verifiedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT COALESCE(totp_enabled, FALSE), totp_verified_at
		FROM rental_customers WHERE id = $1
	`, customerID).Scan(&enabled, &verifiedAt)
	if err != nil {
		return nil, fmt.Errorf("customer not found: %w", err)
	}

	// Count remaining backup codes
	var remaining int
	s.db.QueryRow(`
		SELECT COUNT(*) FROM rental_backup_codes
		WHERE customer_id = $1 AND used = FALSE
	`, customerID).Scan(&remaining)

	resp := &TwoFactorStatusResponse{
		Enabled:     enabled,
		BackupCodes: remaining,
	}
	if verifiedAt.Valid {
		resp.VerifiedAt = &verifiedAt.Time
	}

	return resp, nil
}

// Disable2FA disables 2FA for a customer (requires valid code)
func (s *Service) Disable2FA(customerID int, code string) error {
	// Verify the code first
	valid, err := s.Verify2FACode(customerID, code)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("invalid verification code")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Disable 2FA
	_, err = tx.Exec(`
		UPDATE rental_customers
		SET totp_enabled = FALSE, totp_secret = NULL, totp_verified_at = NULL
		WHERE id = $1
	`, customerID)
	if err != nil {
		return fmt.Errorf("failed to disable 2FA: %w", err)
	}

	// Delete all backup codes
	_, err = tx.Exec(`
		DELETE FROM rental_backup_codes WHERE customer_id = $1
	`, customerID)
	if err != nil {
		return fmt.Errorf("failed to delete backup codes: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	// Log 2FA disabled event
	s.LogSecurityEvent(customerID, "2fa_disabled", "", "", nil)
	log.Printf("2FA disabled for customer %d", customerID)
	return nil
}

// RegenerateBackupCodes generates new backup codes (invalidates old ones)
// Has a 1-hour cooldown between regenerations to prevent abuse
func (s *Service) RegenerateBackupCodes(customerID int, code string) (*BackupCodesResponse, error) {
	// Check cooldown - only allow 1 regeneration per hour
	cooldownKey := fmt.Sprintf("backup_regen:%d", customerID)
	if !s.CheckRateLimit(cooldownKey, "backup_regen", 1, 3600) {
		return nil, fmt.Errorf("backup codes can only be regenerated once per hour")
	}

	// Verify 2FA is enabled
	var enabled bool
	err := s.db.QueryRow(`
		SELECT COALESCE(totp_enabled, FALSE) FROM rental_customers WHERE id = $1
	`, customerID).Scan(&enabled)
	if err != nil {
		return nil, fmt.Errorf("customer not found: %w", err)
	}
	if !enabled {
		return nil, fmt.Errorf("2FA is not enabled")
	}

	// Verify the provided code
	valid, err := s.Verify2FACode(customerID, code)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, fmt.Errorf("invalid verification code")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete old backup codes
	_, err = tx.Exec(`DELETE FROM rental_backup_codes WHERE customer_id = $1`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to delete old codes: %w", err)
	}

	// Generate new codes
	codes, err := GenerateBackupCodes(backupCodeCount)
	if err != nil {
		return nil, fmt.Errorf("failed to generate codes: %w", err)
	}

	for _, code := range codes {
		hash := HashBackupCode(code)
		_, err = tx.Exec(`
			INSERT INTO rental_backup_codes (customer_id, code_hash)
			VALUES ($1, $2)
		`, customerID, hash)
		if err != nil {
			return nil, fmt.Errorf("failed to store backup code: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	log.Printf("Backup codes regenerated for customer %d", customerID)
	return &BackupCodesResponse{Codes: codes}, nil
}

// Verify2FACode verifies a TOTP code or backup code
// Returns true if valid, also consumes backup code if used
// Includes lockout protection after repeated failures
func (s *Service) Verify2FACode(customerID int, code string) (bool, error) {
	// Check for 2FA lockout (5 failed attempts = 15 min lockout)
	lockKey := fmt.Sprintf("2fa_fail:%d", customerID)
	var failedAttempts int
	var lockedUntil sql.NullTime
	err := s.db.QueryRow(`
		SELECT request_count, locked_until FROM rental_rate_limits
		WHERE identifier = $1 AND endpoint = '2fa'
	`, lockKey).Scan(&failedAttempts, &lockedUntil)

	if err == nil && lockedUntil.Valid && lockedUntil.Time.After(time.Now()) {
		remainingMins := int(time.Until(lockedUntil.Time).Minutes()) + 1
		return false, fmt.Errorf("2FA temporarily locked due to failed attempts, try again in %d minutes", remainingMins)
	}

	// Get customer 2FA info
	var encryptedSecret sql.NullString
	var enabled bool
	err = s.db.QueryRow(`
		SELECT totp_secret, COALESCE(totp_enabled, FALSE)
		FROM rental_customers WHERE id = $1
	`, customerID).Scan(&encryptedSecret, &enabled)
	if err != nil {
		return false, fmt.Errorf("customer not found: %w", err)
	}
	if !enabled {
		return false, fmt.Errorf("2FA is not enabled")
	}

	// Try TOTP code first (6 digits)
	if len(code) == 6 && encryptedSecret.Valid {
		// Decrypt the secret for validation
		secret, err := DecryptTOTPSecret(encryptedSecret.String)
		if err != nil {
			return false, fmt.Errorf("failed to decrypt secret: %w", err)
		}
		if ValidateTOTPCode(secret, code) {
			// Clear failed attempts on success
			s.db.Exec(`DELETE FROM rental_rate_limits WHERE identifier = $1 AND endpoint = '2fa'`, lockKey)
			return true, nil
		}
	}

	// Try backup code (format: XXXX-XXXX-XXXX-XXXX-XXXX-XXXX or 24 hex chars)
	normalizedCode := strings.ToUpper(strings.ReplaceAll(code, "-", ""))
	if len(normalizedCode) == 24 {
		valid, err := s.tryBackupCode(customerID, code)
		if valid {
			// Clear failed attempts on success
			s.db.Exec(`DELETE FROM rental_rate_limits WHERE identifier = $1 AND endpoint = '2fa'`, lockKey)
		}
		return valid, err
	}

	// Invalid code - record failure and potentially lock
	s.record2FAFailure(lockKey, customerID)
	return false, nil
}

// record2FAFailure tracks failed 2FA attempts with exponential backoff
// Lockout durations: 5 fails = 5min, 10 = 15min, 15 = 1hr, 20+ = 4hr
func (s *Service) record2FAFailure(lockKey string, customerID int) {
	var currentCount int
	err := s.db.QueryRow(`
		INSERT INTO rental_rate_limits (identifier, endpoint, request_count, window_start)
		VALUES ($1, '2fa', 1, NOW())
		ON CONFLICT (identifier, endpoint) DO UPDATE
		SET request_count = rental_rate_limits.request_count + 1, window_start = NOW()
		RETURNING request_count
	`, lockKey).Scan(&currentCount)

	if err != nil {
		return
	}

	// Exponential backoff based on failure count
	var lockDuration time.Duration
	switch {
	case currentCount >= 20:
		lockDuration = 4 * time.Hour
	case currentCount >= 15:
		lockDuration = 1 * time.Hour
	case currentCount >= 10:
		lockDuration = 15 * time.Minute
	case currentCount >= 5:
		lockDuration = 5 * time.Minute
	default:
		return // No lock yet
	}

	// Lock the account for 2FA
	s.db.Exec(`
		UPDATE rental_rate_limits
		SET locked_until = $1
		WHERE identifier = $2 AND endpoint = '2fa'
	`, time.Now().Add(lockDuration), lockKey)

	s.LogSecurityEvent(customerID, "2fa_locked", "", "", map[string]interface{}{
		"failed_attempts": currentCount,
		"lock_duration":   lockDuration.String(),
	})
	log.Printf("2FA locked for %v due to %d failed attempts for customer %d", lockDuration, currentCount, customerID)
}

// tryBackupCode attempts to use a backup code
func (s *Service) tryBackupCode(customerID int, code string) (bool, error) {
	codeHash := HashBackupCode(code)

	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Find and lock matching unused code
	var codeID int
	err = tx.QueryRow(`
		SELECT id FROM rental_backup_codes
		WHERE customer_id = $1 AND code_hash = $2 AND used = FALSE
		FOR UPDATE
	`, customerID, codeHash).Scan(&codeID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Mark as used
	_, err = tx.Exec(`
		UPDATE rental_backup_codes
		SET used = TRUE, used_at = NOW()
		WHERE id = $1
	`, codeID)
	if err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}

	// Log backup code usage - this is a security-sensitive event
	s.LogSecurityEvent(customerID, "2fa_backup_used", "", "", map[string]interface{}{
		"code_id": codeID,
	})
	log.Printf("Backup code used for customer %d", customerID)
	return true, nil
}

// Is2FARequired checks if 2FA is required for a customer (always true once enabled)
func (s *Service) Is2FARequired(customerID int) bool {
	var enabled bool
	s.db.QueryRow(`
		SELECT COALESCE(totp_enabled, FALSE) FROM rental_customers WHERE id = $1
	`, customerID).Scan(&enabled)
	return enabled
}

// Is2FASetup checks if customer has 2FA set up
func (s *Service) Is2FASetup(customerID int) bool {
	var enabled bool
	s.db.QueryRow(`
		SELECT COALESCE(totp_enabled, FALSE) FROM rental_customers WHERE id = $1
	`, customerID).Scan(&enabled)
	return enabled
}

// ============================================================================
// Session Management (for login-based 2FA)
// ============================================================================

// CreateSession creates a new session for a customer
func (s *Service) CreateSession(customerID int, verified2FA bool, ipAddress string) (*Session, error) {
	token, err := GenerateSessionToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	expiresAt := time.Now().Add(24 * time.Hour) // 24 hour sessions

	var sessionID int
	err = s.db.QueryRow(`
		INSERT INTO rental_sessions (customer_id, session_token, verified_2fa, expires_at, ip_address)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, customerID, HashToken(token), verified2FA, expiresAt, ipAddress).Scan(&sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return &Session{
		ID:           sessionID,
		CustomerID:   customerID,
		SessionToken: token,
		Verified2FA:  verified2FA,
		ExpiresAt:    expiresAt,
		IPAddress:    ipAddress,
	}, nil
}

// GetSession retrieves a session by token
func (s *Service) GetSession(token string) (*Session, error) {
	return s.GetSessionWithIP(token, "")
}

// Session idle timeout - expire session if inactive for this duration
// 30 minutes is appropriate for a financial application handling BTC
const SessionIdleTimeout = 30 * time.Minute

// GetSessionWithIP validates session and checks for IP changes
func (s *Service) GetSessionWithIP(token string, currentIP string) (*Session, error) {
	var sess Session
	var lastUsedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT id, customer_id, session_token, verified_2fa, created_at, expires_at, last_used_at, COALESCE(ip_address, '')
		FROM rental_sessions
		WHERE session_token = $1 AND expires_at > NOW()
	`, HashToken(token)).Scan(
		&sess.ID, &sess.CustomerID, &sess.SessionToken, &sess.Verified2FA,
		&sess.CreatedAt, &sess.ExpiresAt, &lastUsedAt, &sess.IPAddress,
	)
	if lastUsedAt.Valid {
		sess.LastUsedAt = lastUsedAt.Time
	}
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found or expired")
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}

	// The DB stores only HashToken(token); re-attach the raw token so downstream
	// CSRF derivation and current-session checks stay keyed on the browser's secret.
	sess.SessionToken = token

	// Check idle timeout (30 minutes of inactivity = session expired)
	if !sess.LastUsedAt.IsZero() && time.Since(sess.LastUsedAt) > SessionIdleTimeout {
		// Delete the idle session
		s.db.Exec(`DELETE FROM rental_sessions WHERE id = $1`, sess.ID)
		return nil, fmt.Errorf("session expired due to inactivity")
	}

	// Check for IP address change - INVALIDATE session for security
	// For a financial application, IP change indicates potential session hijacking
	if currentIP != "" && sess.IPAddress != "" && sess.IPAddress != currentIP {
		log.Printf("SECURITY: Session %d IP changed from %s to %s (customer_id=%d) - INVALIDATING",
			sess.ID, sess.IPAddress, currentIP, sess.CustomerID)
		// Log to security audit trail
		s.LogSecurityEvent(sess.CustomerID, "session_ip_changed", currentIP, "", map[string]interface{}{
			"session_id": sess.ID,
			"old_ip":     sess.IPAddress,
			"new_ip":     currentIP,
			"action":     "invalidated",
		})
		// Delete the compromised session
		s.db.Exec(`DELETE FROM rental_sessions WHERE id = $1`, sess.ID)
		return nil, fmt.Errorf("session invalidated: IP address changed, please log in again")
	}

	// Update last used
	s.db.Exec(`UPDATE rental_sessions SET last_used_at = NOW() WHERE id = $1`, sess.ID)

	return &sess, nil
}

// Verify2FAForSession marks a session as 2FA verified and rotates the session token
// Returns the new session token (must be set in cookie)
func (s *Service) Verify2FAForSession(sessionID int, code string, customerID int) (string, error) {
	valid, err := s.Verify2FACode(customerID, code)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", fmt.Errorf("invalid verification code")
	}

	// Generate new session token for privilege escalation protection
	newToken, err := GenerateSessionToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate new session token: %w", err)
	}

	// Update session with new token and mark as 2FA verified
	_, err = s.db.Exec(`
		UPDATE rental_sessions
		SET session_token = $1, verified_2fa = TRUE, last_used_at = NOW()
		WHERE id = $2
	`, HashToken(newToken), sessionID)
	if err != nil {
		return "", err
	}

	s.LogSecurityEvent(customerID, "session_rotated_2fa", "", "", map[string]interface{}{
		"session_id": sessionID,
	})

	return newToken, nil
}

// InvalidateSession invalidates a session (logout)
func (s *Service) InvalidateSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM rental_sessions WHERE session_token = $1`, HashToken(token))
	return err
}

// InvalidateAllSessions invalidates all sessions for a customer (security logout)
// Useful when user suspects account compromise
func (s *Service) InvalidateAllSessions(customerID int, currentToken string) (int, error) {
	// Delete all sessions except the current one (so user stays logged in)
	result, err := s.db.Exec(`
		DELETE FROM rental_sessions
		WHERE customer_id = $1 AND session_token != $2
	`, customerID, HashToken(currentToken))
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()

	// Log the security event
	s.LogSecurityEvent(customerID, "all_sessions_invalidated", "", "", map[string]interface{}{
		"sessions_terminated": count,
	})

	return int(count), nil
}

// CleanupExpiredSessions removes expired sessions
func (s *Service) CleanupExpiredSessions() {
	s.db.Exec(`DELETE FROM rental_sessions WHERE expires_at < NOW()`)
}

// DeleteAccount permanently deletes a customer account and all associated data
func (s *Service) DeleteAccount(customerID int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete in order of foreign key dependencies
	_, err = tx.Exec(`DELETE FROM rental_sessions WHERE customer_id = $1`, customerID)
	if err != nil {
		return fmt.Errorf("failed to delete sessions: %w", err)
	}

	_, err = tx.Exec(`DELETE FROM rental_ledger WHERE customer_id = $1`, customerID)
	if err != nil {
		return fmt.Errorf("failed to delete ledger: %w", err)
	}

	_, err = tx.Exec(`DELETE FROM rental_deposits WHERE customer_id = $1`, customerID)
	if err != nil {
		return fmt.Errorf("failed to delete deposits: %w", err)
	}

	_, err = tx.Exec(`DELETE FROM rental_withdrawals WHERE customer_id = $1`, customerID)
	if err != nil {
		return fmt.Errorf("failed to delete withdrawals: %w", err)
	}

	_, err = tx.Exec(`DELETE FROM rental_orders WHERE customer_id = $1`, customerID)
	if err != nil {
		return fmt.Errorf("failed to delete orders: %w", err)
	}

	_, err = tx.Exec(`DELETE FROM rental_security_events WHERE customer_id = $1`, customerID)
	if err != nil {
		return fmt.Errorf("failed to delete security events: %w", err)
	}

	_, err = tx.Exec(`DELETE FROM rental_customers WHERE id = $1`, customerID)
	if err != nil {
		return fmt.Errorf("failed to delete customer: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	log.Printf("Account %d permanently deleted", customerID)
	return nil
}

// Login authenticates a customer and creates a session
func (s *Service) Login(req *LoginRequest, ipAddress string) (*LoginResponse, error) {
	// Check if account is locked due to failed attempts
	lockKey := "login_fail:" + strings.ToLower(req.Email)
	var failedAttempts int
	var lockedUntil sql.NullTime
	err := s.db.QueryRow(`
		SELECT request_count, locked_until FROM rental_rate_limits
		WHERE identifier = $1 AND endpoint = 'login'
	`, lockKey).Scan(&failedAttempts, &lockedUntil)

	if err == nil && lockedUntil.Valid && lockedUntil.Time.After(time.Now()) {
		remainingMins := int(time.Until(lockedUntil.Time).Minutes()) + 1
		return nil, fmt.Errorf("account temporarily locked due to failed login attempts, try again in %d minutes", remainingMins)
	}

	// Get customer by email
	customer, err := s.GetCustomerByEmail(req.Email)
	if err != nil {
		s.recordFailedLogin(lockKey)
		s.LogSecurityEvent(0, "login_failed", ipAddress, "", map[string]interface{}{
			"email":  req.Email,
			"reason": "email_not_found",
		})
		return nil, fmt.Errorf("invalid credentials")
	}

	// Verify password
	if !VerifyPassword(req.Password, customer.PasswordHash) {
		s.recordFailedLogin(lockKey)
		s.LogSecurityEvent(customer.ID, "login_failed", ipAddress, "", map[string]interface{}{
			"email":  req.Email,
			"reason": "invalid_password",
		})
		return nil, fmt.Errorf("invalid credentials")
	}

	// Clear failed attempts on successful login
	s.db.Exec(`DELETE FROM rental_rate_limits WHERE identifier = $1 AND endpoint = 'login'`, lockKey)

	// Check if email is verified
	if !customer.EmailVerified {
		return nil, fmt.Errorf("please verify your email before logging in")
	}

	// Check if 2FA is required
	requires2FA := s.Is2FARequired(customer.ID)

	if requires2FA {
		if req.TOTPCode == "" {
			// Return indication that 2FA is needed
			return &LoginResponse{
				Requires2FA: true,
			}, nil
		}

		// Verify 2FA code
		valid, err := s.Verify2FACode(customer.ID, req.TOTPCode)
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, fmt.Errorf("invalid 2FA code")
		}
	}

	// SECURITY: Invalidate all existing sessions before creating new one
	// This prevents session fixation attacks
	_, err = s.db.Exec(`DELETE FROM rental_sessions WHERE customer_id = $1`, customer.ID)
	if err != nil {
		log.Printf("Warning: failed to invalidate old sessions for customer %d: %v", customer.ID, err)
		// Continue anyway - creating new session is more important
	}

	// Create session
	session, err := s.CreateSession(customer.ID, requires2FA, ipAddress)
	if err != nil {
		return nil, err
	}

	// Log successful login
	s.LogSecurityEvent(customer.ID, "login_success", ipAddress, "", map[string]interface{}{
		"email":     req.Email,
		"used_2fa":  requires2FA,
		"sessionId": session.ID,
	})

	// Track IP and alert if new
	go s.CheckAndTrackLoginIP(customer.ID, ipAddress)

	return &LoginResponse{
		SessionToken: session.SessionToken,
		ExpiresAt:    session.ExpiresAt,
	}, nil
}

// recordFailedLogin tracks failed login attempts and locks account after 5 failures
func (s *Service) recordFailedLogin(lockKey string) {
	const maxAttempts = 5
	const lockDuration = 15 * time.Minute

	// Upsert failed attempt count
	var currentCount int
	err := s.db.QueryRow(`
		INSERT INTO rental_rate_limits (identifier, endpoint, request_count, window_start)
		VALUES ($1, 'login', 1, NOW())
		ON CONFLICT (identifier, endpoint)
		DO UPDATE SET request_count = rental_rate_limits.request_count + 1
		RETURNING request_count
	`, lockKey).Scan(&currentCount)

	if err != nil {
		log.Printf("Failed to record login attempt: %v", err)
		return
	}

	// Lock account if threshold exceeded
	if currentCount >= maxAttempts {
		s.db.Exec(`
			UPDATE rental_rate_limits
			SET locked_until = $1
			WHERE identifier = $2 AND endpoint = 'login'
		`, time.Now().Add(lockDuration), lockKey)
		log.Printf("Account locked due to %d failed login attempts: %s", currentCount, lockKey)
	}
}

// ============================================================================
// Admin Dashboard Methods
// ============================================================================

// GetAdminDashboardStats returns stats for admin dashboard
func (s *Service) GetAdminDashboardStats() *AdminStats {
	stats := &AdminStats{}

	s.db.QueryRow(`SELECT COUNT(*) FROM rental_customers`).Scan(&stats.TotalCustomers)
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_orders WHERE status = 'active'`).Scan(&stats.ActiveOrders)
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_withdrawals WHERE status = 'pending'`).Scan(&stats.PendingWithdrawals)

	var totalVolumeSat int64
	s.db.QueryRow(`SELECT COALESCE(SUM(amount_spent_sat), 0) FROM rental_orders`).Scan(&totalVolumeSat)
	stats.TotalVolumeBTC = fmt.Sprintf("%.8f", float64(totalVolumeSat)/100000000)

	return stats
}

// GetPendingWithdrawalsForAdmin returns pending withdrawals with customer info
func (s *Service) GetPendingWithdrawalsForAdmin() ([]AdminWithdrawalView, error) {
	rows, err := s.db.Query(`
		SELECT w.id, w.customer_id, w.amount_sat, w.btc_address, w.status, w.created_at,
		       c.email
		FROM rental_withdrawals w
		JOIN rental_customers c ON c.id = w.customer_id
		WHERE w.status = 'pending'
		ORDER BY w.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var withdrawals []AdminWithdrawalView
	for rows.Next() {
		var w AdminWithdrawalView
		if err := rows.Scan(&w.ID, &w.CustomerID, &w.AmountSat, &w.BTCAddress, &w.Status, &w.CreatedAt, &w.CustomerEmail); err != nil {
			continue
		}
		withdrawals = append(withdrawals, w)
	}
	return withdrawals, nil
}

// GetRecentOrdersForAdmin returns recent orders with customer info
func (s *Service) GetRecentOrdersForAdmin(limit int) ([]AdminOrderView, error) {
	rows, err := s.db.Query(`
		SELECT o.id, o.customer_id, o.budget_sat, o.speed_limit_ph, o.status, o.created_at,
		       c.email
		FROM rental_orders o
		JOIN rental_customers c ON c.id = o.customer_id
		ORDER BY o.created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []AdminOrderView
	for rows.Next() {
		var o AdminOrderView
		if err := rows.Scan(&o.ID, &o.CustomerID, &o.BudgetSat, &o.SpeedLimitPH, &o.Status, &o.CreatedAt, &o.CustomerEmail); err != nil {
			continue
		}
		orders = append(orders, o)
	}
	return orders, nil
}

// GetRecentCustomersForAdmin returns recent customers with stats
func (s *Service) GetRecentCustomersForAdmin(limit int) ([]AdminCustomerView, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.email, COALESCE(c.totp_enabled, false), c.created_at,
		       COALESCE(SUM(CASE WHEN l.tx_type IN ('deposit', 'order_refund') THEN l.amount_sat
		                         WHEN l.tx_type IN ('order_charge', 'withdrawal') THEN -l.amount_sat
		                         ELSE 0 END), 0) as balance,
		       COUNT(DISTINCT o.id) as order_count
		FROM rental_customers c
		LEFT JOIN rental_ledger l ON l.customer_id = c.id
		LEFT JOIN rental_orders o ON o.customer_id = c.id
		GROUP BY c.id
		ORDER BY c.created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var customers []AdminCustomerView
	for rows.Next() {
		var c AdminCustomerView
		if err := rows.Scan(&c.ID, &c.Email, &c.TwoFAEnabled, &c.CreatedAt, &c.BalanceSat, &c.OrderCount); err != nil {
			continue
		}
		c.BalanceBTC = fmt.Sprintf("%.8f", float64(c.BalanceSat)/100000000)
		customers = append(customers, c)
	}
	return customers, nil
}

// ============================================================================
// Security Events
// ============================================================================

// GetSecurityEvents returns security events for a customer
func (s *Service) GetSecurityEvents(customerID int, limit int) ([]SecurityEvent, error) {
	rows, err := s.db.Query(`
		SELECT id, event_type, COALESCE(ip_address, ''), COALESCE(user_agent, ''),
		       COALESCE(details::text, ''), created_at
		FROM rental_security_log
		WHERE customer_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []SecurityEvent
	for rows.Next() {
		var e SecurityEvent
		if err := rows.Scan(&e.ID, &e.EventType, &e.IPAddress, &e.UserAgent, &e.Details, &e.CreatedAt); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events, nil
}

// ============================================================================
// Email Preferences
// ============================================================================

// GetEmailPreferences returns email preferences for a customer
func (s *Service) GetEmailPreferences(customerID int) (*EmailPreferences, error) {
	prefs := &EmailPreferences{
		CustomerID:        customerID,
		NotifyDeposits:    true,
		NotifyOrders:      true,
		NotifyWithdrawals: true,
		NotifySecurity:    true,
		NotifyMarketing:   false,
	}

	err := s.db.QueryRow(`
		SELECT notify_deposits, notify_orders, notify_withdrawals, notify_security, notify_marketing
		FROM rental_email_preferences
		WHERE customer_id = $1
	`, customerID).Scan(&prefs.NotifyDeposits, &prefs.NotifyOrders, &prefs.NotifyWithdrawals,
		&prefs.NotifySecurity, &prefs.NotifyMarketing)

	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return prefs, nil
}

// UpdateEmailPreferences updates email preferences for a customer
func (s *Service) UpdateEmailPreferences(customerID int, prefs *EmailPreferences) error {
	_, err := s.db.Exec(`
		INSERT INTO rental_email_preferences (customer_id, notify_deposits, notify_orders, notify_withdrawals, notify_security, notify_marketing, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (customer_id) DO UPDATE SET
			notify_deposits = $2, notify_orders = $3, notify_withdrawals = $4,
			notify_security = $5, notify_marketing = $6, updated_at = NOW()
	`, customerID, prefs.NotifyDeposits, prefs.NotifyOrders, prefs.NotifyWithdrawals,
		prefs.NotifySecurity, prefs.NotifyMarketing)
	return err
}

// ShouldSendEmail checks if customer should receive a specific email type
func (s *Service) ShouldSendEmail(customerID int, emailType string) bool {
	prefs, err := s.GetEmailPreferences(customerID)
	if err != nil {
		return true // Default to sending if error
	}
	switch emailType {
	case "deposit":
		return prefs.NotifyDeposits
	case "order":
		return prefs.NotifyOrders
	case "withdrawal":
		return prefs.NotifyWithdrawals
	case "security":
		return prefs.NotifySecurity
	case "marketing":
		return prefs.NotifyMarketing
	default:
		return true
	}
}

// ============================================================================
// Withdrawal Whitelist
// ============================================================================

// GetWhitelistedAddresses returns whitelisted addresses for a customer
func (s *Service) GetWhitelistedAddresses(customerID int) ([]WhitelistedAddress, error) {
	rows, err := s.db.Query(`
		SELECT id, customer_id, btc_address, COALESCE(label, ''), verified, verified_at, created_at
		FROM rental_withdrawal_whitelist
		WHERE customer_id = $1
		ORDER BY created_at DESC
	`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var addresses []WhitelistedAddress
	for rows.Next() {
		var a WhitelistedAddress
		if err := rows.Scan(&a.ID, &a.CustomerID, &a.BTCAddress, &a.Label, &a.Verified, &a.VerifiedAt, &a.CreatedAt); err != nil {
			continue
		}
		addresses = append(addresses, a)
	}
	return addresses, nil
}

// AddWhitelistedAddress adds an address to whitelist (24h delay before usable)
func (s *Service) AddWhitelistedAddress(customerID int, address, label string) error {
	// Validate BTC address
	if !isValidBTCAddressFormat(address) {
		return fmt.Errorf("invalid BTC address format")
	}

	_, err := s.db.Exec(`
		INSERT INTO rental_withdrawal_whitelist (customer_id, btc_address, label, verified, created_at)
		VALUES ($1, $2, $3, FALSE, NOW())
	`, customerID, address, label)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			return fmt.Errorf("address already whitelisted")
		}
		return err
	}

	// Auto-verify after 24 hours (done via background job or check at withdrawal time)
	return nil
}

// RemoveWhitelistedAddress removes an address from whitelist
func (s *Service) RemoveWhitelistedAddress(customerID int, addressID int) error {
	result, err := s.db.Exec(`
		DELETE FROM rental_withdrawal_whitelist
		WHERE id = $1 AND customer_id = $2
	`, addressID, customerID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("address not found")
	}
	return nil
}

// IsAddressWhitelisted checks if an address is whitelisted and verified
func (s *Service) IsAddressWhitelisted(customerID int, address string) (bool, error) {
	var verified bool
	var createdAt time.Time

	err := s.db.QueryRow(`
		SELECT verified, created_at
		FROM rental_withdrawal_whitelist
		WHERE customer_id = $1 AND btc_address = $2
	`, customerID, address).Scan(&verified, &createdAt)

	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Address is verified if explicitly verified OR 24 hours have passed
	if verified || time.Since(createdAt) >= 24*time.Hour {
		// Update to verified if 24h passed
		if !verified && time.Since(createdAt) >= 24*time.Hour {
			s.db.Exec(`UPDATE rental_withdrawal_whitelist SET verified = TRUE, verified_at = NOW() WHERE customer_id = $1 AND btc_address = $2`, customerID, address)
		}
		return true, nil
	}

	return false, nil
}

// ============================================================================
// Notifications
// ============================================================================

// CreateNotification creates an in-app notification
func (s *Service) CreateNotification(customerID int, notifType, title, message, link string) error {
	_, err := s.db.Exec(`
		INSERT INTO rental_notifications (customer_id, type, title, message, link, read, created_at)
		VALUES ($1, $2, $3, $4, $5, FALSE, NOW())
	`, customerID, notifType, title, message, link)
	return err
}

// GetNotifications returns notifications for a customer
func (s *Service) GetNotifications(customerID int, limit int) ([]Notification, error) {
	rows, err := s.db.Query(`
		SELECT id, type, title, message, COALESCE(link, ''), read, created_at
		FROM rental_notifications
		WHERE customer_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notifications []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Type, &n.Title, &n.Message, &n.Link, &n.Read, &n.CreatedAt); err != nil {
			continue
		}
		notifications = append(notifications, n)
	}
	return notifications, nil
}

// GetUnreadNotificationCount returns count of unread notifications
func (s *Service) GetUnreadNotificationCount(customerID int) int {
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM rental_notifications WHERE customer_id = $1 AND read = FALSE`, customerID).Scan(&count)
	return count
}

// MarkNotificationRead marks a notification as read
func (s *Service) MarkNotificationRead(customerID int, notificationID int) error {
	_, err := s.db.Exec(`
		UPDATE rental_notifications SET read = TRUE
		WHERE id = $1 AND customer_id = $2
	`, notificationID, customerID)
	return err
}

// MarkAllNotificationsRead marks all notifications as read
func (s *Service) MarkAllNotificationsRead(customerID int) error {
	_, err := s.db.Exec(`UPDATE rental_notifications SET read = TRUE WHERE customer_id = $1`, customerID)
	return err
}

// ============================================================================
// Session Management
// ============================================================================

// GetActiveSessions returns active sessions for a customer
func (s *Service) GetActiveSessions(customerID int, currentSessionToken string) ([]SessionView, error) {
	rows, err := s.db.Query(`
		SELECT id, COALESCE(device_name, 'Unknown Device'), COALESCE(ip_address, ''),
		       created_at, last_used_at, session_token
		FROM rental_sessions
		WHERE customer_id = $1 AND expires_at > NOW()
		ORDER BY last_used_at DESC
	`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionView
	for rows.Next() {
		var s SessionView
		var token string
		if err := rows.Scan(&s.ID, &s.DeviceName, &s.IPAddress, &s.CreatedAt, &s.LastUsedAt, &token); err != nil {
			continue
		}
		// Use constant-time comparison to prevent timing attacks
		s.IsCurrent = subtle.ConstantTimeCompare([]byte(token), []byte(HashToken(currentSessionToken))) == 1
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// RevokeSession revokes a specific session
func (s *Service) RevokeSession(customerID int, sessionID int) error {
	result, err := s.db.Exec(`
		DELETE FROM rental_sessions
		WHERE id = $1 AND customer_id = $2
	`, sessionID, customerID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("session not found")
	}
	return nil
}

// ============================================================================
// API Key Management
// ============================================================================

// GetAPIKey returns the customer's API key (masked except last 4 chars)
func (s *Service) GetAPIKeyMasked(customerID int) string {
	var apiKey string
	s.db.QueryRow(`SELECT api_key FROM rental_customers WHERE id = $1`, customerID).Scan(&apiKey)
	if len(apiKey) <= 4 {
		return apiKey
	}
	return strings.Repeat("*", len(apiKey)-4) + apiKey[len(apiKey)-4:]
}

// RegenerateAPIKey generates a new API key for the customer
func (s *Service) RegenerateAPIKey(customerID int) (string, error) {
	newKey, err := GenerateToken()
	if err != nil {
		return "", err
	}

	_, err = s.db.Exec(`UPDATE rental_customers SET api_key = $1 WHERE id = $2`, HashAPIKey(newKey), customerID)
	if err != nil {
		return "", err
	}

	s.LogSecurityEvent(customerID, "api_key_regenerated", "", "", nil)
	return newKey, nil
}

// ============================================================================
// Change Password
// ============================================================================

// ChangePassword changes the customer's password (requires current password)
func (s *Service) ChangePassword(customerID int, currentPassword, newPassword string) error {
	// Get current hash
	var currentHash string
	err := s.db.QueryRow(`SELECT COALESCE(password_hash, '') FROM rental_customers WHERE id = $1`, customerID).Scan(&currentHash)
	if err != nil {
		return fmt.Errorf("customer not found")
	}

	// Verify current password
	if !VerifyPassword(currentPassword, currentHash) {
		return fmt.Errorf("current password is incorrect")
	}

	// Validate new password
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}

	// Hash new password
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password")
	}

	// Update password
	_, err = s.db.Exec(`UPDATE rental_customers SET password_hash = $1 WHERE id = $2`, newHash, customerID)
	if err != nil {
		return fmt.Errorf("failed to update password")
	}

	s.LogSecurityEvent(customerID, "password_changed", "", "", nil)
	return nil
}

// ============================================================================
// IP Security Alerts
// ============================================================================

// CheckAndTrackLoginIP checks if IP is new and sends alert if so
func (s *Service) CheckAndTrackLoginIP(customerID int, ipAddress string) {
	if ipAddress == "" {
		return
	}

	// Try to insert new IP, or update existing
	var isNew bool
	err := s.db.QueryRow(`
		INSERT INTO rental_known_ips (customer_id, ip_address, first_seen, last_seen, login_count)
		VALUES ($1, $2, NOW(), NOW(), 1)
		ON CONFLICT (customer_id, ip_address) DO UPDATE
		SET last_seen = NOW(), login_count = rental_known_ips.login_count + 1
		RETURNING (xmax = 0)
	`, customerID, ipAddress).Scan(&isNew)

	if err != nil {
		log.Printf("Failed to track login IP: %v", err)
		return
	}

	// If this is a new IP, send security alert
	if isNew {
		go s.sendNewIPAlert(customerID, ipAddress)
	}
}

// sendNewIPAlert sends email and notification for new IP login
func (s *Service) sendNewIPAlert(customerID int, ipAddress string) {
	// Create in-app notification
	s.CreateNotification(customerID, "security", "New Login Location",
		fmt.Sprintf("Your account was accessed from a new IP address: %s. If this wasn't you, change your password immediately.", ipAddress),
		"/security")

	// Send email if preferences allow
	if s.email == nil || !s.ShouldSendEmail(customerID, "security") {
		return
	}

	customer, err := s.GetCustomerByID(customerID)
	if err != nil || customer.Email == "" {
		return
	}

	if err := s.email.SendNewIPLoginEmail(customer.Email, ipAddress); err != nil {
		log.Printf("Failed to send new IP alert to %s: %v", customer.Email, err)
	}
}

// GetKnownIPs returns known IPs for a customer
func (s *Service) GetKnownIPs(customerID int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT ip_address FROM rental_known_ips
		WHERE customer_id = $1
		ORDER BY last_seen DESC
		LIMIT 20
	`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err == nil {
			ips = append(ips, ip)
		}
	}
	return ips, nil
}

// ============================================================================
// Braiins Balance Alerts
// ============================================================================

// CheckBraiinsBalanceAndAlert checks Braiins balance and sends alert if low
func (s *Service) CheckBraiinsBalanceAndAlert() {
	balance, err := s.braiins.GetBalance()
	if err != nil {
		log.Printf("Failed to check Braiins balance: %v", err)
		return
	}

	threshold := s.getSetting("braiins_balance_alert_threshold", 1000000) // 0.01 BTC default
	if balance.AvailableSat < threshold {
		s.sendBraiinsBalanceAlert(balance.AvailableSat, threshold)
	}
}

// sendBraiinsBalanceAlert sends email to admin about low balance
func (s *Service) sendBraiinsBalanceAlert(currentSat, thresholdSat int64) {
	adminEmail := s.getSettingString("admin_alert_email", "")
	if adminEmail == "" || s.email == nil {
		log.Printf("WARNING: Braiins balance low (%.8f BTC) but no admin email configured",
			float64(currentSat)/100000000)
		return
	}

	currentBTC := fmt.Sprintf("%.8f", float64(currentSat)/100000000)
	thresholdBTC := fmt.Sprintf("%.8f", float64(thresholdSat)/100000000)

	if err := s.email.SendBraiinsBalanceAlertEmail(adminEmail, currentBTC, thresholdBTC); err != nil {
		log.Printf("Failed to send Braiins balance alert: %v", err)
	}
}

// getSettingString returns a string setting value
func (s *Service) getSettingString(key, defaultValue string) string {
	var value string
	err := s.db.QueryRow(`SELECT value FROM rental_settings WHERE key = $1`, key).Scan(&value)
	if err != nil {
		return defaultValue
	}
	return value
}

// GetBraiinsBalanceStatus returns balance status for admin dashboard
func (s *Service) GetBraiinsBalanceStatus() (availableSat int64, isLow bool, thresholdSat int64) {
	thresholdSat = s.getSetting("braiins_balance_alert_threshold", 1000000)

	balance, err := s.braiins.GetBalance()
	if err != nil {
		return 0, false, thresholdSat
	}

	availableSat = balance.AvailableSat
	isLow = availableSat < thresholdSat
	return
}

// ============================================================================
// Contact Form Methods
// ============================================================================

// SaveContactMessage saves a contact form submission
func (s *Service) SaveContactMessage(customerID *int, name, email, subject, message, ipAddress string) error {
	_, err := s.db.Exec(`
		INSERT INTO rental_contact_messages (customer_id, name, email, subject, message, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, customerID, name, email, subject, message, ipAddress)
	return err
}

// SendContactNotification sends email notification to admin about new contact message
func (s *Service) SendContactNotification(name, email, subject, message string) {
	if s.email == nil || s.email.config == nil || s.email.config.SMTPHost == "" {
		log.Printf("CONTACT: New message from %s <%s>: %s", name, email, subject)
		return
	}

	// Get admin email from settings
	adminEmail := s.getSettingString("admin_alert_email", "dev@bitcoincashii.org")

	emailSubject := fmt.Sprintf("New Contact Form: %s", subject)
	body := fmt.Sprintf(`New contact form submission:

From: %s <%s>
Subject: %s

Message:
%s

---
Reply directly to this email or via the admin panel.
`, name, email, subject, message)

	if err := s.email.sendEmail(adminEmail, emailSubject, body); err != nil {
		log.Printf("Failed to send contact notification: %v", err)
	}
}

// GetContactMessages returns contact messages for admin
func (s *Service) GetContactMessages(status string, limit, offset int) ([]ContactMessage, int, error) {
	// Validate status to prevent injection (whitelist allowed values)
	validStatuses := map[string]bool{"new": true, "read": true, "replied": true, "archived": true}
	useStatusFilter := status != "" && status != "all" && validStatuses[status]

	var total int
	if useStatusFilter {
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_contact_messages WHERE status = $1`, status).Scan(&total)
	} else {
		s.db.QueryRow(`SELECT COUNT(*) FROM rental_contact_messages`).Scan(&total)
	}

	var rows *sql.Rows
	var err error
	if useStatusFilter {
		rows, err = s.db.Query(`
			SELECT id, customer_id, name, email, subject, message, ip_address, status, created_at, replied_at
			FROM rental_contact_messages
			WHERE status = $1
			ORDER BY created_at DESC LIMIT $2 OFFSET $3
		`, status, limit, offset)
	} else {
		rows, err = s.db.Query(`
			SELECT id, customer_id, name, email, subject, message, ip_address, status, created_at, replied_at
			FROM rental_contact_messages
			ORDER BY created_at DESC LIMIT $1 OFFSET $2
		`, limit, offset)
	}
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var messages []ContactMessage
	for rows.Next() {
		var m ContactMessage
		var customerID sql.NullInt64
		var repliedAt sql.NullTime
		if err := rows.Scan(&m.ID, &customerID, &m.Name, &m.Email, &m.Subject, &m.Message, &m.IPAddress, &m.Status, &m.CreatedAt, &repliedAt); err != nil {
			continue
		}
		if customerID.Valid {
			id := int(customerID.Int64)
			m.CustomerID = &id
		}
		if repliedAt.Valid {
			m.RepliedAt = &repliedAt.Time
		}
		messages = append(messages, m)
	}
	return messages, total, nil
}

// UpdateContactMessageStatus updates a contact message status
func (s *Service) UpdateContactMessageStatus(messageID int, status string, adminID int) error {
	if status == "replied" {
		_, err := s.db.Exec(`
			UPDATE rental_contact_messages
			SET status = $1, replied_at = NOW(), replied_by = $2
			WHERE id = $3
		`, status, adminID, messageID)
		return err
	}
	_, err := s.db.Exec(`UPDATE rental_contact_messages SET status = $1 WHERE id = $2`, status, messageID)
	return err
}

// ============================================================================
// Email Change Methods
// ============================================================================

// RequestEmailChange initiates an email change request
func (s *Service) RequestEmailChange(customerID int, newEmail string) error {
	// Validate new email
	if err := ValidateEmail(newEmail); err != nil {
		return err
	}
	newEmail = NormalizeEmail(newEmail)

	// Get current customer
	customer, err := s.GetCustomerByID(customerID)
	if err != nil {
		return fmt.Errorf("customer not found")
	}

	// Check if new email is same as old
	if customer.Email == newEmail {
		return fmt.Errorf("new email is the same as current email")
	}

	// Check if new email is already in use
	var existingID int
	err = s.db.QueryRow(`SELECT id FROM rental_customers WHERE email = $1`, newEmail).Scan(&existingID)
	if err == nil {
		return fmt.Errorf("email address is already in use")
	}

	// Cancel any pending email change requests
	s.db.Exec(`DELETE FROM rental_email_changes WHERE customer_id = $1 AND completed_at IS NULL`, customerID)

	// Create new request with SEPARATE tokens per side, so confirming one side
	// (e.g. the attacker-controlled new address) cannot also confirm the other.
	tokenOld, err := GenerateToken()
	if err != nil {
		return fmt.Errorf("failed to generate token")
	}
	tokenNew, err := GenerateToken()
	if err != nil {
		return fmt.Errorf("failed to generate token")
	}

	// Only hashes are stored. The legacy token column keeps the old-side hash to
	// satisfy its NOT NULL/UNIQUE constraint; confirmation matches token_old/new.
	oldHash := HashToken(tokenOld)
	newHash := HashToken(tokenNew)
	expiresAt := time.Now().Add(24 * time.Hour)
	_, err = s.db.Exec(`
		INSERT INTO rental_email_changes (customer_id, old_email, new_email, token, token_old, token_new, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, customerID, customer.Email, newEmail, oldHash, oldHash, newHash, expiresAt)
	if err != nil {
		return fmt.Errorf("failed to create email change request")
	}

	// Send each side its own token (unhashed) to its own address.
	go s.sendEmailChangeVerification(customer.Email, newEmail, tokenOld, true)
	go s.sendEmailChangeVerification(newEmail, customer.Email, tokenNew, false)

	return nil
}

// sendEmailChangeVerification sends verification email for email change
func (s *Service) sendEmailChangeVerification(toEmail, otherEmail, token string, isOldEmail bool) {
	if s.email == nil || s.email.config == nil || s.email.config.SMTPHost == "" {
		// Don't log tokens - security risk. Just indicate email would be sent.
		log.Printf("EMAIL CHANGE: Verification email for %s (SMTP not configured)", toEmail)
		return
	}

	var subject, body string
	verifyURL := fmt.Sprintf("%s/settings/email/verify?token=%s&type=", s.email.config.BaseURL, token)

	if isOldEmail {
		subject = "Confirm Email Change - HashForge"
		verifyURL += "old"
		body = fmt.Sprintf(`You requested to change your HashForge email address.

New email will be: %s

To confirm this change, click the link below:
%s

This link expires in 24 hours.

If you did not request this change, please secure your account immediately.

---
HashForge by Forge Pool
`, otherEmail, verifyURL)
	} else {
		subject = "Verify Your New Email - HashForge"
		verifyURL += "new"
		body = fmt.Sprintf(`Someone is trying to change their HashForge account email to this address.

Current email: %s

To verify this is your email, click the link below:
%s

This link expires in 24 hours.

If you did not request this, you can ignore this email.

---
HashForge by Forge Pool
`, otherEmail, verifyURL)
	}

	if err := s.email.sendEmail(toEmail, subject, body); err != nil {
		log.Printf("Failed to send email change verification to %s: %v", toEmail, err)
	}
}

// ConfirmEmailChange confirms part of an email change request
func (s *Service) ConfirmEmailChange(token, confirmType string) (bool, error) {
	// Hash the token for lookup (tokens are stored as hashes). The token is bound
	// to its side: an "old" confirmation must present the token emailed to the old
	// address, and "new" the token emailed to the new address, so one side's token
	// cannot confirm both sides.
	tokenHash := HashToken(token)

	var sideColumn string
	switch confirmType {
	case "old":
		sideColumn = "token_old"
	case "new":
		sideColumn = "token_new"
	default:
		return false, fmt.Errorf("invalid confirmation type")
	}

	// Find the request by the side-specific token column (sideColumn is a fixed
	// literal from the switch above, never user input).
	var req EmailChangeRequest
	err := s.db.QueryRow(`
		SELECT id, customer_id, old_email, new_email, confirmed_old, confirmed_new, expires_at
		FROM rental_email_changes
		WHERE `+sideColumn+` = $1 AND completed_at IS NULL
	`, tokenHash).Scan(&req.ID, &req.CustomerID, &req.OldEmail, &req.NewEmail, &req.ConfirmedOld, &req.ConfirmedNew, &req.ExpiresAt)
	if err != nil {
		return false, fmt.Errorf("invalid or expired token")
	}

	// Check if expired
	if time.Now().After(req.ExpiresAt) {
		s.db.Exec(`DELETE FROM rental_email_changes WHERE id = $1`, req.ID)
		return false, fmt.Errorf("token has expired")
	}

	// Update confirmation status
	if confirmType == "old" {
		_, err = s.db.Exec(`UPDATE rental_email_changes SET confirmed_old = TRUE WHERE id = $1`, req.ID)
		req.ConfirmedOld = true
	} else if confirmType == "new" {
		_, err = s.db.Exec(`UPDATE rental_email_changes SET confirmed_new = TRUE WHERE id = $1`, req.ID)
		req.ConfirmedNew = true
	} else {
		return false, fmt.Errorf("invalid confirmation type")
	}

	if err != nil {
		return false, fmt.Errorf("failed to update confirmation")
	}

	// If both confirmed, complete the change
	if req.ConfirmedOld && req.ConfirmedNew {
		// Update customer email
		_, err = s.db.Exec(`UPDATE rental_customers SET email = $1 WHERE id = $2`, req.NewEmail, req.CustomerID)
		if err != nil {
			return false, fmt.Errorf("failed to update email")
		}

		// Mark request as completed
		s.db.Exec(`UPDATE rental_email_changes SET completed_at = NOW() WHERE id = $1`, req.ID)

		// Create notification
		s.CreateNotification(req.CustomerID, "security", "Email Changed",
			fmt.Sprintf("Your email has been changed from %s to %s", req.OldEmail, req.NewEmail),
			"/settings")

		return true, nil // fully completed
	}

	return false, nil // partially confirmed
}

// GetPendingEmailChange returns any pending email change for a customer
func (s *Service) GetPendingEmailChange(customerID int) *EmailChangeRequest {
	var req EmailChangeRequest
	err := s.db.QueryRow(`
		SELECT id, old_email, new_email, confirmed_old, confirmed_new, expires_at, created_at
		FROM rental_email_changes
		WHERE customer_id = $1 AND completed_at IS NULL AND expires_at > NOW()
		ORDER BY created_at DESC LIMIT 1
	`, customerID).Scan(&req.ID, &req.OldEmail, &req.NewEmail, &req.ConfirmedOld, &req.ConfirmedNew, &req.ExpiresAt, &req.CreatedAt)
	if err != nil {
		return nil
	}
	req.CustomerID = customerID
	return &req
}

// CancelEmailChange cancels a pending email change
func (s *Service) CancelEmailChange(customerID int) error {
	_, err := s.db.Exec(`DELETE FROM rental_email_changes WHERE customer_id = $1 AND completed_at IS NULL`, customerID)
	return err
}

// ============================================================================
// Balance Alert Methods
// ============================================================================

// GetBalanceAlert returns balance alert settings for a customer
func (s *Service) GetBalanceAlert(customerID int) *BalanceAlert {
	var alert BalanceAlert
	var lastAlertedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT id, customer_id, threshold_sat, notify_email, notify_inapp, last_alerted_at, enabled
		FROM rental_balance_alerts
		WHERE customer_id = $1
	`, customerID).Scan(&alert.ID, &alert.CustomerID, &alert.ThresholdSat, &alert.NotifyEmail, &alert.NotifyInApp, &lastAlertedAt, &alert.Enabled)
	if err != nil {
		// Return default settings
		return &BalanceAlert{
			CustomerID:   customerID,
			ThresholdSat: 100000, // 0.001 BTC
			NotifyEmail:  true,
			NotifyInApp:  true,
			Enabled:      false,
		}
	}
	if lastAlertedAt.Valid {
		alert.LastAlertedAt = &lastAlertedAt.Time
	}
	return &alert
}

// UpdateBalanceAlert updates balance alert settings
func (s *Service) UpdateBalanceAlert(customerID int, thresholdSat int64, notifyEmail, notifyInApp, enabled bool) error {
	_, err := s.db.Exec(`
		INSERT INTO rental_balance_alerts (customer_id, threshold_sat, notify_email, notify_inapp, enabled, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (customer_id) DO UPDATE
		SET threshold_sat = $2, notify_email = $3, notify_inapp = $4, enabled = $5, updated_at = NOW()
	`, customerID, thresholdSat, notifyEmail, notifyInApp, enabled)
	return err
}

// CheckAndSendBalanceAlert checks if balance is below threshold and sends alert
func (s *Service) CheckAndSendBalanceAlert(customerID int) {
	alert := s.GetBalanceAlert(customerID)
	if !alert.Enabled {
		return
	}

	// Don't alert more than once per hour
	if alert.LastAlertedAt != nil && time.Since(*alert.LastAlertedAt) < time.Hour {
		return
	}

	balance, err := s.GetBalance(customerID)
	if err != nil {
		return
	}

	if balance.BalanceSat < alert.ThresholdSat {
		// Send alerts
		thresholdBTC := fmt.Sprintf("%.8f", float64(alert.ThresholdSat)/100000000)

		if alert.NotifyInApp {
			s.CreateNotification(customerID, "balance", "Low Balance Alert",
				fmt.Sprintf("Your balance (%s BTC) is below your alert threshold (%s BTC)", balance.BalanceBTC, thresholdBTC),
				"/deposit")
		}

		if alert.NotifyEmail {
			go s.sendBalanceAlertEmail(customerID, balance.BalanceBTC, thresholdBTC)
		}

		// Update last alerted time
		s.db.Exec(`UPDATE rental_balance_alerts SET last_alerted_at = NOW() WHERE customer_id = $1`, customerID)
	}
}

// ============================================================================
// Mining Rewards Methods
// ============================================================================

// MiningRewardsSummary contains mining rewards data for a customer
type MiningRewardsSummary struct {
	TotalBlocks     int     `json:"total_blocks"`
	TotalRewardBCH2 float64 `json:"total_reward_bch2"`
	ConfirmedBlocks int     `json:"confirmed_blocks"`
	PendingBlocks   int     `json:"pending_blocks"`
}

// GetMiningRewardsSummary returns mining rewards for a customer's BCH2 address
func (s *Service) GetMiningRewardsSummary(bch2Address string) *MiningRewardsSummary {
	if bch2Address == "" {
		return &MiningRewardsSummary{}
	}

	// Normalize address (remove prefix if present)
	address := strings.TrimPrefix(bch2Address, "bitcoincashii:")

	var summary MiningRewardsSummary

	// Query blocks table for this miner
	err := s.db.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(reward), 0)
		FROM blocks
		WHERE miner_id = $1 OR miner_id = $2
	`, address, "bitcoincashii:"+address).Scan(&summary.TotalBlocks, &summary.TotalRewardBCH2)

	if err != nil {
		log.Printf("Failed to get mining rewards for %s: %v", address, err)
		return &MiningRewardsSummary{}
	}

	return &summary
}

// OrderAnalytics contains analytics data for orders
type OrderAnalytics struct {
	TotalOrders     int                `json:"total_orders"`
	TotalSpentSat   int64              `json:"total_spent_sat"`
	TotalHashratePH float64            `json:"total_hashrate_ph"`
	AvgOrderSizeSat int64              `json:"avg_order_size_sat"`
	MonthlySpending []MonthlyDataPoint `json:"monthly_spending"`
	OrdersByMode    map[string]int     `json:"orders_by_mode"`
	OrdersByStatus  map[string]int     `json:"orders_by_status"`
}

// MonthlyDataPoint represents a data point for charts
type MonthlyDataPoint struct {
	Month      string  `json:"month"`
	SpentSat   int64   `json:"spent_sat"`
	Orders     int     `json:"orders"`
	HashratePH float64 `json:"hashrate_ph"`
}

// GetOrderAnalytics returns order analytics for a customer
func (s *Service) GetOrderAnalytics(customerID int) *OrderAnalytics {
	analytics := &OrderAnalytics{
		OrdersByMode:   make(map[string]int),
		OrdersByStatus: make(map[string]int),
	}

	// Get totals
	s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(amount_spent_sat), 0), COALESCE(SUM(speed_limit_ph), 0)
		FROM rental_orders WHERE customer_id = $1
	`, customerID).Scan(&analytics.TotalOrders, &analytics.TotalSpentSat, &analytics.TotalHashratePH)

	if analytics.TotalOrders > 0 {
		analytics.AvgOrderSizeSat = analytics.TotalSpentSat / int64(analytics.TotalOrders)
	}

	// Get orders by mode
	rows, _ := s.db.Query(`
		SELECT mining_mode, COUNT(*) FROM rental_orders
		WHERE customer_id = $1 GROUP BY mining_mode
	`, customerID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var mode string
			var count int
			rows.Scan(&mode, &count)
			analytics.OrdersByMode[mode] = count
		}
	}

	// Get orders by status
	rows, _ = s.db.Query(`
		SELECT status, COUNT(*) FROM rental_orders
		WHERE customer_id = $1 GROUP BY status
	`, customerID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var status string
			var count int
			rows.Scan(&status, &count)
			analytics.OrdersByStatus[status] = count
		}
	}

	// Get monthly spending (last 12 months)
	rows, _ = s.db.Query(`
		SELECT TO_CHAR(created_at, 'YYYY-MM') as month,
		       COALESCE(SUM(amount_spent_sat), 0) as spent,
		       COUNT(*) as orders,
		       COALESCE(SUM(speed_limit_ph), 0) as hashrate
		FROM rental_orders
		WHERE customer_id = $1 AND created_at >= NOW() - INTERVAL '12 months'
		GROUP BY TO_CHAR(created_at, 'YYYY-MM')
		ORDER BY month
	`, customerID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var dp MonthlyDataPoint
			rows.Scan(&dp.Month, &dp.SpentSat, &dp.Orders, &dp.HashratePH)
			analytics.MonthlySpending = append(analytics.MonthlySpending, dp)
		}
	}

	return analytics
}

// sendBalanceAlertEmail sends low balance email notification
func (s *Service) sendBalanceAlertEmail(customerID int, currentBTC, thresholdBTC string) {
	if s.email == nil || s.email.config == nil || s.email.config.SMTPHost == "" {
		return
	}

	customer, err := s.GetCustomerByID(customerID)
	if err != nil || customer.Email == "" {
		return
	}

	subject := "Low Balance Alert - HashForge"
	body := fmt.Sprintf(`Your HashForge balance is running low.

Current Balance: %s BTC
Alert Threshold: %s BTC

Top up your account to continue renting hashpower:
%s/deposit

---
HashForge by Forge Pool
`, currentBTC, thresholdBTC, s.email.config.BaseURL)

	if err := s.email.sendEmail(customer.Email, subject, body); err != nil {
		log.Printf("Failed to send balance alert email: %v", err)
	}
}

// ==================== Support Tickets ====================

// CreateTicket creates a new support ticket
func (s *Service) CreateTicket(customerID int, subject, category, message string) (*SupportTicket, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Create the ticket
	var ticket SupportTicket
	err = tx.QueryRow(`
		INSERT INTO rental_tickets (customer_id, subject, status, priority, category)
		VALUES ($1, $2, 'open', 'normal', $3)
		RETURNING id, customer_id, subject, status, priority, category, created_at, updated_at
	`, customerID, subject, category).Scan(
		&ticket.ID, &ticket.CustomerID, &ticket.Subject, &ticket.Status,
		&ticket.Priority, &ticket.Category, &ticket.CreatedAt, &ticket.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Add the initial message
	_, err = tx.Exec(`
		INSERT INTO rental_ticket_messages (ticket_id, sender_type, sender_id, message)
		VALUES ($1, 'customer', $2, $3)
	`, ticket.ID, customerID, message)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Send notification to admin
	go s.sendTicketNotification(&ticket, message)

	return &ticket, nil
}

// GetTicket returns a ticket by ID for a specific customer
func (s *Service) GetTicket(customerID, ticketID int) (*SupportTicket, error) {
	var ticket SupportTicket
	err := s.db.QueryRow(`
		SELECT id, customer_id, subject, status, priority, category, created_at, updated_at, closed_at
		FROM rental_tickets
		WHERE id = $1 AND customer_id = $2
	`, ticketID, customerID).Scan(
		&ticket.ID, &ticket.CustomerID, &ticket.Subject, &ticket.Status,
		&ticket.Priority, &ticket.Category, &ticket.CreatedAt, &ticket.UpdatedAt, &ticket.ClosedAt,
	)
	if err != nil {
		return nil, err
	}
	return &ticket, nil
}

// GetTicketMessages returns all messages for a ticket
func (s *Service) GetTicketMessages(ticketID int) ([]TicketMessage, error) {
	rows, err := s.db.Query(`
		SELECT id, ticket_id, sender_type, sender_id, message, created_at
		FROM rental_ticket_messages
		WHERE ticket_id = $1
		ORDER BY created_at ASC
	`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []TicketMessage
	for rows.Next() {
		var m TicketMessage
		if err := rows.Scan(&m.ID, &m.TicketID, &m.SenderType, &m.SenderID, &m.Message, &m.CreatedAt); err != nil {
			continue
		}
		messages = append(messages, m)
	}
	return messages, nil
}

// GetCustomerTickets returns all tickets for a customer
func (s *Service) GetCustomerTickets(customerID int) ([]SupportTicket, error) {
	rows, err := s.db.Query(`
		SELECT id, customer_id, subject, status, priority, category, created_at, updated_at, closed_at
		FROM rental_tickets
		WHERE customer_id = $1
		ORDER BY updated_at DESC
	`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tickets []SupportTicket
	for rows.Next() {
		var t SupportTicket
		if err := rows.Scan(&t.ID, &t.CustomerID, &t.Subject, &t.Status, &t.Priority, &t.Category, &t.CreatedAt, &t.UpdatedAt, &t.ClosedAt); err != nil {
			continue
		}
		tickets = append(tickets, t)
	}
	return tickets, nil
}

// AddTicketMessage adds a message to a ticket
func (s *Service) AddTicketMessage(ticketID, customerID int, message, senderType string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Verify ticket belongs to customer (unless admin)
	if senderType == "customer" {
		var count int
		err = tx.QueryRow(`SELECT COUNT(*) FROM rental_tickets WHERE id = $1 AND customer_id = $2`, ticketID, customerID).Scan(&count)
		if err != nil || count == 0 {
			return fmt.Errorf("ticket not found")
		}
	}

	// Add message
	_, err = tx.Exec(`
		INSERT INTO rental_ticket_messages (ticket_id, sender_type, sender_id, message)
		VALUES ($1, $2, $3, $4)
	`, ticketID, senderType, customerID, message)
	if err != nil {
		return err
	}

	// Update ticket timestamp and reopen if closed
	_, err = tx.Exec(`
		UPDATE rental_tickets
		SET updated_at = NOW(), status = CASE WHEN status = 'closed' THEN 'open' ELSE status END
		WHERE id = $1
	`, ticketID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// CloseTicket closes a ticket
func (s *Service) CloseTicket(ticketID, customerID int) error {
	result, err := s.db.Exec(`
		UPDATE rental_tickets
		SET status = 'closed', closed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND customer_id = $2 AND status != 'closed'
	`, ticketID, customerID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("ticket not found or already closed")
	}
	return nil
}

// sendTicketNotification sends email notification for new ticket
func (s *Service) sendTicketNotification(ticket *SupportTicket, message string) {
	if s.email == nil || s.email.config == nil || s.email.config.SMTPHost == "" {
		return
	}

	customer, err := s.GetCustomerByID(ticket.CustomerID)
	if err != nil {
		return
	}

	adminEmail := s.getSettingString("admin_alert_email", "dev@bitcoincashii.org")
	subject := fmt.Sprintf("[HashForge Ticket #%d] %s", ticket.ID, ticket.Subject)
	body := fmt.Sprintf(`New support ticket from %s

Ticket #%d
Category: %s
Subject: %s

Message:
%s

---
View in admin panel: %s/admin/tickets/%d
`, customer.Email, ticket.ID, ticket.Category, ticket.Subject, message, s.email.config.BaseURL, ticket.ID)

	if err := s.email.sendEmail(adminEmail, subject, body); err != nil {
		log.Printf("Failed to send ticket notification: %v", err)
	}
}

// SyncBraiinsPayments queries Braiins for completed orders and creates payment records
// for orders that haven't been tracked yet
func (s *Service) SyncBraiinsPayments() error {
	// Fetch completed bids from Braiins (last 100)
	completedBids, err := s.braiins.GetCompletedBids(100)
	if err != nil {
		return fmt.Errorf("failed to fetch completed bids: %w", err)
	}

	log.Printf("BraiinsPayments: Found %d completed bids to process", len(completedBids))

	// Regex to extract order ID from memo: "Forge Pool rental order #123"
	orderIDRegex := regexp.MustCompile(`#(\d+)`)

	var insertedCount, skippedZero, skippedExists int
	for _, bid := range completedBids {
		// Skip if no amount was consumed
		if bid.AmountSpentSat == 0 {
			skippedZero++
			continue
		}

		// Check if we already have a payment record for this bid
		var exists bool
		err := s.db.QueryRow(`
			SELECT EXISTS(SELECT 1 FROM braiins_payments WHERE braiins_bid_id = $1)
		`, bid.ID).Scan(&exists)
		if err != nil {
			log.Printf("BraiinsPayments: Error checking bid %s: %v", bid.ID, err)
			continue
		}
		if exists {
			skippedExists++
			continue // Already tracked
		}

		// Try to find the rental order ID
		var rentalOrderID int

		// First try cl_order_id (format: FP-123 or FP-123-ext)
		if bid.ClOrderID != "" && strings.HasPrefix(bid.ClOrderID, "FP-") {
			parts := strings.Split(bid.ClOrderID, "-")
			if len(parts) >= 2 {
				fmt.Sscanf(parts[1], "%d", &rentalOrderID)
			}
		}

		// Fallback to parsing memo
		if rentalOrderID == 0 && bid.Memo != "" {
			matches := orderIDRegex.FindStringSubmatch(bid.Memo)
			if len(matches) >= 2 {
				fmt.Sscanf(matches[1], "%d", &rentalOrderID)
			}
		}

		// Verify the order exists and matches
		if rentalOrderID > 0 {
			var dbBidID string
			err := s.db.QueryRow(`
				SELECT braiins_bid_id FROM rental_orders WHERE id = $1
			`, rentalOrderID).Scan(&dbBidID)
			if err != nil || dbBidID != bid.ID {
				// Mismatch or not found - don't link
				log.Printf("BraiinsPayments: Order %d doesn't match bid %s (db has %s)", rentalOrderID, bid.ID, dbBidID)
				rentalOrderID = 0
			}
		}

		// Create payment record
		var insertedID int
		err = s.db.QueryRow(`
			INSERT INTO braiins_payments (braiins_bid_id, rental_order_id, amount_consumed_sat, fee_paid_sat)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (braiins_bid_id) DO NOTHING
			RETURNING id
		`, bid.ID, sql.NullInt64{Int64: int64(rentalOrderID), Valid: rentalOrderID > 0}, bid.AmountSpentSat, bid.FeePaidSat).Scan(&insertedID)

		if err != nil && err != sql.ErrNoRows {
			log.Printf("BraiinsPayments: Error inserting payment for bid %s: %v", bid.ID, err)
			continue
		}

		if insertedID > 0 {
			insertedCount++
			log.Printf("BraiinsPayments: Created payment record #%d for bid %s (order %d, amount %d sats)",
				insertedID, bid.ID, rentalOrderID, bid.AmountSpentSat)
		}
	}

	log.Printf("BraiinsPayments: Sync complete - inserted=%d, skipped_zero=%d, skipped_exists=%d",
		insertedCount, skippedZero, skippedExists)
	return nil
}

// GetUnpaidBraiinsOrders returns all unpaid Braiins orders with their amounts
func (s *Service) GetUnpaidBraiinsOrders() ([]map[string]interface{}, error) {
	rows, err := s.db.Query(`
		SELECT bp.id, bp.braiins_bid_id, bp.rental_order_id, bp.amount_consumed_sat, bp.fee_paid_sat, bp.created_at
		FROM braiins_payments bp
		WHERE bp.paid_at IS NULL
		ORDER BY bp.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []map[string]interface{}
	for rows.Next() {
		var id int
		var bidID string
		var orderID sql.NullInt64
		var amount, fee int64
		var createdAt time.Time
		if err := rows.Scan(&id, &bidID, &orderID, &amount, &fee, &createdAt); err != nil {
			continue
		}
		orders = append(orders, map[string]interface{}{
			"id":             id,
			"braiins_bid_id": bidID,
			"order_id":       orderID.Int64,
			"amount_sat":     amount,
			"fee_sat":        fee,
			"created_at":     createdAt,
		})
	}
	return orders, nil
}

// GetBraiinsPaymentsSummary returns a summary of Braiins payments
func (s *Service) GetBraiinsPaymentsSummary() (map[string]interface{}, error) {
	var totalUnpaid, countUnpaid int64
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(amount_consumed_sat), 0), COUNT(*)
		FROM braiins_payments
		WHERE paid_at IS NULL
	`).Scan(&totalUnpaid, &countUnpaid)
	if err != nil {
		return nil, err
	}

	var totalPaid, countPaid int64
	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(amount_consumed_sat), 0), COUNT(*)
		FROM braiins_payments
		WHERE paid_at IS NOT NULL
	`).Scan(&totalPaid, &countPaid)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"unpaid_count":     countUnpaid,
		"unpaid_total_sat": totalUnpaid,
		"unpaid_total_btc": fmt.Sprintf("%.8f", float64(totalUnpaid)/100000000),
		"paid_count":       countPaid,
		"paid_total_sat":   totalPaid,
		"paid_total_btc":   fmt.Sprintf("%.8f", float64(totalPaid)/100000000),
	}, nil
}

// MarkBraiinsPaymentPaid marks a Braiins payment as paid
func (s *Service) MarkBraiinsPaymentPaid(paymentID int, txid string) error {
	result, err := s.db.Exec(`
		UPDATE braiins_payments
		SET paid_at = NOW(), btc_txid = $1
		WHERE id = $2 AND paid_at IS NULL
	`, txid, paymentID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("payment not found or already paid")
	}
	return nil
}

// startBraiinsPaymentSync starts a background goroutine to sync payments periodically
func (s *Service) startBraiinsPaymentSync() {
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		// Run immediately on startup
		if err := s.SyncBraiinsPayments(); err != nil {
			log.Printf("BraiinsPayments: Sync error: %v", err)
		}
		// Process any unpaid orders
		s.ProcessBraiinsPayments()

		for range ticker.C {
			if err := s.SyncBraiinsPayments(); err != nil {
				log.Printf("BraiinsPayments: Sync error: %v", err)
			}
			// Process any unpaid orders
			s.ProcessBraiinsPayments()
		}
	}()
}

// ProcessBraiinsPayments sends BTC to Braiins for all unpaid completed orders
func (s *Service) ProcessBraiinsPayments() {
	// Check if any signing method is available
	if s.remoteSigner == nil && s.hotWallet == nil {
		log.Printf("BraiinsPayments: Skipping payment processing - no signing method configured")
		return
	}
	if s.config.BraiinsDepositAddress == "" {
		log.Printf("BraiinsPayments: Skipping payment processing - no deposit address configured")
		return
	}

	// Get all unpaid payments
	rows, err := s.db.Query(`
		SELECT id, braiins_bid_id, rental_order_id, amount_consumed_sat
		FROM braiins_payments
		WHERE paid_at IS NULL AND amount_consumed_sat > 0
		ORDER BY created_at ASC
	`)
	if err != nil {
		log.Printf("BraiinsPayments: Error fetching unpaid orders: %v", err)
		return
	}
	defer rows.Close()

	// Collect all unpaid payments
	type unpaidPayment struct {
		ID        int
		BidID     string
		OrderID   sql.NullInt64
		AmountSat int64
	}
	var payments []unpaidPayment

	for rows.Next() {
		var p unpaidPayment
		if err := rows.Scan(&p.ID, &p.BidID, &p.OrderID, &p.AmountSat); err != nil {
			continue
		}
		payments = append(payments, p)
	}

	if len(payments) == 0 {
		return
	}

	// Calculate total to pay
	var totalSat int64
	for _, p := range payments {
		totalSat += p.AmountSat
	}

	log.Printf("BraiinsPayments: Processing %d unpaid orders, total %d sats", len(payments), totalSat)

	// Get the current highest derivation index from database
	var maxIndex int
	err = s.db.QueryRow(`SELECT COALESCE(MAX(derivation_index), 0) FROM rental_addresses`).Scan(&maxIndex)
	if err != nil {
		log.Printf("BraiinsPayments: Failed to get max derivation index: %v", err)
		maxIndex = 10
	}
	maxIndex += 5

	// Send single consolidated payment to Braiins
	var txid string
	if s.remoteSigner != nil {
		txid, err = s.sendViaRemoteSigner(s.config.BraiinsDepositAddress, totalSat, maxIndex)
	} else if s.hotWallet != nil {
		txid, err = s.hotWallet.SendToAddress(s.config.BraiinsDepositAddress, totalSat, maxIndex)
	} else {
		log.Printf("BraiinsPayments: No signing method available")
		return
	}
	if err != nil {
		log.Printf("BraiinsPayments: FAILED to send %d sats to Braiins: %v", totalSat, err)
		// Log to audit table
		s.db.Exec(`
			INSERT INTO rental_audit_log (event_type, details, created_at)
			VALUES ('braiins_payment_failed', $1, NOW())
		`, fmt.Sprintf("Failed to send %d sats for %d orders: %v", totalSat, len(payments), err))
		return
	}

	log.Printf("BraiinsPayments: SUCCESS - sent %d sats to Braiins, txid: %s", totalSat, txid)

	// Mark all payments as paid
	for _, p := range payments {
		_, err := s.db.Exec(`
			UPDATE braiins_payments
			SET paid_at = NOW(), btc_txid = $1
			WHERE id = $2
		`, txid, p.ID)
		if err != nil {
			log.Printf("BraiinsPayments: Error marking payment %d as paid: %v", p.ID, err)
		}
	}

	// Log success to audit table
	s.db.Exec(`
		INSERT INTO rental_audit_log (event_type, details, created_at)
		VALUES ('braiins_payment_success', $1, NOW())
	`, fmt.Sprintf("Sent %d sats to Braiins for %d orders, txid: %s", totalSat, len(payments), txid))
}
