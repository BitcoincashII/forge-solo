package rental

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// APIHandler handles rental API requests
type APIHandler struct {
	service *Service
}

// NewAPIHandler creates a new API handler
func NewAPIHandler(service *Service) *APIHandler {
	return &APIHandler{service: service}
}

// Response helpers
func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func errorResponse(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

// safeErrorResponse logs the full error internally but returns a generic message
// Use this for internal errors that might expose implementation details
func safeErrorResponse(w http.ResponseWriter, status int, internalErr error, publicMsg string) {
	if internalErr != nil {
		log.Printf("API error: %v", internalErr)
	}
	errorResponse(w, status, publicMsg)
}

// sanitizePagination validates and normalizes pagination parameters
// Returns (limit, offset) with safe defaults and bounds
func sanitizePagination(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	// Enforce bounds to prevent DoS
	if limit <= 0 {
		limit = 50 // Default
	} else if limit > 500 {
		limit = 500 // Max
	}
	if offset < 0 {
		offset = 0
	} else if offset > 100000 {
		// Prevent extremely large offsets that could cause performance issues
		// Normal pagination should never exceed this
		offset = 100000
	}

	return limit, offset
}

// getAPIKey extracts API key from request headers only
// SECURITY: Never accept API keys in query parameters - they get logged and cached
func getAPIKey(r *http.Request) string {
	// Check X-API-Key header first
	key := r.Header.Get("X-API-Key")
	if key != "" {
		return key
	}

	// Check Authorization: Bearer header
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	// No API key found - do NOT check query params for security
	return ""
}

// authMiddleware validates API key and adds customer to context
func (h *APIHandler) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Limit request body size to prevent memory exhaustion
		limitRequestBody(r)

		apiKey := getAPIKey(r)
		if apiKey == "" {
			errorResponse(w, http.StatusUnauthorized, "API key required")
			return
		}

		customer, err := h.service.GetCustomerByAPIKey(apiKey)
		if err != nil {
			log.Printf("API auth failed: %v", err)
			errorResponse(w, http.StatusUnauthorized, "Invalid API key")
			return
		}

		// Store customer ID in header for handlers
		r.Header.Set("X-Customer-ID", strconv.Itoa(customer.ID))
		next(w, r)
	}
}

// getCustomerID extracts customer ID from request (set by middleware)
func getCustomerID(r *http.Request) int {
	id, _ := strconv.Atoi(r.Header.Get("X-Customer-ID"))
	return id
}

// AdminAuthMiddleware is the exported version of adminAuthMiddleware for use in main.go
func (h *APIHandler) AdminAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return h.adminAuthMiddleware(next)
}

// adminAuthMiddleware validates admin API key
func (h *APIHandler) adminAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Limit request body size to prevent memory exhaustion
		limitRequestBody(r)

		apiKey := getAPIKey(r)
		if apiKey == "" {
			errorResponse(w, http.StatusUnauthorized, "Admin API key required")
			return
		}

		admin, err := h.service.GetAdminByAPIKey(apiKey)
		if err != nil {
			errorResponse(w, http.StatusUnauthorized, "Invalid admin API key")
			return
		}

		r.Header.Set("X-Admin-ID", strconv.Itoa(admin.ID))
		r.Header.Set("X-Admin-Username", admin.Username)
		r.Header.Set("X-Admin-Role", admin.Role)
		next(w, r)
	}
}

// getAdminUsername extracts admin username from request (set by middleware)
func getAdminUsername(r *http.Request) string {
	return r.Header.Get("X-Admin-Username")
}

// getAdminRole extracts admin role from request (set by middleware)
func getAdminRole(r *http.Request) string {
	return r.Header.Get("X-Admin-Role")
}

// requireAdminRole checks if admin has required role for an action
// Returns true if authorized, false if not
func requireAdminRole(w http.ResponseWriter, r *http.Request, allowedRoles ...string) bool {
	role := getAdminRole(r)
	for _, allowed := range allowedRoles {
		if role == allowed {
			return true
		}
	}
	errorResponse(w, http.StatusForbidden, "Insufficient privileges for this action")
	return false
}

