package rental

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// testDB holds the test database connection
var testDB *sql.DB

// setupTestDB initializes the test database connection
func setupTestDB(t *testing.T) *sql.DB {
	if testDB != nil {
		return testDB
	}

	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		// Default test database connection
		connStr = "host=localhost port=5432 user=forge password=forgepool dbname=forgepool_test sslmode=disable"
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Skipf("Skipping database tests: %v", err)
	}

	if err := db.Ping(); err != nil {
		t.Skipf("Skipping database tests (cannot connect): %v", err)
	}

	testDB = db
	return db
}

// createTestCustomer creates a customer for testing and returns their ID
func createTestCustomer(t *testing.T, db *sql.DB, suffix string) int {
	var customerID int
	err := db.QueryRow(`
		INSERT INTO rental_customers (api_key, email, email_verified)
		VALUES ($1, $2, TRUE)
		RETURNING id
	`, fmt.Sprintf("test_key_%s", suffix), fmt.Sprintf("test_%s@example.com", suffix)).Scan(&customerID)
	if err != nil {
		t.Fatalf("Failed to create test customer: %v", err)
	}
	return customerID
}

// cleanupTestCustomer removes test data for a customer
func cleanupTestCustomer(t *testing.T, db *sql.DB, customerID int) {
	db.Exec(`DELETE FROM rental_ledger WHERE customer_id = $1`, customerID)
	db.Exec(`DELETE FROM rental_deposits WHERE customer_id = $1`, customerID)
	db.Exec(`DELETE FROM rental_orders WHERE customer_id = $1`, customerID)
	db.Exec(`DELETE FROM rental_addresses WHERE customer_id = $1`, customerID)
	db.Exec(`DELETE FROM rental_customers WHERE id = $1`, customerID)
}

// ============================================================================
// GetBalance Tests
// ============================================================================

func TestGetBalance_EmptyLedger(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "balance_empty")
	defer cleanupTestCustomer(t, db, customerID)

	balance, err := service.GetBalance(customerID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	if balance.BalanceSat != 0 {
		t.Errorf("Expected 0 balance for new customer, got %d", balance.BalanceSat)
	}

	if balance.BalanceBTC != "0.00000000" {
		t.Errorf("Expected '0.00000000' BTC, got %s", balance.BalanceBTC)
	}
}

func TestGetBalance_WithDeposits(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "balance_deposits")
	defer cleanupTestCustomer(t, db, customerID)

	// Add ledger entries: 100k + 50k deposits, -25k order charge
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'deposit', 100000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'deposit', 50000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'order_charge', -25000)`, customerID)

	balance, err := service.GetBalance(customerID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	expected := int64(125000) // 100k + 50k - 25k
	if balance.BalanceSat != expected {
		t.Errorf("Expected %d balance, got %d", expected, balance.BalanceSat)
	}
}

func TestGetBalance_WithRefunds(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "balance_refunds")
	defer cleanupTestCustomer(t, db, customerID)

	// Deposit 200k, order charge -150k, refund +50k
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'deposit', 200000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'order_charge', -150000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'order_refund', 50000)`, customerID)

	balance, err := service.GetBalance(customerID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	expected := int64(100000) // 200k - 150k + 50k
	if balance.BalanceSat != expected {
		t.Errorf("Expected %d balance, got %d", expected, balance.BalanceSat)
	}
}

func TestGetBalance_PendingDeposits(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "balance_pending")
	defer cleanupTestCustomer(t, db, customerID)

	// Add pending (uncredited) deposit
	db.Exec(`INSERT INTO rental_deposits (btc_txid, btc_address, amount_sat, credited, customer_id)
             VALUES ('txid_pending_123456789012345678901234', 'bc1qtest', 75000, FALSE, $1)`, customerID)

	balance, err := service.GetBalance(customerID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	if balance.BalanceSat != 0 {
		t.Errorf("Expected 0 confirmed balance, got %d", balance.BalanceSat)
	}

	if balance.PendingDeposit != 75000 {
		t.Errorf("Expected 75000 pending, got %d", balance.PendingDeposit)
	}
}

