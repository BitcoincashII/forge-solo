package rental

import (
	"time"
)

// Customer represents a rental customer
type Customer struct {
	ID            int       `json:"id" db:"id"`
	APIKey        string    `json:"-" db:"api_key"` // Hidden from JSON, used for API access
	Email         string    `json:"email,omitempty" db:"email"`
	PasswordHash  string    `json:"-" db:"password_hash"` // Hidden from JSON
	EmailVerified bool      `json:"email_verified" db:"email_verified"`
	BCH2Address   string    `json:"bch2_address,omitempty" db:"bch2_address"` // BCH2 payout address
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
}

// Address represents a BTC deposit address for a customer
type Address struct {
	ID              int       `json:"id" db:"id"`
	CustomerID      int       `json:"customer_id" db:"customer_id"`
	BTCAddress      string    `json:"btc_address" db:"btc_address"`
	DerivationIndex int       `json:"derivation_index" db:"derivation_index"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
}

// LedgerEntry represents a balance transaction
type LedgerEntry struct {
	ID           int       `json:"id" db:"id"`
	CustomerID   int       `json:"customer_id" db:"customer_id"`
	TxType       string    `json:"tx_type" db:"tx_type"`
	AmountSat    int64     `json:"amount_sat" db:"amount_sat"`
	BTCTxID      string    `json:"btc_txid,omitempty" db:"btc_txid"`
	OrderID      *int      `json:"order_id,omitempty" db:"order_id"`
	Memo         string    `json:"memo,omitempty" db:"memo"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	RunningTotal int64     `json:"running_total,omitempty"` // Calculated field for display
}

// Order represents a hashrate rental order
type Order struct {
	ID              int        `json:"id" db:"id"`
	CustomerID      int        `json:"customer_id" db:"customer_id"`
	BraiinsBidID    string     `json:"braiins_bid_id,omitempty" db:"braiins_bid_id"`
	TargetPoolURL   string     `json:"target_pool_url" db:"target_pool_url"`
	TargetIdentity  string     `json:"target_identity" db:"target_identity"`
	BudgetSat       int64      `json:"budget_sat" db:"budget_sat"`              // What customer paid
	PriceSat        int64      `json:"price_sat" db:"price_sat"`                // Pool price (with margin)
	MarketPriceSat  int64      `json:"market_price_sat" db:"market_price_sat"`  // Braiins market price
	BraiinsBudgetSat int64     `json:"braiins_budget_sat" db:"braiins_budget_sat"` // What we send to Braiins
	PoolMarginSat   int64      `json:"pool_margin_sat" db:"pool_margin_sat"`    // Our profit
	SpeedLimitPH    float64    `json:"speed_limit_ph" db:"speed_limit_ph"`
	MarginPct       float64    `json:"margin_pct" db:"margin_pct"`
	MiningMode      string     `json:"mining_mode" db:"mining_mode"`            // pplns or solo
	Status          string     `json:"status" db:"status"`
	AmountSpentSat  int64      `json:"amount_spent_sat" db:"amount_spent_sat"`
	ErrorMessage    string     `json:"error_message,omitempty" db:"error_message"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty" db:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty" db:"completed_at"`
}