// trustedProxies defines IPs that are allowed to set X-Forwarded-For
// These should be your reverse proxy (nginx, load balancer) IPs
var trustedProxies = map[string]bool{
	"127.0.0.1": true,
	"::1":       true,
	// Add your nginx/load balancer IPs here if they're on different hosts
}

// getClientIP extracts client IP from request
// SECURITY: Only trusts X-Forwarded-For from known proxy IPs to prevent spoofing
func getClientIP(r *http.Request) string {
	// Extract RemoteAddr IP (strip port)
	remoteIP := r.RemoteAddr
	if idx := strings.LastIndex(remoteIP, ":"); idx != -1 {
		remoteIP = remoteIP[:idx]
	}
	// Handle IPv6 brackets
	remoteIP = strings.TrimPrefix(remoteIP, "[")
	remoteIP = strings.TrimSuffix(remoteIP, "]")

	// Only trust forwarded headers if request came from a known proxy
	if trustedProxies[remoteIP] {
		// Check X-Real-IP first (set by nginx)
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
		// Check X-Forwarded-For - take the LAST IP before our proxy
		// (rightmost is most recently added, thus most trustworthy)
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			// Take the last one (closest to the proxy we trust)
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}

	// Not from trusted proxy - use RemoteAddr directly
	// This prevents attackers from spoofing X-Forwarded-For
	return remoteIP
}

// maxRequestBodySize limits request body to prevent memory exhaustion (1MB default)
const maxRequestBodySize = 1 << 20 // 1MB

// limitRequestBody wraps the request body with a size limit
func limitRequestBody(r *http.Request) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodySize)
}

// rateLimitMiddleware applies rate limiting
func (h *APIHandler) rateLimitMiddleware(maxRequests int, windowSecs int, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Limit request body size to prevent memory exhaustion
		limitRequestBody(r)

		identifier := getClientIP(r)
		endpoint := r.URL.Path

		if !h.service.CheckRateLimit(identifier, endpoint, maxRequests, windowSecs) {
			errorResponse(w, http.StatusTooManyRequests, "Rate limit exceeded. Please try again later.")
			return
		}

		next(w, r)
	}
}

// HandleRegister handles POST /api/v1/rental/register
func (h *APIHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		errorResponse(w, http.StatusBadRequest, "Email and password are required")
		return
	}

	result, err := h.service.RegisterCustomer(req.Email, req.Password)
	if err != nil {
		// Don't reveal if account exists - prevents enumeration attacks
		safeErrorResponse(w, http.StatusBadRequest, err,
			"Registration failed. Please check your email and password requirements.")
		return
	}

	jsonResponse(w, http.StatusCreated, result)
}

// HandleBalance handles GET /api/v1/rental/balance
func (h *APIHandler) HandleBalance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	customerID := getCustomerID(r)
	result, err := h.service.GetBalance(customerID)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve balance")
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// HandleDepositAddress handles GET /api/v1/rental/deposit-address
func (h *APIHandler) HandleDepositAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	customerID := getCustomerID(r)
	address, err := h.service.GetDepositAddress(customerID)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve deposit address")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"btc_address": address})
}

// HandlePrices handles GET /api/v1/rental/prices
func (h *APIHandler) HandlePrices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	result, err := h.service.GetPrices()
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve prices")
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// HandlePlaceOrder handles POST /api/v1/rental/order
func (h *APIHandler) HandlePlaceOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req PlaceOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate required fields
	if req.BudgetSat <= 0 {
		errorResponse(w, http.StatusBadRequest, "budget_sat must be positive")
		return
	}

	customerID := getCustomerID(r)
	result, err := h.service.PlaceOrder(customerID, &req)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusCreated, result)
}

// HandleGetOrders handles GET /api/v1/rental/orders
func (h *APIHandler) HandleGetOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	customerID := getCustomerID(r)
	orders, err := h.service.GetOrders(customerID)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve orders")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"orders": orders})
}