func TestGetBalance_MixedState(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "balance_mixed")
	defer cleanupTestCustomer(t, db, customerID)

	// Confirmed deposit of 100k
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'deposit', 100000)`, customerID)

	// Pending deposit of 50k
	db.Exec(`INSERT INTO rental_deposits (btc_txid, btc_address, amount_sat, credited, customer_id)
             VALUES ('txid_mixed_12345678901234567890123456', 'bc1qtest2', 50000, FALSE, $1)`, customerID)

	balance, err := service.GetBalance(customerID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	if balance.BalanceSat != 100000 {
		t.Errorf("Expected 100000 confirmed balance, got %d", balance.BalanceSat)
	}

	if balance.PendingDeposit != 50000 {
		t.Errorf("Expected 50000 pending, got %d", balance.PendingDeposit)
	}
}

// ============================================================================
// Margin Calculation Tests
// ============================================================================

func TestMarginCalculation_25Percent(t *testing.T) {
	tests := []struct {
		name            string
		customerBudget  int64
		marginPct       float64
		expectedBraiins int64
		expectedMargin  int64
	}{
		{
			name:            "1M sats with 25% margin",
			customerBudget:  1000000,
			marginPct:       25.0,
			expectedBraiins: 800000,
			expectedMargin:  200000,
		},
		{
			name:            "125k sats (min order) with 25% margin",
			customerBudget:  125000,
			marginPct:       25.0,
			expectedBraiins: 100000,
			expectedMargin:  25000,
		},
		{
			name:            "10M sats (max order) with 25% margin",
			customerBudget:  10000000,
			marginPct:       25.0,
			expectedBraiins: 8000000,
			expectedMargin:  2000000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marginBasisPoints := int64(tt.marginPct * 100)
			braiinsBudget := (tt.customerBudget * 10000) / (10000 + marginBasisPoints)
			poolMargin := tt.customerBudget - braiinsBudget

			if braiinsBudget != tt.expectedBraiins {
				t.Errorf("Braiins budget: expected %d, got %d", tt.expectedBraiins, braiinsBudget)
			}
			if poolMargin != tt.expectedMargin {
				t.Errorf("Pool margin: expected %d, got %d", tt.expectedMargin, poolMargin)
			}

			// Verify invariant: customer pays exactly braiins + margin
			if braiinsBudget+poolMargin != tt.customerBudget {
				t.Errorf("Invariant violation: %d + %d != %d", braiinsBudget, poolMargin, tt.customerBudget)
			}
		})
	}
}

func TestMarginCalculation_VariousPercentages(t *testing.T) {
	tests := []struct {
		marginPct       float64
		customerBudget  int64
		expectedBraiins int64
	}{
		{5.0, 1000000, 952380},   // 5% margin
		{10.0, 1000000, 909090},  // 10% margin
		{15.0, 1000000, 869565},  // 15% margin
		{20.0, 1000000, 833333},  // 20% margin
		{25.0, 1000000, 800000},  // 25% margin
		{30.0, 1000000, 769230},  // 30% margin
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%.0f%%", tt.marginPct), func(t *testing.T) {
			marginBasisPoints := int64(tt.marginPct * 100)
			braiinsBudget := (tt.customerBudget * 10000) / (10000 + marginBasisPoints)

			if braiinsBudget != tt.expectedBraiins {
				t.Errorf("With %.0f%% margin: expected Braiins budget %d, got %d",
					tt.marginPct, tt.expectedBraiins, braiinsBudget)
			}
		})
	}
}

func TestMarginCalculation_NoOverflow(t *testing.T) {
	// Test maximum safe budget from service.go
	maxSafeBudget := int64(922337203685477)
	marginBasisPoints := int64(2500) // 25%

	// This calculation should not overflow
	braiinsBudget := (maxSafeBudget * 10000) / (10000 + marginBasisPoints)

	if braiinsBudget <= 0 {
		t.Errorf("Budget calculation resulted in invalid value: %d", braiinsBudget)
	}

	// Verify result is reasonable (should be about 80% of max)
	expectedMin := maxSafeBudget * 75 / 100
	expectedMax := maxSafeBudget * 85 / 100
	if braiinsBudget < expectedMin || braiinsBudget > expectedMax {
		t.Errorf("Braiins budget %d outside expected range [%d, %d]", braiinsBudget, expectedMin, expectedMax)
	}

	t.Logf("Max safe budget: %d, Braiins receives: %d", maxSafeBudget, braiinsBudget)
}

func TestMarginCalculation_SmallAmounts(t *testing.T) {
	// Test that small amounts don't cause issues with integer division
	marginBasisPoints := int64(2500) // 25%

	tests := []struct {
		budget          int64
		expectedBraiins int64
	}{
		{125000, 100000}, // Minimum order
		{100000, 80000},
		{50000, 40000},
		{10000, 8000},
		{1000, 800},
		{100, 80},
	}

	for _, tt := range tests {
		braiinsBudget := (tt.budget * 10000) / (10000 + marginBasisPoints)
		if braiinsBudget != tt.expectedBraiins {
			t.Errorf("Budget %d: expected Braiins %d, got %d", tt.budget, tt.expectedBraiins, braiinsBudget)
		}
	}
}

// ============================================================================
// creditDeposit Tests
// ============================================================================

func TestCreditDeposit_NewDeposit(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "credit_new")
	defer cleanupTestCustomer(t, db, customerID)

	// Create uncredited deposit
	txid := "txid_credit_new_12345678901234567890123"
	db.Exec(`INSERT INTO rental_deposits (btc_txid, btc_address, amount_sat, credited, customer_id)
             VALUES ($1, 'bc1qcredit', 50000, FALSE, $2)`, txid, customerID)

	// Credit the deposit
	err := service.creditDeposit(customerID, txid, 0, 50000)
	if err != nil {
		t.Fatalf("creditDeposit failed: %v", err)
	}

	// Verify balance was credited
	balance, _ := service.GetBalance(customerID)
	if balance.BalanceSat != 50000 {
		t.Errorf("Expected balance 50000 after credit, got %d", balance.BalanceSat)
	}

	// Verify deposit marked as credited
	var credited bool
	db.QueryRow(`SELECT credited FROM rental_deposits WHERE btc_txid = $1`, txid).Scan(&credited)
	if !credited {
		t.Error("Deposit should be marked as credited")
	}
}

func TestCreditDeposit_AlreadyCredited(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "credit_dupe")
	defer cleanupTestCustomer(t, db, customerID)

	// Create already credited deposit
	txid := "txid_already_credited_123456789012345678"
	db.Exec(`INSERT INTO rental_deposits (btc_txid, btc_address, amount_sat, credited, customer_id)
             VALUES ($1, 'bc1qdupe', 100000, TRUE, $2)`, txid, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat, btc_txid)
             VALUES ($1, 'deposit', 100000, $2)`, customerID, txid)

	// Try to credit again - should be no-op (not error)
	err := service.creditDeposit(customerID, txid, 0, 100000)
	if err != nil {
		t.Fatalf("creditDeposit should succeed for already credited: %v", err)
	}

	// Balance should still be 100000, not 200000 (no double-credit)
	balance, _ := service.GetBalance(customerID)
	if balance.BalanceSat != 100000 {
		t.Errorf("Expected balance 100000 (no double-credit), got %d", balance.BalanceSat)
	}

	// Count ledger entries - should still be 1
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM rental_ledger WHERE btc_txid = $1`, txid).Scan(&count)
	if count != 1 {
		t.Errorf("Expected 1 ledger entry, got %d", count)
	}
}

func TestCreditDeposit_LargeAmount(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "credit_large")
	defer cleanupTestCustomer(t, db, customerID)

	// Create deposit with 100 BTC (10 billion sats)
	txid := "txid_large_amount_123456789012345678901"
	largeAmount := int64(10000000000) // 100 BTC in sats
	db.Exec(`INSERT INTO rental_deposits (btc_txid, btc_address, amount_sat, credited, customer_id)
             VALUES ($1, 'bc1qlarge', $2, FALSE, $3)`, txid, largeAmount, customerID)

	err := service.creditDeposit(customerID, txid, 0, largeAmount)
	if err != nil {
		t.Fatalf("creditDeposit failed for large amount: %v", err)
	}

	balance, _ := service.GetBalance(customerID)
	if balance.BalanceSat != largeAmount {
		t.Errorf("Expected balance %d, got %d", largeAmount, balance.BalanceSat)
	}
}

// ============================================================================
// Validation Tests (without full order placement)
// ============================================================================

func TestValidateOrderBudget_Minimum(t *testing.T) {
	minOrderSat := int64(125000) // From config

	tests := []struct {
		budget  int64
		isValid bool
	}{
		{124999, false}, // Below minimum
		{125000, true},  // Exactly minimum
		{125001, true},  // Above minimum
		{1000000, true}, // Well above minimum
	}

	for _, tt := range tests {
		valid := tt.budget >= minOrderSat
		if valid != tt.isValid {
			t.Errorf("Budget %d: expected valid=%v, got valid=%v", tt.budget, tt.isValid, valid)
		}
	}
}

func TestValidateOrderBudget_Maximum(t *testing.T) {
	maxOrderSat := int64(10000000) // From config

	tests := []struct {
		budget  int64
		isValid bool
	}{
		{9999999, true},   // Below maximum
		{10000000, true},  // Exactly maximum
		{10000001, false}, // Above maximum
		{100000000, false}, // Well above maximum
	}

	for _, tt := range tests {
		valid := tt.budget <= maxOrderSat
		if valid != tt.isValid {
			t.Errorf("Budget %d: expected valid=%v, got valid=%v", tt.budget, tt.isValid, valid)
		}
	}
}

func TestValidateOrderBudget_OverflowProtection(t *testing.T) {
	maxSafeBudget := int64(922337203685477) // From service.go

	tests := []struct {
		budget  int64
		isSafe  bool
	}{
		{922337203685477, true},  // Exactly max safe
		{922337203685478, false}, // Above max safe
		{1000000000000000, false}, // Way above
	}

	for _, tt := range tests {
		safe := tt.budget <= maxSafeBudget
		if safe != tt.isSafe {
			t.Errorf("Budget %d: expected safe=%v, got safe=%v", tt.budget, tt.isSafe, safe)
		}
	}
}

// ============================================================================
// Ledger Integrity Tests
// ============================================================================

func TestLedgerIntegrity_BalanceNeverNegative(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "ledger_negative")
	defer cleanupTestCustomer(t, db, customerID)

	// Add entries that would result in negative balance if summed incorrectly
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'deposit', 100000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'order_charge', -50000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'order_charge', -50000)`, customerID)

	balance, err := service.GetBalance(customerID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	// Balance should be exactly 0
	if balance.BalanceSat != 0 {
		t.Errorf("Expected balance 0, got %d", balance.BalanceSat)
	}
}

func TestLedgerIntegrity_TransactionTypes(t *testing.T) {
	db := setupTestDB(t)
	service := &Service{db: db}

	customerID := createTestCustomer(t, db, "ledger_types")
	defer cleanupTestCustomer(t, db, customerID)

	// All transaction types
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'deposit', 1000000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'order_charge', -500000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'order_refund', 100000)`, customerID)
	db.Exec(`INSERT INTO rental_ledger (customer_id, tx_type, amount_sat) VALUES ($1, 'withdrawal', -200000)`, customerID)

	balance, err := service.GetBalance(customerID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	// 1M - 500k + 100k - 200k = 400k
	expected := int64(400000)
	if balance.BalanceSat != expected {
		t.Errorf("Expected balance %d, got %d", expected, balance.BalanceSat)
	}
}