// Deposit represents a tracked BTC deposit
type Deposit struct {
	ID            int        `json:"id" db:"id"`
	BTCTxID       string     `json:"btc_txid" db:"btc_txid"`
	BTCAddress    string     `json:"btc_address" db:"btc_address"`
	AmountSat     int64      `json:"amount_sat" db:"amount_sat"`
	Confirmations int        `json:"confirmations" db:"confirmations"`
	Credited      bool       `json:"credited" db:"credited"`
	CustomerID    *int       `json:"customer_id,omitempty" db:"customer_id"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	ConfirmedAt   *time.Time `json:"confirmed_at,omitempty" db:"confirmed_at"`
}

// CustomerBalance represents a customer's current balance
type CustomerBalance struct {
	CustomerID int   `json:"customer_id"`
	BalanceSat int64 `json:"balance_sat"`
}

// Order statuses
const (
	OrderStatusPending   = "pending"
	OrderStatusActive    = "active"
	OrderStatusCompleted = "completed"
	OrderStatusFailed    = "failed"
	OrderStatusCancelled = "cancelled"
)

// Ledger transaction types
const (
	TxTypeDeposit     = "deposit"
	TxTypeOrderCharge = "order_charge"
	TxTypeOrderRefund = "order_refund"
	TxTypeWithdrawal  = "withdrawal"
)

// API Request/Response types

type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type RegisterResponse struct {
	BTCAddress string `json:"btc_address"`
	Message    string `json:"message,omitempty"`
}

type BalanceResponse struct {
	BalanceSat     int64  `json:"balance_sat"`
	BalanceBTC     string `json:"balance_btc"`
	PendingDeposit int64  `json:"pending_deposit_sat"`
}

type PlaceOrderRequest struct {
	WorkerName   string  `json:"worker_name,omitempty"`    // Optional worker name for identification
	BudgetSat    int64   `json:"budget_sat"`               // Max BTC to spend
	SpeedLimitPH float64 `json:"speed_limit_ph,omitempty"` // Hashrate limit (default 1)
	MiningMode   string  `json:"mining_mode,omitempty"`    // "pplns" or "solo" (default pplns)
}

type PlaceOrderResponse struct {
	OrderID    int    `json:"order_id"`
	Status     string `json:"status"`
	BudgetSat  int64  `json:"budget_sat"`
	PriceSat   int64  `json:"price_sat"`
	MiningMode string `json:"mining_mode"`
	Message    string `json:"message"`
}

type ExtendOrderRequest struct {
	AdditionalBudgetSat int64 `json:"additional_budget_sat"` // Additional BTC to add
}

type OrderResponse struct {
	Order           *Order  `json:"order"`
	ProgressPct     float64 `json:"progress_pct"`
	CurrentSpeedPH  float64 `json:"current_speed_ph,omitempty"`
	AmountRemaining int64   `json:"amount_remaining_sat"`
}

type PriceResponse struct {
	BestAskSat    int64   `json:"best_ask_sat"`      // Current best ask on Braiins
	PoolPriceSat  int64   `json:"pool_price_sat"`    // Price with margin
	MarginPct     float64 `json:"margin_pct"`
	AvailablePH   float64 `json:"available_ph"`      // Available hashrate
	PriceUnit     string  `json:"price_unit"`        // "sat/EH/day"
}

type WithdrawRequest struct {
	BTCAddress string `json:"btc_address"`
	AmountSat  int64  `json:"amount_sat,omitempty"` // 0 = withdraw all
	TOTPCode   string `json:"totp_code"`            // Required - 2FA code or backup code
}

type WithdrawResponse struct {
	TxID      string `json:"txid"`
	AmountSat int64  `json:"amount_sat"`
	Status    string `json:"status"`
}

// Braiins API types

type BraiinsBid struct {
	ID              string  `json:"id"`
	ClOrderID       string  `json:"cl_order_id"`
	Status          string  `json:"status"`
	PriceSat        int64   `json:"price_sat"`
	AmountSat       int64   `json:"amount_sat"`
	SpeedLimitPH    float64 `json:"speed_limit_ph"`
	AvgSpeedPH      float64 `json:"avg_speed_ph"`
	ProgressPct     float64 `json:"progress_pct"`
	AmountSpentSat  int64   `json:"amount_spent_sat"`
	AmountRemaining int64   `json:"amount_remaining_sat"`
	FeePaidSat      int64   `json:"fee_paid_sat"`
	Memo            string  `json:"memo"`
	LastPauseReason string  `json:"last_pause_reason"`
}

type BraiinsOrderbook struct {
	BestAskSat  int64   `json:"best_ask_sat"`
	BestBidSat  int64   `json:"best_bid_sat"`
	AvailablePH float64 `json:"available_ph"`
}

type BraiinsBalance struct {
	TotalSat     int64 `json:"total_sat"`
	AvailableSat int64 `json:"available_sat"`
	BlockedSat   int64 `json:"blocked_sat"`
}

// Withdrawal represents a withdrawal request
type Withdrawal struct {
	ID              int        `json:"id" db:"id"`
	CustomerID      int        `json:"customer_id" db:"customer_id"`
	AmountSat       int64      `json:"amount_sat" db:"amount_sat"`
	BTCAddress      string     `json:"btc_address" db:"btc_address"`
	Status          string     `json:"status" db:"status"`
	TxID            string     `json:"txid,omitempty" db:"txid"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
	ProcessedAt     *time.Time `json:"processed_at,omitempty" db:"processed_at"`
	ProcessedBy     string     `json:"processed_by,omitempty" db:"processed_by"`
	RejectionReason string     `json:"rejection_reason,omitempty" db:"rejection_reason"`
}