// HandleGetOrder handles GET /api/v1/rental/order/:id
func (h *APIHandler) HandleGetOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract order ID from path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 1 {
		errorResponse(w, http.StatusBadRequest, "Order ID required")
		return
	}
	orderID, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	customerID := getCustomerID(r)
	result, err := h.service.GetOrder(customerID, orderID)
	if err != nil {
		errorResponse(w, http.StatusNotFound, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// HandleCancelOrder handles DELETE /api/v1/rental/order/:id
func (h *APIHandler) HandleCancelOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract order ID from path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 1 {
		errorResponse(w, http.StatusBadRequest, "Order ID required")
		return
	}
	orderID, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	customerID := getCustomerID(r)
	if err := h.service.CancelOrder(customerID, orderID); err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// HandleExtendOrder handles POST /api/v1/rental/order/:id/extend
func (h *APIHandler) HandleExtendOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract order ID from path: /api/v1/rental/order/123/extend
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 2 {
		errorResponse(w, http.StatusBadRequest, "Order ID required")
		return
	}
	// Find "extend" and get the ID before it
	orderID := 0
	for i, p := range parts {
		if p == "extend" && i > 0 {
			var err error
			orderID, err = strconv.Atoi(parts[i-1])
			if err != nil {
				errorResponse(w, http.StatusBadRequest, "Invalid order ID")
				return
			}
			break
		}
	}
	if orderID == 0 {
		errorResponse(w, http.StatusBadRequest, "Order ID required")
		return
	}

	var req ExtendOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.AdditionalBudgetSat <= 0 {
		errorResponse(w, http.StatusBadRequest, "additional_budget_sat must be positive")
		return
	}

	customerID := getCustomerID(r)
	result, err := h.service.ExtendOrder(customerID, orderID, req.AdditionalBudgetSat)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// HandleBraiinsStatus handles GET /api/v1/rental/braiins-status (admin)
func (h *APIHandler) HandleBraiinsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	balance, err := h.service.GetBraiinsBalance()
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve balance")
		return
	}

	jsonResponse(w, http.StatusOK, balance)
}

// ============================================================================
// Transaction History Handlers
// ============================================================================

// HandleTransactions handles GET /api/v1/rental/transactions
func (h *APIHandler) HandleTransactions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	customerID := getCustomerID(r)
	limit, offset := sanitizePagination(r)

	entries, total, err := h.service.GetTransactionHistory(customerID, limit, offset)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve transactions")
		return
	}

	// Calculate running totals
	balance, err := h.service.GetBalance(customerID)
	if err == nil && balance != nil {
		runningTotal := balance.BalanceSat
		for i := range entries {
			entries[i].RunningTotal = runningTotal
			runningTotal -= entries[i].AmountSat
		}
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"transactions": entries,
		"total":        total,
	})
}

// HandleDeposits handles GET /api/v1/rental/deposits
func (h *APIHandler) HandleDeposits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	customerID := getCustomerID(r)
	deposits, err := h.service.GetDeposits(customerID)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve deposits")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"deposits": deposits})
}

// ============================================================================
// Withdrawal Handlers
// ============================================================================

// HandleRequestWithdrawal handles POST /api/v1/rental/withdraw
func (h *APIHandler) HandleRequestWithdrawal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req WithdrawRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	customerID := getCustomerID(r)

	// Per-account rate limit (independent of IP and of the 2FA lockout): cap how
	// many withdrawal requests one account can make, to blunt automated abuse of a
	// compromised session. 5 requests per hour.
	if !h.service.CheckRateLimit("withdraw:"+strconv.Itoa(customerID), "withdraw", 5, 3600) {
		errorResponse(w, http.StatusTooManyRequests, "Too many withdrawal requests; please try again later")
		return
	}

	result, err := h.service.RequestWithdrawal(customerID, &req)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusCreated, result)
}

// HandleGetWithdrawals handles GET /api/v1/rental/withdrawals
func (h *APIHandler) HandleGetWithdrawals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	customerID := getCustomerID(r)
	withdrawals, err := h.service.GetWithdrawals(customerID)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve withdrawals")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"withdrawals": withdrawals})
}

// HandleGetWithdrawal handles GET /api/v1/rental/withdrawal/:id
func (h *APIHandler) HandleGetWithdrawal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	withdrawalID, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid withdrawal ID")
		return
	}

	customerID := getCustomerID(r)
	result, err := h.service.GetWithdrawal(customerID, withdrawalID)
	if err != nil {
		errorResponse(w, http.StatusNotFound, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// ============================================================================
// Two-Factor Authentication Handlers
// ============================================================================

// Handle2FASetup handles POST /api/v1/rental/2fa/setup
func (h *APIHandler) Handle2FASetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	customerID := getCustomerID(r)

	// Get customer email for QR code label
	customer, _ := h.service.GetCustomerByAPIKey(getAPIKey(r))
	email := ""
	if customer != nil {
		email = customer.Email
	}

	result, err := h.service.Setup2FA(customerID, email)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// Handle2FAVerify handles POST /api/v1/rental/2fa/verify
func (h *APIHandler) Handle2FAVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req TwoFactorVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Code == "" {
		errorResponse(w, http.StatusBadRequest, "code is required")
		return
	}

	customerID := getCustomerID(r)
	if err := h.service.Verify2FA(customerID, req.Code); err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	// Generate backup codes to return to user
	codes, err := h.service.RegenerateBackupCodes(customerID, req.Code)
	if err != nil {
		// 2FA is enabled but backup codes failed - still return success
		jsonResponse(w, http.StatusOK, map[string]interface{}{
			"status":  "enabled",
			"message": "2FA enabled successfully. Save your backup codes in a safe place.",
		})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"status":       "enabled",
		"message":      "2FA enabled successfully. Save your backup codes in a safe place.",
		"backup_codes": codes.Codes,
	})
}

// Handle2FAStatus handles GET /api/v1/rental/2fa/status
func (h *APIHandler) Handle2FAStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	customerID := getCustomerID(r)
	result, err := h.service.Get2FAStatus(customerID)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve 2FA status")
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// Handle2FADisable handles POST /api/v1/rental/2fa/disable
func (h *APIHandler) Handle2FADisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req TwoFactorVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Code == "" {
		errorResponse(w, http.StatusBadRequest, "code is required")
		return
	}

	customerID := getCustomerID(r)
	if err := h.service.Disable2FA(customerID, req.Code); err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "disabled"})
}

// Handle2FABackupCodes handles POST /api/v1/rental/2fa/backup-codes
func (h *APIHandler) Handle2FABackupCodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req TwoFactorVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Code == "" {
		errorResponse(w, http.StatusBadRequest, "code is required to regenerate backup codes")
		return
	}

	customerID := getCustomerID(r)
	result, err := h.service.RegenerateBackupCodes(customerID, req.Code)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"message":      "New backup codes generated. Old codes are now invalid.",
		"backup_codes": result.Codes,
	})
}