// Withdrawal statuses
const (
	WithdrawalStatusPending   = "pending"
	WithdrawalStatusApproved  = "approved"
	WithdrawalStatusCompleted = "completed"
	WithdrawalStatusRejected  = "rejected"
)

// Admin represents an admin user
type Admin struct {
	ID           int        `json:"id" db:"id"`
	Username     string     `json:"username" db:"username"`
	PasswordHash string     `json:"-" db:"password_hash"`
	APIKey       string     `json:"-" db:"api_key"` // Hidden from JSON to prevent credential leak
	Role         string     `json:"role" db:"role"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	LastLogin    *time.Time `json:"last_login,omitempty" db:"last_login"`
}

// AuditLog represents an admin action audit entry
type AuditLog struct {
	ID         int       `json:"id" db:"id"`
	AdminID    *int      `json:"admin_id,omitempty" db:"admin_id"`
	Action     string    `json:"action" db:"action"`
	EntityType string    `json:"entity_type,omitempty" db:"entity_type"`
	EntityID   *int      `json:"entity_id,omitempty" db:"entity_id"`
	Details    string    `json:"details,omitempty" db:"details"`
	IPAddress  string    `json:"ip_address,omitempty" db:"ip_address"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
}

// SystemStats for admin dashboard
type SystemStats struct {
	TotalCustomers      int   `json:"total_customers"`
	TotalDeposits       int   `json:"total_deposits"`
	TotalDepositsSat    int64 `json:"total_deposits_sat"`
	TotalOrders         int   `json:"total_orders"`
	ActiveOrders        int   `json:"active_orders"`
	TotalSpentSat       int64 `json:"total_spent_sat"`
	PendingWithdrawals  int   `json:"pending_withdrawals"`
	TotalWithdrawnSat   int64 `json:"total_withdrawn_sat"`
	TotalActiveHashrate int   `json:"total_active_hashrate"`
}

// WithdrawalListResponse for listing withdrawals
type WithdrawalListResponse struct {
	Withdrawals []Withdrawal `json:"withdrawals"`
	Total       int          `json:"total"`
}

// CustomerListResponse for admin listing customers
type CustomerListResponse struct {
	Customers []CustomerWithBalance `json:"customers"`
	Total     int                   `json:"total"`
}

// CustomerWithBalance extends Customer with balance info
type CustomerWithBalance struct {
	Customer
	BalanceSat int64  `json:"balance_sat"`
	BTCAddress string `json:"btc_address"`
}

// OrderListResponse for listing orders
type OrderListResponse struct {
	Orders []Order `json:"orders"`
	Total  int     `json:"total"`
}

// Setting represents a system setting
type Setting struct {
	Key       string    `json:"key" db:"key"`
	Value     string    `json:"value" db:"value"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// ============================================================================
// 2FA Types
// ============================================================================

// TwoFactorSetupResponse is returned when setting up 2FA
type TwoFactorSetupResponse struct {
	Secret     string `json:"secret"`      // Base32 secret for manual entry
	QRCodeURI  string `json:"qr_code_uri"` // otpauth:// URI for QR code
	BackupCode string `json:"backup_code"` // One backup code shown during setup
}

// TwoFactorVerifyRequest is used to verify/enable 2FA
type TwoFactorVerifyRequest struct {
	Code string `json:"code"` // 6-digit TOTP code
}

// TwoFactorStatusResponse shows 2FA status
type TwoFactorStatusResponse struct {
	Enabled       bool       `json:"enabled"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	BackupCodes   int        `json:"backup_codes_remaining"`
}