// HandleLogin handles POST /api/v1/rental/login
func (h *APIHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		errorResponse(w, http.StatusBadRequest, "email and password are required")
		return
	}

	ipAddress := getClientIP(r)
	result, err := h.service.Login(&req, ipAddress)
	if err != nil {
		errorResponse(w, http.StatusUnauthorized, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// HandleLogout handles POST /api/v1/rental/logout
func (h *APIHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Get session token from header
	token := r.Header.Get("X-Session-Token")
	if token == "" {
		token = r.Header.Get("Authorization")
		if strings.HasPrefix(token, "Bearer ") {
			token = strings.TrimPrefix(token, "Bearer ")
		}
	}

	if token != "" {
		h.service.InvalidateSession(token)
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// ============================================================================
// Admin Handlers
// ============================================================================

// HandleAdminStats handles GET /api/v1/admin/stats
func (h *APIHandler) HandleAdminStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	stats, err := h.service.GetSystemStats()
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve system stats")
		return
	}

	jsonResponse(w, http.StatusOK, stats)
}

// HandleAdminCustomers handles GET /api/v1/admin/customers
func (h *APIHandler) HandleAdminCustomers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	limit, offset := sanitizePagination(r)

	result, err := h.service.AdminListCustomers(limit, offset)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve customers")
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// HandleAdminOrders handles GET /api/v1/admin/orders
func (h *APIHandler) HandleAdminOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	status := r.URL.Query().Get("status")
	limit, offset := sanitizePagination(r)

	result, err := h.service.AdminListOrders(status, limit, offset)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve orders")
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// HandleAdminWithdrawals handles GET /api/v1/admin/withdrawals
func (h *APIHandler) HandleAdminWithdrawals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	status := r.URL.Query().Get("status")
	limit, offset := sanitizePagination(r)

	result, err := h.service.AdminListWithdrawals(status, limit, offset)
	if err != nil {
		safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve withdrawals")
		return
	}

	jsonResponse(w, http.StatusOK, result)
}

// HandleAdminWithdrawalAction handles POST /api/v1/admin/withdrawal/:id/:action
func (h *APIHandler) HandleAdminWithdrawalAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// RBAC: Withdrawal actions require admin or superadmin role
	if !requireAdminRole(w, r, "admin", "superadmin") {
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 2 {
		errorResponse(w, http.StatusBadRequest, "Invalid path")
		return
	}

	action := parts[len(parts)-1]
	withdrawalID, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid withdrawal ID")
		return
	}

	adminUsername := getAdminUsername(r)

	switch action {
	case "approve":
		err = h.service.AdminApproveWithdrawal(adminUsername, withdrawalID)
	case "complete":
		var req struct {
			TxID string `json:"txid"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		err = h.service.AdminCompleteWithdrawal(adminUsername, withdrawalID, req.TxID)
	case "reject":
		var req struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		err = h.service.AdminRejectWithdrawal(adminUsername, withdrawalID, req.Reason)
	default:
		errorResponse(w, http.StatusBadRequest, "Invalid action")
		return
	}

	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleAdminCancelOrder handles POST /api/v1/admin/order/:id/cancel and /api/v1/admin/order/:id/autofund
func (h *APIHandler) HandleAdminCancelOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	action := parts[len(parts)-1]
	log.Printf("Admin order action: path=%s action=%s parts=%v", r.URL.Path, action, parts)
	orderID, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	switch action {
	case "cancel":
		// RBAC: Order cancellation requires admin or superadmin role
		if !requireAdminRole(w, r, "admin", "superadmin") {
			return
		}
		adminUsername := getAdminUsername(r)
		if err := h.service.AdminCancelOrder(adminUsername, orderID); err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "cancelled"})

	case "autofund":
		// RBAC: Auto-fund requires superadmin role
		if !requireAdminRole(w, r, "superadmin") {
			return
		}
		if err := h.service.AdminAutoFund(orderID); err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "auto-fund triggered"})

	default:
		errorResponse(w, http.StatusBadRequest, "Unknown action: "+action)
	}
}

// HandleAdminAutoFund handles POST /api/v1/admin/order/:id/autofund
func (h *APIHandler) HandleAdminAutoFund(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// RBAC: Auto-fund requires superadmin role
	if !requireAdminRole(w, r, "superadmin") {
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	orderID, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		errorResponse(w, http.StatusBadRequest, "Invalid order ID")
		return
	}

	if err := h.service.AdminAutoFund(orderID); err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "auto-fund triggered"})
}

// HandleAdminSettings handles GET/PUT /api/v1/admin/settings
func (h *APIHandler) HandleAdminSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := h.service.AdminGetSettings()
		if err != nil {
			safeErrorResponse(w, http.StatusInternalServerError, err, "Failed to retrieve settings")
			return
		}
		jsonResponse(w, http.StatusOK, settings)

	case http.MethodPut:
		// RBAC: Settings updates require superadmin role
		if !requireAdminRole(w, r, "superadmin") {
			return
		}

		var req struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errorResponse(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		adminUsername := getAdminUsername(r)
		if err := h.service.AdminUpdateSetting(adminUsername, req.Key, req.Value); err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "updated"})

	default:
		errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// RegisterRoutes registers all rental API routes
func (h *APIHandler) RegisterRoutes(mux *http.ServeMux) {
	// Public endpoints (rate limited)
	mux.HandleFunc("/api/v1/rental/register", h.rateLimitMiddleware(10, 60, h.HandleRegister)) // 10 per minute
	mux.HandleFunc("/api/v1/rental/prices", h.rateLimitMiddleware(60, 60, h.HandlePrices))     // 60 per minute

	// Authenticated customer endpoints
	mux.HandleFunc("/api/v1/rental/balance", h.authMiddleware(h.HandleBalance))
	mux.HandleFunc("/api/v1/rental/deposit-address", h.authMiddleware(h.HandleDepositAddress))
	mux.HandleFunc("/api/v1/rental/transactions", h.authMiddleware(h.HandleTransactions))
	mux.HandleFunc("/api/v1/rental/deposits", h.authMiddleware(h.HandleDeposits))
	mux.HandleFunc("/api/v1/rental/order", h.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.HandlePlaceOrder(w, r)
		} else {
			errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	}))
	mux.HandleFunc("/api/v1/rental/orders", h.authMiddleware(h.HandleGetOrders))
	// Order extend endpoint (more specific, must be registered before general /order/)
	mux.HandleFunc("/api/v1/rental/order/", h.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is an extend request
		if strings.HasSuffix(r.URL.Path, "/extend") && r.Method == http.MethodPost {
			h.HandleExtendOrder(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			h.HandleGetOrder(w, r)
		case http.MethodDelete:
			h.HandleCancelOrder(w, r)
		default:
			errorResponse(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	}))

	// Customer withdrawal endpoints
	mux.HandleFunc("/api/v1/rental/withdraw", h.authMiddleware(h.HandleRequestWithdrawal))
	mux.HandleFunc("/api/v1/rental/withdrawals", h.authMiddleware(h.HandleGetWithdrawals))
	mux.HandleFunc("/api/v1/rental/withdrawal/", h.authMiddleware(h.HandleGetWithdrawal))

	// Two-Factor Authentication endpoints - rate limited to prevent brute force
	mux.HandleFunc("/api/v1/rental/2fa/setup", h.authMiddleware(h.Handle2FASetup))
	mux.HandleFunc("/api/v1/rental/2fa/verify", h.rateLimitMiddleware(5, 60, h.authMiddleware(h.Handle2FAVerify)))       // 5 attempts per minute
	mux.HandleFunc("/api/v1/rental/2fa/status", h.authMiddleware(h.Handle2FAStatus))
	mux.HandleFunc("/api/v1/rental/2fa/disable", h.rateLimitMiddleware(5, 60, h.authMiddleware(h.Handle2FADisable)))     // 5 attempts per minute
	mux.HandleFunc("/api/v1/rental/2fa/backup-codes", h.rateLimitMiddleware(5, 60, h.authMiddleware(h.Handle2FABackupCodes)))

	// Session-based auth (login/logout)
	mux.HandleFunc("/api/v1/rental/login", h.rateLimitMiddleware(10, 60, h.HandleLogin)) // 10 per minute
	mux.HandleFunc("/api/v1/rental/logout", h.HandleLogout)

	// Admin endpoints - rate limited BEFORE auth to prevent brute-force attacks
	// 30 requests per minute per IP for read operations
	// 10 requests per minute per IP for write operations
	mux.HandleFunc("/api/v1/admin/stats", h.rateLimitMiddleware(30, 60, h.adminAuthMiddleware(h.HandleAdminStats)))
	mux.HandleFunc("/api/v1/admin/customers", h.rateLimitMiddleware(30, 60, h.adminAuthMiddleware(h.HandleAdminCustomers)))
	mux.HandleFunc("/api/v1/admin/orders", h.rateLimitMiddleware(30, 60, h.adminAuthMiddleware(h.HandleAdminOrders)))
	mux.HandleFunc("/api/v1/admin/withdrawals", h.rateLimitMiddleware(30, 60, h.adminAuthMiddleware(h.HandleAdminWithdrawals)))
	mux.HandleFunc("/api/v1/admin/withdrawal/", h.rateLimitMiddleware(10, 60, h.adminAuthMiddleware(h.HandleAdminWithdrawalAction)))
	mux.HandleFunc("/api/v1/admin/order/", h.rateLimitMiddleware(10, 60, h.adminAuthMiddleware(h.HandleAdminCancelOrder)))
	mux.HandleFunc("/api/v1/admin/settings", h.rateLimitMiddleware(10, 60, h.adminAuthMiddleware(h.HandleAdminSettings)))
	mux.HandleFunc("/api/v1/admin/braiins-status", h.rateLimitMiddleware(30, 60, h.adminAuthMiddleware(h.HandleBraiinsStatus)))
}