// BackupCodesResponse returns newly generated backup codes
type BackupCodesResponse struct {
	Codes []string `json:"codes"` // Plain text codes (only shown once)
}

// LoginRequest for session-based auth with 2FA
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code,omitempty"` // Required if 2FA enabled
}

// LoginResponse returns a session token
type LoginResponse struct {
	SessionToken string    `json:"session_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Requires2FA  bool      `json:"requires_2fa,omitempty"` // If 2FA needed but not provided
}

// Session represents an authenticated session
type Session struct {
	ID           int       `json:"id" db:"id"`
	CustomerID   int       `json:"customer_id" db:"customer_id"`
	SessionToken string    `json:"session_token" db:"session_token"`
	Verified2FA  bool      `json:"verified_2fa" db:"verified_2fa"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	ExpiresAt    time.Time `json:"expires_at" db:"expires_at"`
	LastUsedAt   time.Time `json:"last_used_at" db:"last_used_at"`
	IPAddress    string    `json:"ip_address,omitempty" db:"ip_address"`
}

// BackupCode represents a one-time recovery code
type BackupCode struct {
	ID         int        `json:"id" db:"id"`
	CustomerID int        `json:"customer_id" db:"customer_id"`
	CodeHash   string     `json:"-" db:"code_hash"`
	Used       bool       `json:"used" db:"used"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	UsedAt     *time.Time `json:"used_at,omitempty" db:"used_at"`
}

// ============================================================================
// Admin View Types
// ============================================================================

// AdminWithdrawalView extends Withdrawal with customer email
type AdminWithdrawalView struct {
	Withdrawal
	CustomerEmail string `json:"customer_email"`
}

// AdminOrderView extends Order with customer email
type AdminOrderView struct {
	Order
	CustomerEmail string `json:"customer_email"`
}

// AdminCustomerView extends Customer with aggregated data
type AdminCustomerView struct {
	ID           int       `json:"id"`
	Email        string    `json:"email"`
	BalanceSat   int64     `json:"balance_sat"`
	BalanceBTC   string    `json:"balance_btc"`
	OrderCount   int       `json:"order_count"`
	TwoFAEnabled bool      `json:"two_fa_enabled"`
	CreatedAt    time.Time `json:"created_at"`
}

// AdminStats extends SystemStats with BTC formatting
type AdminStats struct {
	TotalCustomers     int    `json:"total_customers"`
	ActiveOrders       int    `json:"active_orders"`
	TotalVolumeBTC     string `json:"total_volume_btc"`
	PendingWithdrawals int    `json:"pending_withdrawals"`
}

// ============================================================================
// Security Events
// ============================================================================

// SecurityEvent represents a security audit log entry
type SecurityEvent struct {
	ID        int       `json:"id" db:"id"`
	EventType string    `json:"event_type" db:"event_type"`
	IPAddress string    `json:"ip_address" db:"ip_address"`
	UserAgent string    `json:"user_agent" db:"user_agent"`
	Details   string    `json:"details" db:"details"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// ============================================================================
// Email Preferences
// ============================================================================

// EmailPreferences represents user email notification settings
type EmailPreferences struct {
	CustomerID        int  `json:"customer_id" db:"customer_id"`
	NotifyDeposits    bool `json:"notify_deposits" db:"notify_deposits"`
	NotifyOrders      bool `json:"notify_orders" db:"notify_orders"`
	NotifyWithdrawals bool `json:"notify_withdrawals" db:"notify_withdrawals"`
	NotifySecurity    bool `json:"notify_security" db:"notify_security"`
	NotifyMarketing   bool `json:"notify_marketing" db:"notify_marketing"`
}

// ============================================================================
// Withdrawal Whitelist
// ============================================================================

// WhitelistedAddress represents a pre-approved withdrawal address
type WhitelistedAddress struct {
	ID         int        `json:"id" db:"id"`
	CustomerID int        `json:"customer_id" db:"customer_id"`
	BTCAddress string     `json:"btc_address" db:"btc_address"`
	Label      string     `json:"label" db:"label"`
	Verified   bool       `json:"verified" db:"verified"`
	VerifiedAt *time.Time `json:"verified_at" db:"verified_at"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
}

// ============================================================================
// Notifications
// ============================================================================

// Notification represents an in-app notification
type Notification struct {
	ID        int       `json:"id" db:"id"`
	Type      string    `json:"type" db:"type"`
	Title     string    `json:"title" db:"title"`
	Message   string    `json:"message" db:"message"`
	Link      string    `json:"link" db:"link"`
	Read      bool      `json:"read" db:"read"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// NotificationCounts for header badge
type NotificationCounts struct {
	Unread int `json:"unread"`
	Total  int `json:"total"`
}

// ============================================================================
// Session Management
// ============================================================================

// SessionView for displaying active sessions
type SessionView struct {
	ID         int       `json:"id"`
	DeviceName string    `json:"device_name"`
	IPAddress  string    `json:"ip_address"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	IsCurrent  bool      `json:"is_current"`
}

// ============================================================================
// Contact & Support Types
// ============================================================================

// ContactMessage represents a contact form submission
type ContactMessage struct {
	ID         int        `json:"id" db:"id"`
	CustomerID *int       `json:"customer_id,omitempty" db:"customer_id"`
	Name       string     `json:"name" db:"name"`
	Email      string     `json:"email" db:"email"`
	Subject    string     `json:"subject" db:"subject"`
	Message    string     `json:"message" db:"message"`
	IPAddress  string     `json:"ip_address" db:"ip_address"`
	Status     string     `json:"status" db:"status"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	RepliedAt  *time.Time `json:"replied_at,omitempty" db:"replied_at"`
}

// SupportTicket represents a support ticket
type SupportTicket struct {
	ID         int        `json:"id" db:"id"`
	CustomerID int        `json:"customer_id" db:"customer_id"`
	Subject    string     `json:"subject" db:"subject"`
	Status     string     `json:"status" db:"status"`
	Priority   string     `json:"priority" db:"priority"`
	Category   string     `json:"category" db:"category"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at" db:"updated_at"`
	ClosedAt   *time.Time `json:"closed_at,omitempty" db:"closed_at"`
}

// TicketMessage represents a message in a support ticket
type TicketMessage struct {
	ID         int       `json:"id" db:"id"`
	TicketID   int       `json:"ticket_id" db:"ticket_id"`
	SenderType string    `json:"sender_type" db:"sender_type"` // customer or admin
	SenderID   int       `json:"sender_id" db:"sender_id"`
	Message    string    `json:"message" db:"message"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
}

// BalanceAlert represents customer balance alert settings
type BalanceAlert struct {
	ID            int        `json:"id" db:"id"`
	CustomerID    int        `json:"customer_id" db:"customer_id"`
	ThresholdSat  int64      `json:"threshold_sat" db:"threshold_sat"`
	NotifyEmail   bool       `json:"notify_email" db:"notify_email"`
	NotifyInApp   bool       `json:"notify_inapp" db:"notify_inapp"`
	LastAlertedAt *time.Time `json:"last_alerted_at,omitempty" db:"last_alerted_at"`
	Enabled       bool       `json:"enabled" db:"enabled"`
}

// EmailChangeRequest represents a pending email change
type EmailChangeRequest struct {
	ID           int        `json:"id" db:"id"`
	CustomerID   int        `json:"customer_id" db:"customer_id"`
	OldEmail     string     `json:"old_email" db:"old_email"`
	NewEmail     string     `json:"new_email" db:"new_email"`
	Token        string     `json:"-" db:"token"`
	ConfirmedOld bool       `json:"confirmed_old" db:"confirmed_old"`
	ConfirmedNew bool       `json:"confirmed_new" db:"confirmed_new"`
	ExpiresAt    time.Time  `json:"expires_at" db:"expires_at"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty" db:"completed_at"`
}
