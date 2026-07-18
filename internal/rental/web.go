package rental

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bch2/forge-pool/internal/i18n"
	qrcode "github.com/skip2/go-qrcode"
)

// WebHandler handles web UI requests
type WebHandler struct {
	service          *Service
	templates        *template.Template
	templateDir      string
	staticDir        string
	wsHub            *WSHub
	turnstileSiteKey string
	turnstileSecret  string
	translator       *i18n.Translator
}

// templateFuncs returns template helper functions
func templateFuncs(translator *i18n.Translator) template.FuncMap {
	return template.FuncMap{
		// T translates a key to the current language, falls back to English then key itself
		"T": func(lang, key string) string {
			if translator == nil {
				return key
			}
			return translator.T(lang, key)
		},
		"formatBTC": func(sats int64) string {
			// Convert satoshis to BTC decimal
			btc := float64(sats) / 100000000
			return fmt.Sprintf("%.8f", btc)
		},
		"formatSats": func(sats int64) string {
			// Add thousand separators (kept for backwards compatibility)
			s := fmt.Sprintf("%d", sats)
			if sats < 0 {
				s = s[1:]
			}
			n := len(s)
			if n <= 3 {
				if sats < 0 {
					return "-" + s
				}
				return s
			}
			var result strings.Builder
			pre := n % 3
			if pre > 0 {
				result.WriteString(s[:pre])
				if n > pre {
					result.WriteString(",")
				}
			}
			for i := pre; i < n; i += 3 {
				result.WriteString(s[i : i+3])
				if i+3 < n {
					result.WriteString(",")
				}
			}
			if sats < 0 {
				return "-" + result.String()
			}
			return result.String()
		},
		"formatTime": func(t time.Time) string {
			return t.Format("Jan 2, 15:04")
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"upper": strings.ToUpper,
		"txTypeBadge": func(txType string) string {
			switch txType {
			case "deposit":
				return "completed"
			case "order_charge":
				return "pending"
			case "order_refund":
				return "active"
			case "withdrawal":
				return "failed"
			default:
				return "pending"
			}
		},
	}
}

// NewWebHandler creates a new web handler
func NewWebHandler(service *Service, templateDir, staticDir string) (*WebHandler, error) {
	// Verify template directory exists
	if _, err := filepath.Glob(filepath.Join(templateDir, "*.html")); err != nil {
		return nil, fmt.Errorf("invalid template directory: %w", err)
	}

	// Create and start WebSocket hub
	wsHub := NewWSHub()
	go wsHub.Run()

	// Load translations
	translator := i18n.New("en")
	i18nDir := filepath.Join(filepath.Dir(templateDir), "i18n")
	if err := translator.LoadDir(i18nDir); err != nil {
		log.Printf("Warning: failed to load i18n translations: %v", err)
	} else {
		log.Printf("Loaded %d languages: %v", len(translator.Languages()), translator.Languages())
	}

	handler := &WebHandler{
		service:          service,
		templateDir:      templateDir,
		staticDir:        staticDir,
		wsHub:            wsHub,
		turnstileSiteKey: service.config.TurnstileSiteKey,
		turnstileSecret:  service.config.TurnstileSecret,
		translator:       translator,
	}

	// Give service access to WebSocket hub for broadcasting updates
	service.wsHub = wsHub

	return handler, nil
}

// loadTemplate loads base + page template with path validation
func loadTemplate(templateDir, page string, translator *i18n.Translator) (*template.Template, error) {
	// Security: Validate page name to prevent path traversal
	// Only allow alphanumeric, underscore, hyphen, and .html extension
	if !isValidTemplateName(page) {
		return nil, fmt.Errorf("invalid template name: %s", page)
	}

	basePath := filepath.Join(templateDir, "base.html")
	pagePath := filepath.Join(templateDir, page)

	// Security: Verify resolved path is within templateDir
	absTemplateDir, _ := filepath.Abs(templateDir)
	absPagePath, _ := filepath.Abs(pagePath)
	if !strings.HasPrefix(absPagePath, absTemplateDir) {
		return nil, fmt.Errorf("template path traversal attempt: %s", page)
	}

	return template.New("").Funcs(templateFuncs(translator)).ParseFiles(basePath, pagePath)
}

// isValidTemplateName checks if template name is safe
func isValidTemplateName(name string) bool {
	// Must end with .html
	if !strings.HasSuffix(name, ".html") {
		return false
	}
	// Must not contain path separators or parent directory references
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return false
	}
	// Must only contain safe characters
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// Session cookie name
const sessionCookieName = "hashforge_session"

// Language cookie name
const langCookieName = "hashforge_lang"

// getLang returns the user's preferred language from cookie or Accept-Language header
func (h *WebHandler) getLang(r *http.Request) string {
	// Check cookie first
	if cookie, err := r.Cookie(langCookieName); err == nil && cookie.Value != "" {
		if h.translator != nil && h.translator.HasLanguage(cookie.Value) {
			return cookie.Value
		}
	}

	// Check Accept-Language header
	acceptLang := r.Header.Get("Accept-Language")
	if acceptLang != "" {
		// Parse Accept-Language (simplified - just take first language)
		parts := strings.Split(acceptLang, ",")
		if len(parts) > 0 {
			lang := strings.TrimSpace(strings.Split(parts[0], ";")[0])
			// Try exact match first
			if h.translator != nil && h.translator.HasLanguage(lang) {
				return lang
			}
			// Try language code without region (e.g., "en" from "en-US")
			if len(lang) > 2 {
				langCode := lang[:2]
				if h.translator != nil && h.translator.HasLanguage(langCode) {
					return langCode
				}
			}
		}
	}

	// Default to English
	return "en"
}

// cspNonce returns a fresh base64 nonce for a per-request CSP script-src.
func cspNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "" // fail closed: an empty nonce matches nothing, so inline scripts are blocked
	}
	return base64.StdEncoding.EncodeToString(b)
}

// nonceScripts adds the CSP nonce to every attribute-less inline <script> so it
// satisfies a nonce-based script-src. External <script src=...> tags are untouched.
func nonceScripts(html, nonce string) string {
	return strings.ReplaceAll(html, "<script>", "<script nonce=\""+nonce+"\">")
}

// serveWithNonce writes pre-built HTML with a matching nonce-based CSP, nonce-ing
// its inline scripts (used by the raw file + data-injection handlers).
func serveWithNonce(w http.ResponseWriter, html string) {
	nonce := cspNonce()
	setSecurityHeaders(w, nonce)
	w.Write([]byte(nonceScripts(html, nonce)))
}

// setSecurityHeaders adds all security headers to the response
func setSecurityHeaders(w http.ResponseWriter, nonce string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-XSS-Protection", "1; mode=block")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=(), usb=()")
	w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; script-src 'self' 'nonce-"+nonce+"' https://challenges.cloudflare.com https://static.cloudflareinsights.com; frame-src https://challenges.cloudflare.com; connect-src 'self' wss://hashforge.bch2.org ws://hashforge.bch2.org")
}

// render renders a template with common data
func (h *WebHandler) render(w http.ResponseWriter, tmpl string, data map[string]interface{}) {
	h.renderWithRequest(w, nil, tmpl, data)
}

func (h *WebHandler) renderWithRequest(w http.ResponseWriter, r *http.Request, tmpl string, data map[string]interface{}) {
	if data == nil {
		data = make(map[string]interface{})
	}

	// Per-request CSP nonce for inline <script> blocks (script-src has no 'unsafe-inline').
	nonce := cspNonce()
	data["CSPNonce"] = nonce

	// Add current language if request is available
	if r != nil {
		data["Lang"] = h.getLang(r)
	}

	// Generate CSRF token - use session-bound token if session available
	if _, ok := data["CSRFToken"]; !ok {
		if sessionToken, ok := data["_sessionToken"].(string); ok && sessionToken != "" {
			data["CSRFToken"] = deriveCSRFToken(sessionToken)
			delete(data, "_sessionToken") // Don't expose to template
		} else {
			data["CSRFToken"] = generateCSRFToken()
		}
	}

	// Add unread notification count if customer is logged in
	if customer, ok := data["Customer"].(*Customer); ok && customer != nil {
		data["LoggedIn"] = true
		data["UnreadNotifications"] = h.service.GetUnreadNotificationCount(customer.ID)
	}

	// Load template
	t, err := loadTemplate(h.templateDir, tmpl, h.translator)
	if err != nil {
		log.Printf("Failed to load template %s: %v", tmpl, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	setSecurityHeaders(w, nonce)

	err = t.ExecuteTemplate(w, "base", data)
	if err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// trustedWebProxies defines IPs allowed to set X-Forwarded-For for web requests
// Must match the trusted proxies in api.go
var trustedWebProxies = map[string]bool{
	"127.0.0.1": true,
	"::1":       true,
}

// getClientIPWeb extracts client IP from web request
// SECURITY: Only trusts forwarded headers from known proxy IPs to prevent IP spoofing
func getClientIPWeb(r *http.Request) string {
	// Extract RemoteAddr IP (strip port)
	remoteIP := r.RemoteAddr
	if idx := strings.LastIndex(remoteIP, ":"); idx != -1 {
		remoteIP = remoteIP[:idx]
	}
	// Handle IPv6 brackets
	remoteIP = strings.TrimPrefix(remoteIP, "[")
	remoteIP = strings.TrimSuffix(remoteIP, "]")

	// Only trust forwarded headers if request came from a known proxy
	if trustedWebProxies[remoteIP] {
		// Check X-Real-IP first (set by nginx)
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
		// Check X-Forwarded-For - take the LAST IP before our proxy
		// (rightmost is most recently added, thus most trustworthy)
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}

	// Not from trusted proxy - use RemoteAddr directly
	// This prevents attackers from spoofing X-Forwarded-For
	return remoteIP
}

// getSession gets session from cookie with IP validation
func (h *WebHandler) getSession(r *http.Request) (*Session, *Customer) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, nil
	}

	// Get session with IP check for security logging
	clientIP := getClientIPWeb(r)
	session, err := h.service.GetSessionWithIP(cookie.Value, clientIP)
	if err != nil {
		return nil, nil
	}

	customer, err := h.service.GetCustomerByID(session.CustomerID)
	if err != nil {
		return nil, nil
	}

	return session, customer
}

// setSession sets session cookie
func (h *WebHandler) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400, // 24 hours
	})
}

// clearSession clears session cookie
func (h *WebHandler) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// requireAuth middleware
func (h *WebHandler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, customer := h.getSession(r)
		if session == nil || customer == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		// Store in request context via headers (simple approach)
		r.Header.Set("X-Customer-ID", strconv.Itoa(customer.ID))
		r.Header.Set("X-Session-ID", strconv.Itoa(session.ID))
		next(w, r)
	}
}

// requireAdmin middleware checks for admin session with IP and role validation
func (h *WebHandler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check for admin session cookie
		cookie, err := r.Cookie("admin_session")
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		// Validate admin session including role and IP
		var adminID int
		var username, role, sessionIP string
		err = h.service.db.QueryRow(`
			SELECT a.id, a.username, COALESCE(a.role, 'admin'), COALESCE(s.ip_address, '')
			FROM rental_admins a
			JOIN rental_admin_sessions s ON s.admin_id = a.id
			WHERE s.session_token = $1 AND s.expires_at > NOW()
		`, HashToken(cookie.Value)).Scan(&adminID, &username, &role, &sessionIP)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		// Validate IP address matches (security: prevent session hijacking)
		clientIP := getClientIPWeb(r)
		if sessionIP != "" && sessionIP != clientIP {
			log.Printf("SECURITY: Admin session IP changed from %s to %s (admin=%s) - INVALIDATING", sessionIP, clientIP, username)
			h.service.db.Exec(`DELETE FROM rental_admin_sessions WHERE session_token = $1`, HashToken(cookie.Value))
			http.SetCookie(w, &http.Cookie{
				Name:     "admin_session",
				Value:    "",
				Path:     "/admin",
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteStrictMode,
				MaxAge:   -1,
			})
			http.Redirect(w, r, "/admin/login?error=session_expired", http.StatusSeeOther)
			return
		}

		r.Header.Set("X-Admin-ID", strconv.Itoa(adminID))
		r.Header.Set("X-Admin-Username", username)
		r.Header.Set("X-Admin-Role", role)
		next(w, r)
	}
}

// requireAdminRole creates a middleware that requires specific admin roles
func (h *WebHandler) requireAdminRole(allowedRoles ...string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return h.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
			role := r.Header.Get("X-Admin-Role")
			for _, allowed := range allowedRoles {
				if role == allowed {
					next(w, r)
					return
				}
			}
			http.Redirect(w, r, "/admin?error=insufficient_permissions", http.StatusSeeOther)
		})
	}
}

// csrfSecret is used to derive CSRF tokens from session tokens
// In production, this MUST be set via environment variable for session persistence
var csrfSecret []byte
var csrfSecretOnce sync.Once

func initCSRFSecret() {
	secret := os.Getenv("CSRF_SECRET")
	if secret != "" {
		// Decode hex-encoded secret from environment
		decoded, err := hex.DecodeString(secret)
		if err == nil && len(decoded) >= 32 {
			csrfSecret = decoded
			log.Println("CSRF secret loaded from environment")
			return
		}
		// If not hex, use raw bytes (for backwards compatibility)
		if len(secret) >= 32 {
			csrfSecret = []byte(secret)
			log.Println("CSRF secret loaded from environment (raw)")
			return
		}
		log.Println("WARNING: CSRF_SECRET too short (need 32+ chars), generating random")
	}

	// Check if we're in production - fail if CSRF secret not configured
	if os.Getenv("PRODUCTION") == "true" || os.Getenv("ENV") == "production" {
		log.Fatal("FATAL: CSRF_SECRET must be configured in production")
	}

	// Generate random secret for development (will invalidate sessions on restart)
	csrfSecret = make([]byte, 32)
	if _, err := rand.Read(csrfSecret); err != nil {
		log.Fatalf("FATAL: Failed to generate CSRF secret - cryptographic randomness unavailable: %v", err)
	}
	log.Println("SECURITY WARNING: CSRF_SECRET not set - sessions will not survive restart")
	log.Println("SECURITY WARNING: Generate a secret with: openssl rand -hex 32")
}

func getCSRFSecret() []byte {
	csrfSecretOnce.Do(initCSRFSecret)
	return csrfSecret
}

// generateCSRFToken derives a CSRF token from the session token using HMAC
// This ensures the token is tied to the session and can be validated
func generateCSRFToken() string {
	// Fallback for non-authenticated pages
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Printf("ERROR: Failed to generate CSRF token: %v", err)
		return "" // Will fail validation, preventing operation
	}
	return hex.EncodeToString(b)
}

// deriveCSRFToken creates a deterministic CSRF token from a session token
func deriveCSRFToken(sessionToken string) string {
	h := hmac.New(sha256.New, getCSRFSecret())
	h.Write([]byte(sessionToken))
	return hex.EncodeToString(h.Sum(nil))
}

// validateCSRFToken checks if the provided token matches the expected token
func validateCSRFToken(sessionToken, providedToken string) bool {
	expectedToken := deriveCSRFToken(sessionToken)
	// Use constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare([]byte(expectedToken), []byte(providedToken)) == 1
}

// csrfProtect is middleware that validates CSRF tokens on state-changing requests
func (h *WebHandler) csrfProtect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only validate on state-changing methods
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			providedToken := r.FormValue("csrf_token")
			if providedToken == "" {
				providedToken = r.Header.Get("X-CSRF-Token")
			}

			session, _ := h.getSession(r)
			if session != nil {
				// Customer session: token is bound to the customer session token.
				if !validateCSRFToken(session.SessionToken, providedToken) {
					log.Printf("CSRF validation failed for session %d", session.ID)
					http.Error(w, "Invalid CSRF token", http.StatusForbidden)
					return
				}
			} else if adminCookie, err := r.Cookie("admin_session"); err == nil && adminCookie.Value != "" {
				// Admin session: token is bound to the admin session cookie
				// (deriveCSRFToken(admin_session), as embedded in the admin forms).
				// Previously admins had no customer session here, so CSRF was
				// skipped entirely on the admin fund-approval endpoints.
				if !validateCSRFToken(adminCookie.Value, providedToken) {
					log.Printf("CSRF validation failed for admin session")
					http.Error(w, "Invalid CSRF token", http.StatusForbidden)
					return
				}
			}
		}
		next(w, r)
	}
}

// HandleHome handles GET /
func (h *WebHandler) HandleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		h.Handle404(w, r)
		return
	}

	session, _ := h.getSession(r)
	h.renderWithRequest(w, r, "home.html", map[string]interface{}{
		"LoggedIn": session != nil,
	})
}

// HandleRegister handles GET/POST /register
func (h *WebHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	session, _ := h.getSession(r)
	if session != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"LoggedIn":         false,
		"Title":            "Register",
		"TurnstileSiteKey": h.turnstileSiteKey,
	}

	if r.Method == http.MethodPost {
		// Rate limit: 5 registrations per hour per IP to prevent spam accounts
		clientIP := getClientIPWeb(r)
		if !h.service.CheckRateLimit(clientIP, "register", 5, 3600) {
			data["Error"] = "Too many registration attempts. Please try again later."
			h.renderWithRequest(w, r, "register.html", data)
			return
		}

		// Verify Turnstile CAPTCHA
		turnstileToken := r.FormValue("cf-turnstile-response")
		if valid, err := VerifyTurnstile(h.turnstileSecret, turnstileToken, clientIP); !valid {
			if err != nil {
				log.Printf("Turnstile verification error: %v", err)
			}
			data["Error"] = "CAPTCHA verification failed. Please try again."
			h.renderWithRequest(w, r, "register.html", data)
			return
		}

		email := r.FormValue("email")
		password := r.FormValue("password")

		_, err := h.service.RegisterCustomer(email, password)
		if err != nil {
			data["Error"] = err.Error()
			h.renderWithRequest(w, r, "register.html", data)
			return
		}

		data["Success"] = true
		data["Email"] = email
	}

	h.renderWithRequest(w, r, "register.html", data)
}

// HandleLogin handles GET/POST /login
func (h *WebHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	session, _ := h.getSession(r)
	if session != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"LoggedIn":         false,
		"Title":            "Login",
		"TurnstileSiteKey": h.turnstileSiteKey,
	}

	if r.Method == http.MethodPost {
		email := r.FormValue("email")
		password := r.FormValue("password")
		totpCode := r.FormValue("totp_code")
		clientIP := getClientIPWeb(r)

		// Verify Turnstile CAPTCHA (only on initial login, not 2FA step)
		if totpCode == "" {
			turnstileToken := r.FormValue("cf-turnstile-response")
			if valid, err := VerifyTurnstile(h.turnstileSecret, turnstileToken, clientIP); !valid {
				if err != nil {
					log.Printf("Turnstile verification error: %v", err)
				}
				data["Error"] = "CAPTCHA verification failed. Please try again."
				data["Email"] = email
				h.renderWithRequest(w, r, "login.html", data)
				return
			}
		}

		// Try to login
		loginReq := &LoginRequest{
			Email:    email,
			Password: password,
			TOTPCode: totpCode,
		}

		result, err := h.service.Login(loginReq, clientIP)
		if err != nil {
			data["Error"] = err.Error()
			data["Email"] = email
			h.renderWithRequest(w, r, "login.html", data)
			return
		}

		if result.Requires2FA {
			data["Requires2FA"] = true
			data["Email"] = email
			h.renderWithRequest(w, r, "login.html", data)
			return
		}

		h.setSession(w, result.SessionToken)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	h.renderWithRequest(w, r, "login.html", data)
}

// HandleLogout handles GET /logout
func (h *WebHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookieName)
	if cookie != nil {
		h.service.InvalidateSession(cookie.Value)
	}
	h.clearSession(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleVerifyEmail handles GET /verify-email?token=xxx
func (h *WebHandler) HandleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")

	data := map[string]interface{}{
		"LoggedIn": false,
		"Title":    "Verify Email",
	}

	result, err := h.service.VerifyEmail(token)
	if err != nil {
		data["Error"] = err.Error()
		h.renderWithRequest(w, r, "verify_email.html", data)
		return
	}

	data["BTCAddress"] = result.BTCAddress
	h.renderWithRequest(w, r, "verify_email.html", data)
}

// HandleResendVerification handles GET /resend-verification?email=xxx
func (h *WebHandler) HandleResendVerification(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")

	data := map[string]interface{}{
		"LoggedIn": false,
		"Title":    "Resend Verification",
	}

	if email == "" {
		data["Error"] = "Email address is required"
		h.renderWithRequest(w, r, "register.html", data)
		return
	}

	// Always show success to prevent email enumeration
	_ = h.service.ResendVerificationEmail(email)

	data["Success"] = true
	data["Email"] = email
	data["Resent"] = true
	h.renderWithRequest(w, r, "register.html", data)
}

// HandleForgotPassword handles GET/POST /forgot-password
func (h *WebHandler) HandleForgotPassword(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"LoggedIn":         false,
		"Title":            "Forgot Password",
		"TurnstileSiteKey": h.turnstileSiteKey,
	}

	if r.Method == http.MethodPost {
		// Rate limit: 3 requests per 15 minutes per IP to prevent abuse
		clientIP := getClientIPWeb(r)
		if !h.service.CheckRateLimit(clientIP, "forgot_password", 3, 900) {
			data["Error"] = "Too many password reset requests. Please try again in 15 minutes."
			h.renderWithRequest(w, r, "forgot_password.html", data)
			return
		}

		// Verify Turnstile CAPTCHA
		turnstileToken := r.FormValue("cf-turnstile-response")
		if valid, err := VerifyTurnstile(h.turnstileSecret, turnstileToken, clientIP); !valid {
			if err != nil {
				log.Printf("Turnstile verification error: %v", err)
			}
			data["Error"] = "CAPTCHA verification failed. Please try again."
			h.renderWithRequest(w, r, "forgot_password.html", data)
			return
		}

		email := r.FormValue("email")
		_ = h.service.RequestPasswordReset(email)
		data["Success"] = true
	}

	h.renderWithRequest(w, r, "forgot_password.html", data)
}

// HandleResetPassword handles GET/POST /reset-password
func (h *WebHandler) HandleResetPassword(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"LoggedIn": false,
		"Title":    "Reset Password",
	}

	token := r.URL.Query().Get("token")
	if r.Method == http.MethodPost {
		token = r.FormValue("token")
	}

	data["Token"] = token

	if r.Method == http.MethodPost {
		password := r.FormValue("password")

		err := h.service.ResetPassword(token, password)
		if err != nil {
			data["Error"] = err.Error()
			if strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "invalid") {
				data["Expired"] = true
			}
			h.renderWithRequest(w, r, "reset_password.html", data)
			return
		}

		data["Success"] = true
	}

	h.renderWithRequest(w, r, "reset_password.html", data)
}

// HandleDashboard handles GET /dashboard
func (h *WebHandler) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Get balance
	balance, _ := h.service.GetBalance(customer.ID)

	// Get 2FA status
	twoFAStatus, _ := h.service.Get2FAStatus(customer.ID)

	// Get orders
	orders, _ := h.service.GetOrders(customer.ID)

	// Find active order
	var activeOrder *Order
	var activeCount int
	for i := range orders {
		if orders[i].Status == OrderStatusActive || orders[i].Status == OrderStatusPending {
			activeOrder = &orders[i]
			activeCount++
		}
	}

	// Recent orders (up to 5)
	recentOrders := orders
	if len(recentOrders) > 5 {
		recentOrders = recentOrders[:5]
	}

	// Get transactions and calculate running totals
	transactions, _, _ := h.service.GetTransactionHistory(customer.ID, 5, 0)
	// Calculate running totals (newest first, so work backwards from current balance)
	if balance != nil {
		runningTotal := balance.BalanceSat
		for i := range transactions {
			transactions[i].RunningTotal = runningTotal
			runningTotal -= transactions[i].AmountSat
		}
	}

	// Get mining rewards summary
	miningRewards := h.service.GetMiningRewardsSummary(customer.BCH2Address)

	// Get pending deposits (not yet credited)
	allDeposits, _ := h.service.GetDeposits(customer.ID)
	var pendingDeposits []Deposit
	for _, d := range allDeposits {
		if !d.Credited {
			pendingDeposits = append(pendingDeposits, d)
		}
	}

	data := map[string]interface{}{
		"LoggedIn":           true,
		"Title":              "Dashboard",
		"Customer":           customer,
		"BalanceSat":         balance.BalanceSat,
		"BalanceBTC":         balance.BalanceBTC,
		"PendingDeposit":     balance.PendingDeposit,
		"PendingDeposits":    pendingDeposits,
		"RequiredConfirms":   h.service.GetRequiredConfirmations(),
		"TwoFAEnabled":       twoFAStatus.Enabled,
		"ActiveOrders":       activeCount,
		"ActiveOrder":        activeOrder,
		"RecentOrders":       recentOrders,
		"RecentTransactions": transactions,
		"MiningRewards":      miningRewards,
		"_sessionToken":      session.SessionToken, // For CSRF token derivation
	}

	h.renderWithRequest(w, r, "dashboard.html", data)
}

// HandleDeposit handles GET /deposit
func (h *WebHandler) HandleDeposit(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	address, _ := h.service.GetDepositAddress(customer.ID)
	deposits, _ := h.service.GetDeposits(customer.ID)

	// Filter pending
	var pending []Deposit
	for _, d := range deposits {
		if !d.Credited {
			pending = append(pending, d)
		}
	}

	h.renderWithRequest(w, r, "deposit.html", map[string]interface{}{
		"LoggedIn":        true,
		"Title":           "Deposit",
		"BTCAddress":      address,
		"QRCodeImage":     qrDataURI(address),
		"PendingDeposits": pending,
	})
}

// HandleNewOrder handles GET/POST /order/new
func (h *WebHandler) HandleNewOrder(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	balance, _ := h.service.GetBalance(customer.ID)
	orders, _ := h.service.GetOrders(customer.ID)

	// Check for active order
	var activeOrderID int
	for _, o := range orders {
		if o.Status == OrderStatusActive || o.Status == OrderStatusPending {
			activeOrderID = o.ID
			break
		}
	}

	// Check if customer has 2FA enabled
	twoFAStatus, _ := h.service.Get2FAStatus(customer.ID)
	has2FA := twoFAStatus != nil && twoFAStatus.Enabled

	// Convert balance to BTC for display
	maxBudgetBTC := fmt.Sprintf("%.8f", float64(balance.BalanceSat)/100000000)

	data := map[string]interface{}{
		"LoggedIn":       true,
		"Title":          "Rent Hashpower",
		"BalanceSat":     balance.BalanceSat,
		"BalanceBTC":     balance.BalanceBTC,
		"MaxBudgetBTC":   maxBudgetBTC,
		"HasActiveOrder": activeOrderID > 0,
		"ActiveOrderID":  activeOrderID,
		"Customer":       customer,
		"Has2FA":         has2FA,
		"_sessionToken":  session.SessionToken, // For CSRF token derivation
	}

	if r.Method == http.MethodPost {
		// Rate limit: 10 order creations per hour per customer
		rateLimitKey := fmt.Sprintf("order:%d", customer.ID)
		if !h.service.CheckRateLimit(rateLimitKey, "order", 10, 3600) {
			data["Error"] = "Too many order requests. Please try again later."
			h.renderWithRequest(w, r, "order_new.html", data)
			return
		}

		// Require confirmation flow
		if r.FormValue("confirm") != "true" {
			data["Error"] = "Please review and confirm your order."
			h.renderWithRequest(w, r, "order_new.html", data)
			return
		}

		// Require acknowledgment of cancellation policy
		if r.FormValue("acknowledge") != "true" {
			data["Error"] = "You must acknowledge the cancellation policy to proceed."
			h.renderWithRequest(w, r, "order_new.html", data)
			return
		}

		// Verify 2FA if enabled
		if has2FA {
			totpCode := r.FormValue("totp_code")
			if totpCode == "" {
				data["Error"] = "2FA code is required to place an order."
				h.renderWithRequest(w, r, "order_new.html", data)
				return
			}

			valid, err := h.service.Verify2FACode(customer.ID, totpCode)
			if err != nil {
				data["Error"] = "Failed to verify 2FA code. Please try again."
				h.renderWithRequest(w, r, "order_new.html", data)
				return
			}
			if !valid {
				data["Error"] = "Invalid 2FA code. Please try again."
				h.renderWithRequest(w, r, "order_new.html", data)
				return
			}
		}

		workerName := r.FormValue("worker_name")
		miningMode := r.FormValue("mining_mode")
		log.Printf("Order form: mining_mode=%q, worker_name=%q", miningMode, workerName)
		speedLimit, _ := strconv.ParseFloat(r.FormValue("speed_limit_ph"), 64)
		if math.IsNaN(speedLimit) || math.IsInf(speedLimit, 0) || speedLimit < 1 {
			speedLimit = 1
		}

		// Parse budget as BTC and convert to sats
		budgetBTC, _ := strconv.ParseFloat(r.FormValue("budget_btc"), 64)
		if math.IsNaN(budgetBTC) || math.IsInf(budgetBTC, 0) || budgetBTC < 0.001 {
			budgetBTC = 0.001
		}
		budgetSat := int64(budgetBTC * 100000000)

		req := &PlaceOrderRequest{
			WorkerName:   workerName,
			BudgetSat:    budgetSat,
			SpeedLimitPH: speedLimit,
			MiningMode:   miningMode,
		}

		result, err := h.service.PlaceOrder(customer.ID, req)
		if err != nil {
			data["Error"] = err.Error()
			h.renderWithRequest(w, r, "order_new.html", data)
			return
		}

		// Redirect to appropriate mining status page based on mode
		log.Printf("PlaceOrder result: MiningMode=%q, OrderID=%d", result.MiningMode, result.OrderID)
		if result.MiningMode == "solo" {
			log.Printf("Redirecting to /solo for address %s", customer.BCH2Address)
			http.Redirect(w, r, fmt.Sprintf("/solo?address=%s", url.QueryEscape(customer.BCH2Address)), http.StatusSeeOther)
		} else {
			log.Printf("Redirecting to /pplns for address %s", customer.BCH2Address)
			http.Redirect(w, r, fmt.Sprintf("/pplns?address=%s", url.QueryEscape(customer.BCH2Address)), http.StatusSeeOther)
		}
		return
	}

	h.renderWithRequest(w, r, "order_new.html", data)
}

// HandleCancelOrder handles POST /order/:id/cancel
func (h *WebHandler) HandleCancelOrder(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Extract order ID
	parts := strings.Split(r.URL.Path, "/")
	orderID := 0
	for i, p := range parts {
		if p == "order" && i+1 < len(parts) {
			orderID, _ = strconv.Atoi(parts[i+1])
			break
		}
	}

	if orderID > 0 {
		err := h.service.CancelOrder(customer.ID, orderID)
		if err != nil {
			// Redirect with error message
			http.Redirect(w, r, "/dashboard?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
	}

	http.Redirect(w, r, "/dashboard?success=Order+cancelled", http.StatusSeeOther)
}

// HandleExtendOrder handles GET/POST /order/:id/extend
func (h *WebHandler) HandleExtendOrder(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Extract order ID
	parts := strings.Split(r.URL.Path, "/")
	orderID := 0
	for i, p := range parts {
		if p == "order" && i+1 < len(parts) {
			orderID, _ = strconv.Atoi(parts[i+1])
			break
		}
	}

	if orderID == 0 {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	// Get order details
	orderResp, err := h.service.GetOrder(customer.ID, orderID)
	if err != nil {
		http.Redirect(w, r, "/dashboard?error="+url.QueryEscape("Order not found"), http.StatusSeeOther)
		return
	}

	// Only active orders can be extended
	if orderResp.Order.Status != OrderStatusActive {
		http.Redirect(w, r, "/dashboard?error="+url.QueryEscape("Only active orders can be extended"), http.StatusSeeOther)
		return
	}

	// Get balance
	balance, _ := h.service.GetBalance(customer.ID)
	maxBudgetBTC := fmt.Sprintf("%.8f", float64(balance.BalanceSat)/100000000)

	data := map[string]interface{}{
		"LoggedIn":      true,
		"Title":         fmt.Sprintf("Extend Order #%d", orderID),
		"Order":         orderResp.Order,
		"Remaining":     orderResp.Order.BudgetSat - orderResp.Order.AmountSpentSat,
		"BalanceSat":    balance.BalanceSat,
		"BalanceBTC":    balance.BalanceBTC,
		"MaxBudgetBTC":  maxBudgetBTC,
		"_sessionToken": session.SessionToken,
	}

	if r.Method == http.MethodPost {
		// Parse additional budget
		additionalBTC, _ := strconv.ParseFloat(r.FormValue("additional_btc"), 64)
		if math.IsNaN(additionalBTC) || math.IsInf(additionalBTC, 0) || additionalBTC <= 0 {
			data["Error"] = "Please enter a valid amount"
			h.renderWithRequest(w, r, "order_extend.html", data)
			return
		}

		additionalSat := int64(additionalBTC * 100000000)

		// Extend the order
		_, err := h.service.ExtendOrder(customer.ID, orderID, additionalSat)
		if err != nil {
			data["Error"] = err.Error()
			h.renderWithRequest(w, r, "order_extend.html", data)
			return
		}

		// Redirect to dashboard with success
		http.Redirect(w, r, "/dashboard?success="+url.QueryEscape("Order extended successfully"), http.StatusSeeOther)
		return
	}

	h.renderWithRequest(w, r, "order_extend.html", data)
}

// HandleOrderDetail handles GET /order/:id - shows order details
func (h *WebHandler) HandleOrderDetail(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Extract order ID from path: /order/123
	parts := strings.Split(r.URL.Path, "/")
	orderID := 0
	for i, p := range parts {
		if p == "order" && i+1 < len(parts) {
			orderID, _ = strconv.Atoi(parts[i+1])
			break
		}
	}

	if orderID == 0 {
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}

	// Get order details
	orderResp, err := h.service.GetOrder(customer.ID, orderID)
	if err != nil {
		http.Redirect(w, r, "/orders?error="+url.QueryEscape("Order not found"), http.StatusSeeOther)
		return
	}

	remaining := orderResp.Order.BudgetSat - orderResp.Order.AmountSpentSat
	progressPct := 0.0
	if orderResp.Order.BudgetSat > 0 {
		progressPct = float64(orderResp.Order.AmountSpentSat) / float64(orderResp.Order.BudgetSat) * 100
	}

	data := map[string]interface{}{
		"LoggedIn":      true,
		"Title":         fmt.Sprintf("Order #%d", orderID),
		"Order":         orderResp.Order,
		"Remaining":     remaining,
		"ProgressPct":   progressPct,
		"_sessionToken": session.SessionToken,
	}

	h.renderWithRequest(w, r, "order_detail.html", data)
}

// HandleMining handles GET /mining/:id - shows rental mining dashboard
func (h *WebHandler) HandleMining(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Extract order ID from path
	parts := strings.Split(r.URL.Path, "/")
	orderID := 0
	for i, p := range parts {
		if p == "mining" && i+1 < len(parts) {
			orderID, _ = strconv.Atoi(parts[i+1])
			break
		}
	}

	if orderID == 0 {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	// Get order details
	orderResp, err := h.service.GetOrder(customer.ID, orderID)
	if err != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}

	order := orderResp.Order

	// For solo mining, serve solo.html
	if order.MiningMode == "solo" {
		h.serveSoloPage(w, r, order)
		return
	}

	// For PPLNS, serve pplns.html
	h.servePPLNSPage(w, r, order)
}

// getMinerAddress extracts the BCH2 address from target_identity and adds prefix
func getMinerAddress(targetIdentity string) string {
	// target_identity format: address.workername
	parts := strings.Split(targetIdentity, ".")
	address := parts[0]
	// Add bitcoincashii: prefix if not present
	if !strings.HasPrefix(address, "bitcoincashii:") {
		address = "bitcoincashii:" + address
	}
	return address
}

// serveSoloPage serves the solo mining page as a standalone HTML file
func (h *WebHandler) serveSoloPage(w http.ResponseWriter, r *http.Request, order *Order) {
	soloPath := filepath.Join(h.templateDir, "solo.html")
	content, err := os.ReadFile(soloPath)
	if err != nil {
		log.Printf("Failed to read solo.html: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Inject miner address and rental data before the closing </head> tag
	// Using json.Marshal for safe JavaScript injection (prevents XSS attacks)
	minerAddress := getMinerAddress(order.TargetIdentity)
	budgetBTC := float64(order.BudgetSat) / 100000000
	spentBTC := float64(order.AmountSpentSat) / 100000000

	// Fetch real-time data from Braiins API if order is active
	var currentSpeedPH float64
	var progressPct float64
	if order.Status == "active" && order.BraiinsBidID != "" && h.service.braiins != nil {
		if bid, err := h.service.braiins.GetBidDetail(order.BraiinsBidID); err == nil {
			currentSpeedPH = bid.AvgSpeedPH
			progressPct = bid.ProgressPct
			// Use Braiins spend data for accuracy
			spentBTC = float64(bid.AmountSpentSat) / 100000000
		}
	}

	rentalData := map[string]interface{}{
		"orderId":        order.ID,
		"orderedPH":      order.SpeedLimitPH,
		"currentSpeedPH": currentSpeedPH,
		"budgetBTC":      budgetBTC,
		"spentBTC":       spentBTC,
		"progressPct":    progressPct,
		"status":         order.Status,
		"miningMode":     order.MiningMode,
	}
	rentalJSON, _ := json.Marshal(rentalData)
	minerAddrJSON, _ := json.Marshal(minerAddress)

	// Include WebSocket real-time update code
	// Check X-Forwarded-Proto header (set by nginx) since TLS is terminated at proxy
	wsProtocol := "wss:"
	if r.Header.Get("X-Forwarded-Proto") == "http" || (r.Header.Get("X-Forwarded-Proto") == "" && r.TLS == nil) {
		wsProtocol = "ws:"
	}

	injection := fmt.Sprintf(`<script>
window.HASHFORGE_MINER_ADDRESS = %s;
window.HASHFORGE_RENTAL = %s;

// WebSocket for real-time order updates
(function() {
    var wsUrl = '%s//' + window.location.host + '/ws';
    var ws = null;
    var reconnectDelay = 1000;

    function connect() {
        ws = new WebSocket(wsUrl);
        ws.onopen = function() {
            console.log('WebSocket connected');
            reconnectDelay = 1000;
        };
        ws.onmessage = function(e) {
            try {
                var msg = JSON.parse(e.data);
                if (msg.type === 'order_update' && msg.payload.order_id === window.HASHFORGE_RENTAL.orderId) {
                    window.HASHFORGE_RENTAL.spentBTC = parseFloat(msg.payload.spent_btc);
                    window.HASHFORGE_RENTAL.status = msg.payload.status;
                    var spentEl = document.getElementById('spentBTC');
                    if (spentEl) spentEl.textContent = msg.payload.spent_btc;
                    var statusEl = document.getElementById('orderStatus');
                    if (statusEl) statusEl.textContent = msg.payload.status;
                    if (msg.payload.status === 'completed' || msg.payload.status === 'cancelled') {
                        var statusBadge = document.querySelector('.order-status');
                        if (statusBadge) {
                            statusBadge.className = 'status-badge status-' + msg.payload.status;
                            statusBadge.textContent = msg.payload.status.charAt(0).toUpperCase() + msg.payload.status.slice(1);
                        }
                    }
                }
            } catch(err) {}
        };
        ws.onclose = function() {
            setTimeout(connect, reconnectDelay);
            reconnectDelay = Math.min(reconnectDelay * 2, 30000);
        };
        ws.onerror = function() { ws.close(); };
    }
    connect();
})();
</script></head>`, string(minerAddrJSON), string(rentalJSON), wsProtocol)
	contentStr := strings.Replace(string(content), "</head>", injection, 1)

	serveWithNonce(w, contentStr)
}

// HandleSolo serves the solo mining page directly with an address parameter
func (h *WebHandler) HandleSolo(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}

	soloPath := filepath.Join(h.templateDir, "solo.html")
	content, err := os.ReadFile(soloPath)
	if err != nil {
		log.Printf("Failed to read solo.html: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Try to find an active order for this address
	var rentalJSON []byte
	_, customer := h.getSession(r)
	if customer != nil {
		// Normalize address for comparison (add prefix if missing)
		normalizedAddress := address
		if !strings.HasPrefix(normalizedAddress, "bitcoincashii:") {
			normalizedAddress = "bitcoincashii:" + normalizedAddress
		}

		// Look for active solo orders matching this address
		orders, _ := h.service.GetOrders(customer.ID)
		for _, order := range orders {
			if order.Status == "active" && order.MiningMode == "solo" {
				// Check if target_identity matches address
				orderAddr := getMinerAddress(order.TargetIdentity)
				if orderAddr == normalizedAddress {
					budgetBTC := float64(order.BudgetSat) / 100000000
					spentBTC := float64(order.AmountSpentSat) / 100000000

					// Fetch real-time data from Braiins API if order is active
					var currentSpeedPH float64
					var progressPct float64
					if order.BraiinsBidID != "" && h.service.braiins != nil {
						if bid, err := h.service.braiins.GetBidDetail(order.BraiinsBidID); err == nil {
							currentSpeedPH = bid.AvgSpeedPH
							progressPct = bid.ProgressPct
							// Use Braiins spend data for accuracy
							spentBTC = float64(bid.AmountSpentSat) / 100000000
						}
					}

					rentalData := map[string]interface{}{
						"orderId":        order.ID,
						"orderedPH":      order.SpeedLimitPH,
						"currentSpeedPH": currentSpeedPH,
						"budgetBTC":      budgetBTC,
						"spentBTC":       spentBTC,
						"progressPct":    progressPct,
						"status":         order.Status,
						"miningMode":     order.MiningMode,
					}
					rentalJSON, _ = json.Marshal(rentalData)
					break
				}
			}
		}
	}

	// Inject miner address and rental data before the closing </head> tag
	minerAddrJSON, _ := json.Marshal(address)

	var injection string
	if rentalJSON != nil {
		// Include WebSocket for real-time updates
		wsProtocol := "wss:"
		if r.Header.Get("X-Forwarded-Proto") == "http" || (r.Header.Get("X-Forwarded-Proto") == "" && r.TLS == nil) {
			wsProtocol = "ws:"
		}
		injection = fmt.Sprintf(`<script>
window.HASHFORGE_MINER_ADDRESS = %s;
window.HASHFORGE_RENTAL = %s;

// WebSocket for real-time order updates
(function() {
    var wsUrl = '%s//' + window.location.host + '/ws';
    var ws = null;
    var reconnectDelay = 1000;

    function connect() {
        ws = new WebSocket(wsUrl);
        ws.onopen = function() { reconnectDelay = 1000; };
        ws.onmessage = function(e) {
            try {
                var msg = JSON.parse(e.data);
                if (msg.type === 'order_update' && msg.payload.order_id === window.HASHFORGE_RENTAL.orderId) {
                    window.HASHFORGE_RENTAL.spentBTC = parseFloat(msg.payload.spent_btc);
                    window.HASHFORGE_RENTAL.status = msg.payload.status;
                }
            } catch(err) {}
        };
        ws.onclose = function() { setTimeout(connect, reconnectDelay); reconnectDelay = Math.min(reconnectDelay * 2, 30000); };
        ws.onerror = function() { ws.close(); };
    }
    connect();
})();
</script></head>`, string(minerAddrJSON), string(rentalJSON), wsProtocol)
	} else {
		injection = fmt.Sprintf(`<script>
window.HASHFORGE_MINER_ADDRESS = %s;
window.HASHFORGE_RENTAL = null;
</script></head>`, string(minerAddrJSON))
	}

	contentStr := strings.Replace(string(content), "</head>", injection, 1)

	serveWithNonce(w, contentStr)
}

// HandlePPLNS serves the PPLNS mining page directly with an address parameter
func (h *WebHandler) HandlePPLNS(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}

	// Try to find an active order for this address
	_, customer := h.getSession(r)
	if customer != nil {
		// Normalize address for comparison (add prefix if missing)
		normalizedAddress := address
		if !strings.HasPrefix(normalizedAddress, "bitcoincashii:") {
			normalizedAddress = "bitcoincashii:" + normalizedAddress
		}

		orders, _ := h.service.GetOrders(customer.ID)
		for _, order := range orders {
			if order.Status == "active" && order.MiningMode == "pplns" {
				orderAddr := getMinerAddress(order.TargetIdentity)
				if orderAddr == normalizedAddress {
					h.servePPLNSPage(w, r, &order)
					return
				}
			}
		}
	}

	// No active order found, serve page without rental data
	pplnsPath := filepath.Join(h.templateDir, "pplns.html")
	content, err := os.ReadFile(pplnsPath)
	if err != nil {
		log.Printf("Failed to read pplns.html: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	minerAddrJSON, _ := json.Marshal(address)
	injection := fmt.Sprintf(`<script>
window.HASHFORGE_MINER_ADDRESS = %s;
window.HASHFORGE_RENTAL = null;
</script></head>`, string(minerAddrJSON))

	contentStr := strings.Replace(string(content), "</head>", injection, 1)
	serveWithNonce(w, contentStr)
}

// servePPLNSPage serves the PPLNS mining page as a standalone HTML file
func (h *WebHandler) servePPLNSPage(w http.ResponseWriter, r *http.Request, order *Order) {
	pplnsPath := filepath.Join(h.templateDir, "pplns.html")
	content, err := os.ReadFile(pplnsPath)
	if err != nil {
		log.Printf("Failed to read pplns.html: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Inject miner address and rental data before the closing </head> tag
	// Using json.Marshal for safe JavaScript injection (prevents XSS attacks)
	minerAddress := getMinerAddress(order.TargetIdentity)
	budgetBTC := float64(order.BudgetSat) / 100000000
	spentBTC := float64(order.AmountSpentSat) / 100000000

	// Fetch real-time data from Braiins API if order is active
	var currentSpeedPH float64
	var progressPct float64
	if order.Status == "active" && order.BraiinsBidID != "" && h.service.braiins != nil {
		if bid, err := h.service.braiins.GetBidDetail(order.BraiinsBidID); err == nil {
			currentSpeedPH = bid.AvgSpeedPH
			progressPct = bid.ProgressPct
			// Use Braiins spend data for accuracy
			spentBTC = float64(bid.AmountSpentSat) / 100000000
		}
	}

	rentalData := map[string]interface{}{
		"orderId":        order.ID,
		"orderedPH":      order.SpeedLimitPH,
		"currentSpeedPH": currentSpeedPH,
		"budgetBTC":      budgetBTC,
		"spentBTC":       spentBTC,
		"progressPct":    progressPct,
		"status":         order.Status,
		"miningMode":     order.MiningMode,
	}
	rentalJSON, _ := json.Marshal(rentalData)
	minerAddrJSON, _ := json.Marshal(minerAddress)

	// Include WebSocket real-time update code
	// Check X-Forwarded-Proto header (set by nginx) since TLS is terminated at proxy
	wsProtocol := "wss:"
	if r.Header.Get("X-Forwarded-Proto") == "http" || (r.Header.Get("X-Forwarded-Proto") == "" && r.TLS == nil) {
		wsProtocol = "ws:"
	}

	injection := fmt.Sprintf(`<script>
window.HASHFORGE_MINER_ADDRESS = %s;
window.HASHFORGE_RENTAL = %s;

// WebSocket for real-time order updates
(function() {
    var wsUrl = '%s//' + window.location.host + '/ws';
    var ws = null;
    var reconnectDelay = 1000;

    function connect() {
        ws = new WebSocket(wsUrl);
        ws.onopen = function() {
            console.log('WebSocket connected');
            reconnectDelay = 1000;
        };
        ws.onmessage = function(e) {
            try {
                var msg = JSON.parse(e.data);
                if (msg.type === 'order_update' && msg.payload.order_id === window.HASHFORGE_RENTAL.orderId) {
                    window.HASHFORGE_RENTAL.spentBTC = parseFloat(msg.payload.spent_btc);
                    window.HASHFORGE_RENTAL.status = msg.payload.status;
                    var spentEl = document.getElementById('spentBTC');
                    if (spentEl) spentEl.textContent = msg.payload.spent_btc;
                    var statusEl = document.getElementById('orderStatus');
                    if (statusEl) statusEl.textContent = msg.payload.status;
                    if (msg.payload.status === 'completed' || msg.payload.status === 'cancelled') {
                        var statusBadge = document.querySelector('.order-status');
                        if (statusBadge) {
                            statusBadge.className = 'status-badge status-' + msg.payload.status;
                            statusBadge.textContent = msg.payload.status.charAt(0).toUpperCase() + msg.payload.status.slice(1);
                        }
                    }
                }
            } catch(err) {}
        };
        ws.onclose = function() {
            setTimeout(connect, reconnectDelay);
            reconnectDelay = Math.min(reconnectDelay * 2, 30000);
        };
        ws.onerror = function() { ws.close(); };
    }
    connect();
})();
</script></head>`, string(minerAddrJSON), string(rentalJSON), wsProtocol)
	contentStr := strings.Replace(string(content), "</head>", injection, 1)

	serveWithNonce(w, contentStr)
}

// HandleOrderAPI handles /api/order/{id}/speed - returns current Braiins speed
func (h *WebHandler) HandleOrderAPI(w http.ResponseWriter, r *http.Request) {
	// Extract order ID from path: /api/order/{id}/speed
	path := strings.TrimPrefix(r.URL.Path, "/api/order/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "speed" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	orderID, err := strconv.Atoi(parts[0])
	if err != nil {
		http.Error(w, "Invalid order ID", http.StatusBadRequest)
		return
	}

	// Verify order belongs to customer
	session, customer := h.getSession(r)
	if session == nil || customer == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	orderResp, err := h.service.GetOrder(customer.ID, orderID)
	if err != nil || orderResp.Order == nil {
		http.Error(w, "Order not found", http.StatusNotFound)
		return
	}
	order := orderResp.Order

	// Fetch current speed from Braiins
	var currentSpeedPH float64
	var progressPct float64
	if order.Status == "active" && order.BraiinsBidID != "" && h.service.braiins != nil {
		if bid, err := h.service.braiins.GetBidDetail(order.BraiinsBidID); err == nil {
			currentSpeedPH = bid.AvgSpeedPH
			progressPct = bid.ProgressPct
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"currentSpeedPH": currentSpeedPH,
		"progressPct":    progressPct,
		"orderedPH":      order.SpeedLimitPH,
		"status":         order.Status,
	})
}

// Handle2FASetup handles GET/POST /2fa/setup
func (h *WebHandler) Handle2FASetup(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	status, _ := h.service.Get2FAStatus(customer.ID)

	data := map[string]interface{}{
		"LoggedIn":             true,
		"Title":                "2FA Setup",
		"Enabled":              status.Enabled,
		"BackupCodesRemaining": status.BackupCodes,
		"_sessionToken":        session.SessionToken, // For CSRF token derivation
	}

	if status.Enabled {
		h.renderWithRequest(w, r, "2fa_setup.html", data)
		return
	}

	if r.Method == http.MethodPost {
		result, err := h.service.Setup2FA(customer.ID, customer.Email)
		if err != nil {
			data["Error"] = err.Error()
			h.renderWithRequest(w, r, "2fa_setup.html", data)
			return
		}

		data["Secret"] = result.Secret
		data["QRCodeURI"] = result.QRCodeURI
		data["QRCodeImage"] = qrDataURI(result.QRCodeURI)
	}

	h.renderWithRequest(w, r, "2fa_setup.html", data)
}

// qrDataURI renders content to a QR-code PNG returned as an inline data: URI, so
// QR codes (2FA otpauth secrets, deposit addresses) are generated in this origin
// rather than sent to a third-party image service.
func qrDataURI(content string) string {
	png, err := qrcode.Encode(content, qrcode.Medium, 240)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// Handle2FAVerify handles POST /2fa/verify
func (h *WebHandler) Handle2FAVerify(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	code := r.FormValue("code")

	err := h.service.Verify2FA(customer.ID, code)
	if err != nil {
		// Re-show setup page with error
		result, _ := h.service.Setup2FA(customer.ID, customer.Email)
		data := map[string]interface{}{
			"LoggedIn":      true,
			"Title":         "2FA Setup",
			"Error":         "Invalid code. Please try again.",
			"Secret":        result.Secret,
			"QRCodeURI":     result.QRCodeURI,
			"QRCodeImage":   qrDataURI(result.QRCodeURI),
			"_sessionToken": session.SessionToken,
		}
		h.renderWithRequest(w, r, "2fa_setup.html", data)
		return
	}

	// Get backup codes to show
	codes, _ := h.service.RegenerateBackupCodes(customer.ID, code)

	h.renderWithRequest(w, r, "2fa_backup.html", map[string]interface{}{
		"LoggedIn":      true,
		"Title":         "Backup Codes",
		"BackupCodes":   codes.Codes,
		"_sessionToken": session.SessionToken,
	})
}

// Handle2FABackupCodes handles GET/POST /2fa/backup-codes
func (h *WebHandler) Handle2FABackupCodes(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"LoggedIn":      true,
		"Title":         "Backup Codes",
		"_sessionToken": session.SessionToken, // For CSRF token derivation
	}

	if r.Method == http.MethodPost {
		code := r.FormValue("code")
		codes, err := h.service.RegenerateBackupCodes(customer.ID, code)
		if err != nil {
			data["Error"] = err.Error()
			h.renderWithRequest(w, r, "2fa_backup.html", data)
			return
		}
		data["BackupCodes"] = codes.Codes
	}

	h.renderWithRequest(w, r, "2fa_backup.html", data)
}

// HandleWithdraw handles GET/POST /withdraw
func (h *WebHandler) HandleWithdraw(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	balance, _ := h.service.GetBalance(customer.ID)
	twoFA, _ := h.service.Get2FAStatus(customer.ID)
	withdrawals, _ := h.service.GetWithdrawals(customer.ID)

	// Filter pending
	var pending []Withdrawal
	for _, w := range withdrawals {
		if w.Status == WithdrawalStatusPending || w.Status == WithdrawalStatusApproved {
			pending = append(pending, w)
		}
	}

	data := map[string]interface{}{
		"LoggedIn":           true,
		"Title":              "Withdraw",
		"BalanceSat":         balance.BalanceSat,
		"BalanceBTC":         balance.BalanceBTC,
		"TwoFAEnabled":       twoFA.Enabled,
		"MinWithdrawal":      h.service.getSetting("min_withdrawal_sat", 50000),
		"MaxWithdrawal":      h.service.getSetting("max_withdrawal_sat", 10000000),
		"WithdrawalFee":      h.service.getSetting("withdrawal_fee_sat", 1000),
		"PendingWithdrawals": pending,
		"_sessionToken":      session.SessionToken, // For CSRF token derivation
	}

	if r.Method == http.MethodPost {
		// Rate limit: 5 withdrawal requests per hour per customer
		rateLimitKey := fmt.Sprintf("withdraw:%d", customer.ID)
		if !h.service.CheckRateLimit(rateLimitKey, "withdraw", 5, 3600) {
			data["Error"] = "Too many withdrawal requests. Please try again later."
			h.renderWithRequest(w, r, "withdraw.html", data)
			return
		}

		btcAddr := r.FormValue("btc_address")
		totpCode := r.FormValue("totp_code")

		// Convert BTC to satoshis
		var amountSat int64
		amountBTCStr := r.FormValue("amount_btc")
		if amountBTCStr != "" {
			amountBTC, _ := strconv.ParseFloat(amountBTCStr, 64)
			amountSat = int64(amountBTC * 100000000)
		}

		req := &WithdrawRequest{
			BTCAddress: btcAddr,
			AmountSat:  amountSat,
			TOTPCode:   totpCode,
		}

		_, err := h.service.RequestWithdrawal(customer.ID, req)
		if err != nil {
			data["Error"] = err.Error()
			h.renderWithRequest(w, r, "withdraw.html", data)
			return
		}

		data["Success"] = "Withdrawal request submitted. It will be processed shortly."
	}

	h.renderWithRequest(w, r, "withdraw.html", data)
}

// HandleFAQ handles GET /faq
func (h *WebHandler) HandleFAQ(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)

	data := map[string]interface{}{
		"LoggedIn": session != nil,
		"Title":    "FAQ",
	}
	if customer != nil {
		data["Customer"] = customer
	}

	h.renderWithRequest(w, r, "faq.html", data)
}

// HandleTerms handles GET /terms
func (h *WebHandler) HandleTerms(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)

	data := map[string]interface{}{
		"LoggedIn": session != nil,
		"Title":    "Terms of Service",
	}
	if customer != nil {
		data["Customer"] = customer
	}

	h.renderWithRequest(w, r, "terms.html", data)
}

// HandlePrivacy handles GET /privacy
func (h *WebHandler) HandlePrivacy(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)

	data := map[string]interface{}{
		"LoggedIn": session != nil,
		"Title":    "Privacy Policy",
	}
	if customer != nil {
		data["Customer"] = customer
	}

	h.renderWithRequest(w, r, "privacy.html", data)
}

// HandleRisk handles GET /risk
func (h *WebHandler) HandleRisk(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)

	data := map[string]interface{}{
		"LoggedIn": session != nil,
		"Title":    "Risk Disclosure",
	}
	if customer != nil {
		data["Customer"] = customer
	}

	h.renderWithRequest(w, r, "risk.html", data)
}

// HandleRefunds handles GET /refunds
func (h *WebHandler) HandleRefunds(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)

	data := map[string]interface{}{
		"LoggedIn": session != nil,
		"Title":    "Refund Policy",
	}
	if customer != nil {
		data["Customer"] = customer
	}

	h.renderWithRequest(w, r, "refunds.html", data)
}

// HandleContact handles GET/POST /contact
func (h *WebHandler) HandleContact(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)

	data := map[string]interface{}{
		"LoggedIn":         session != nil,
		"Title":            "Contact Us",
		"TurnstileSiteKey": h.turnstileSiteKey,
	}
	if customer != nil {
		data["Customer"] = customer
		data["Email"] = customer.Email
	}

	if r.Method == http.MethodPost {
		clientIP := getClientIPWeb(r)

		// Rate limit: 3 messages per hour
		if !h.service.CheckRateLimit(clientIP, "contact", 3, 3600) {
			data["Error"] = "Too many messages. Please try again later."
			h.renderWithRequest(w, r, "contact.html", data)
			return
		}

		// Verify Turnstile CAPTCHA
		turnstileToken := r.FormValue("cf-turnstile-response")
		if valid, err := VerifyTurnstile(h.turnstileSecret, turnstileToken, clientIP); !valid {
			if err != nil {
				log.Printf("Turnstile verification error: %v", err)
			}
			data["Error"] = "CAPTCHA verification failed. Please try again."
			h.renderWithRequest(w, r, "contact.html", data)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		email := strings.TrimSpace(r.FormValue("email"))
		subject := strings.TrimSpace(r.FormValue("subject"))
		message := strings.TrimSpace(r.FormValue("message"))

		// Validation
		if name == "" || email == "" || subject == "" || message == "" {
			data["Error"] = "All fields are required."
			data["Name"] = name
			data["Email"] = email
			data["Subject"] = subject
			data["Message"] = message
			h.renderWithRequest(w, r, "contact.html", data)
			return
		}

		if err := ValidateEmail(email); err != nil {
			data["Error"] = "Please enter a valid email address."
			h.renderWithRequest(w, r, "contact.html", data)
			return
		}

		// Save to database
		var customerID *int
		if customer != nil {
			customerID = &customer.ID
		}

		err := h.service.SaveContactMessage(customerID, name, email, subject, message, clientIP)
		if err != nil {
			log.Printf("Failed to save contact message: %v", err)
			data["Error"] = "Failed to send message. Please try again."
			h.renderWithRequest(w, r, "contact.html", data)
			return
		}

		// Send notification email to admin
		go h.service.SendContactNotification(name, email, subject, message)

		data["Success"] = true
	}

	h.renderWithRequest(w, r, "contact.html", data)
}

// HandleHowItWorks handles GET /how-it-works
func (h *WebHandler) HandleHowItWorks(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)

	data := map[string]interface{}{
		"LoggedIn": session != nil,
		"Title":    "How It Works",
	}
	if customer != nil {
		data["Customer"] = customer
	}

	h.renderWithRequest(w, r, "how-it-works.html", data)
}

// HandleOrders handles GET /orders - full order history
func (h *WebHandler) HandleOrders(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	orders, _ := h.service.GetOrders(customer.ID)

	// Calculate stats
	var totalSpent int64
	var activeCount int
	for _, o := range orders {
		totalSpent += o.AmountSpentSat
		if o.Status == OrderStatusActive {
			activeCount++
		}
	}

	data := map[string]interface{}{
		"LoggedIn":      true,
		"Title":         "Order History",
		"Orders":        orders,
		"TotalOrders":   len(orders),
		"TotalSpentBTC": fmt.Sprintf("%.8f", float64(totalSpent)/100000000),
		"ActiveCount":   activeCount,
	}

	h.renderWithRequest(w, r, "orders.html", data)
}

// HandleOrderAnalytics handles GET /orders/analytics
func (h *WebHandler) HandleOrderAnalytics(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	analytics := h.service.GetOrderAnalytics(customer.ID)

	data := map[string]interface{}{
		"LoggedIn":      true,
		"Title":         "Order Analytics",
		"Analytics":     analytics,
		"TotalSpentBTC": fmt.Sprintf("%.8f", float64(analytics.TotalSpentSat)/100000000),
		"AvgOrderBTC":   fmt.Sprintf("%.8f", float64(analytics.AvgOrderSizeSat)/100000000),
	}

	h.renderWithRequest(w, r, "order_analytics.html", data)
}

// HandleTransactions handles GET /transactions - full transaction history
func (h *WebHandler) HandleTransactions(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}

	// Get transactions (up to 100)
	transactions, _, _ := h.service.GetTransactionHistory(customer.ID, 100, 0)

	balance, _ := h.service.GetBalance(customer.ID)

	// Calculate running totals (newest first, work backwards from current balance)
	if balance != nil {
		runningTotal := balance.BalanceSat
		for i := range transactions {
			transactions[i].RunningTotal = runningTotal
			runningTotal -= transactions[i].AmountSat
		}
	}

	// Filter if needed
	var filtered []LedgerEntry
	if filter != "all" {
		for _, t := range transactions {
			if t.TxType == filter {
				filtered = append(filtered, t)
			}
		}
	} else {
		filtered = transactions
	}

	// Calculate stats
	var totalDeposited, totalWithdrawn int64
	for _, t := range transactions {
		if t.TxType == "deposit" {
			totalDeposited += t.AmountSat
		} else if t.TxType == "withdrawal" {
			totalWithdrawn += -t.AmountSat // withdrawals are negative
		}
	}

	var balanceBTC string
	if balance != nil {
		balanceBTC = balance.BalanceBTC
	} else {
		balanceBTC = "0.00000000"
	}

	data := map[string]interface{}{
		"LoggedIn":          true,
		"Title":             "Transaction History",
		"Transactions":      filtered,
		"Filter":            filter,
		"TotalDepositedBTC": fmt.Sprintf("%.8f", float64(totalDeposited)/100000000),
		"TotalWithdrawnBTC": fmt.Sprintf("%.8f", float64(totalWithdrawn)/100000000),
		"BalanceBTC":        balanceBTC,
	}

	h.renderWithRequest(w, r, "transactions.html", data)
}

// HandleStatus handles GET /status - system status page
func (h *WebHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)

	// Check services
	dbOK := h.service.CheckDBHealth()
	braiinsOK := h.service.CheckBraiinsHealth()

	// Get basic stats
	stats, _ := h.service.GetAdminStats()

	data := map[string]interface{}{
		"LoggedIn":       session != nil,
		"Title":          "System Status",
		"LastUpdated":    time.Now().Format("Jan 2, 2006 3:04 PM MST"),
		"AllOperational": dbOK && braiinsOK,
		"Services": map[string]bool{
			"Platform": true,
			"Database": dbOK,
			"Braiins":  braiinsOK,
			"Bitcoin":  true,
			"Pool":     true,
		},
		"Stats": map[string]interface{}{
			"ActiveOrders":      stats.ActiveOrders,
			"TotalHashratePH":   stats.TotalActiveHashrate,
			"NetworkDifficulty": "N/A",
		},
		"Incidents": []interface{}{},
	}

	if customer != nil {
		data["Customer"] = customer
	}

	h.renderWithRequest(w, r, "status.html", data)
}

// HandleSettings handles GET/POST /settings
func (h *WebHandler) HandleSettings(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"LoggedIn":      true,
		"Title":         "Settings",
		"Customer":      customer,
		"BCH2Address":   customer.BCH2Address,
		"_sessionToken": session.SessionToken, // For CSRF token derivation
	}

	if r.Method == http.MethodPost {
		bch2Address := r.FormValue("bch2_address")

		err := h.service.UpdateBCH2Address(customer.ID, bch2Address)
		if err != nil {
			data["Error"] = err.Error()
			h.renderWithRequest(w, r, "settings.html", data)
			return
		}

		data["Success"] = "BCH2 payout address updated successfully"
		// Ensure prefix is shown consistently
		normalized := strings.ToLower(bch2Address)
		if !strings.HasPrefix(normalized, "bitcoincashii:") {
			normalized = "bitcoincashii:" + normalized
		}
		data["BCH2Address"] = normalized
	}

	h.renderWithRequest(w, r, "settings.html", data)
}

// HandleLogoutAllSessions terminates all sessions except current (security feature)
func (h *WebHandler) HandleLogoutAllSessions(w http.ResponseWriter, r *http.Request) {
	session, _ := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	count, err := h.service.InvalidateAllSessions(session.CustomerID, session.SessionToken)
	if err != nil {
		http.Redirect(w, r, "/settings?error=logout_failed", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/settings?success=Logged+out+%d+other+sessions", count), http.StatusSeeOther)
}

// HandleExportData handles GET /settings/export - exports user data as JSON
func (h *WebHandler) HandleExportData(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Get all user data
	balance, _ := h.service.GetBalance(customer.ID)
	orders, _ := h.service.GetOrders(customer.ID)
	transactions, _, _ := h.service.GetTransactionHistory(customer.ID, 1000, 0)
	twoFAStatus, _ := h.service.Get2FAStatus(customer.ID)

	// Build export data
	exportData := map[string]interface{}{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"account": map[string]interface{}{
			"email":          customer.Email,
			"bch2_address":   customer.BCH2Address,
			"created_at":     customer.CreatedAt,
			"email_verified": customer.EmailVerified,
			"two_fa_enabled": twoFAStatus.Enabled,
		},
		"balance": map[string]interface{}{
			"available_sat":   balance.BalanceSat,
			"available_btc":   balance.BalanceBTC,
			"pending_deposit": balance.PendingDeposit,
		},
		"orders":       orders,
		"transactions": transactions,
	}

	// Set headers for JSON download
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=hashforge-export-%s.json", time.Now().Format("2006-01-02")))

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.Encode(exportData)
}

// HandleDeleteAccount handles POST /settings/delete-account
func (h *WebHandler) HandleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	// Check for active orders
	orders, _ := h.service.GetOrders(customer.ID)
	for _, order := range orders {
		if order.Status == OrderStatusActive || order.Status == OrderStatusPending {
			http.Redirect(w, r, "/settings?error="+url.QueryEscape("Please cancel all active orders before deleting your account"), http.StatusSeeOther)
			return
		}
	}

	// Check for remaining balance
	balance, _ := h.service.GetBalance(customer.ID)
	if balance.BalanceSat > 0 {
		http.Redirect(w, r, "/settings?error="+url.QueryEscape("Please withdraw your remaining balance before deleting your account"), http.StatusSeeOther)
		return
	}

	// Delete the account
	err := h.service.DeleteAccount(customer.ID)
	if err != nil {
		http.Redirect(w, r, "/settings?error="+url.QueryEscape("Failed to delete account: "+err.Error()), http.StatusSeeOther)
		return
	}

	// Clear session cookie (real name is sessionCookieName; the previous literal
	// "session" cookie never existed, so the session cookie was left intact).
	h.clearSession(w)

	http.Redirect(w, r, "/?deleted=1", http.StatusSeeOther)
}

// HandleBlocks handles GET /blocks - shows user's blocks
func (h *WebHandler) HandleBlocks(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	blocksPath := filepath.Join(h.templateDir, "blocks.html")
	content, err := os.ReadFile(blocksPath)
	if err != nil {
		log.Printf("Failed to read blocks.html: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Inject miner address from customer's BCH2 address
	minerAddress := ""
	if customer.BCH2Address != "" {
		addr := strings.Split(customer.BCH2Address, ".")[0] // Remove worker suffix if any
		if !strings.HasPrefix(addr, "bitcoincashii:") {
			addr = "bitcoincashii:" + addr
		}
		minerAddress = addr
	}

	// Use json.Marshal for safe JavaScript injection (prevents XSS)
	minerAddrJSON, _ := json.Marshal(minerAddress)
	injection := fmt.Sprintf(`<script>window.HASHFORGE_MINER_ADDRESS = %s;</script></head>`, string(minerAddrJSON))
	contentStr := strings.Replace(string(content), "</head>", injection, 1)

	serveWithNonce(w, contentStr)
}

// ============================================================================
// Security Handlers
// ============================================================================

// HandleSecurity shows security events page
func (h *WebHandler) HandleSecurity(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	events, _ := h.service.GetSecurityEvents(customer.ID, 50)

	data := map[string]interface{}{
		"Customer":       customer,
		"CSRFToken":      deriveCSRFToken(session.SessionToken),
		"SecurityEvents": events,
	}

	h.renderWithRequest(w, r, "security.html", data)
}

// HandleSessions handles session management
func (h *WebHandler) HandleSessions(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method == http.MethodPost {
		sessionID, _ := strconv.Atoi(r.FormValue("session_id"))
		if sessionID > 0 {
			h.service.RevokeSession(customer.ID, sessionID)
		}
		http.Redirect(w, r, "/security/sessions?success=1", http.StatusSeeOther)
		return
	}

	sessions, _ := h.service.GetActiveSessions(customer.ID, session.SessionToken)

	data := map[string]interface{}{
		"Customer":  customer,
		"CSRFToken": deriveCSRFToken(session.SessionToken),
		"Sessions":  sessions,
		"Success":   r.URL.Query().Get("success") == "1",
	}

	h.renderWithRequest(w, r, "sessions.html", data)
}

// HandleChangePassword handles password change
func (h *WebHandler) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"Customer":  customer,
		"CSRFToken": deriveCSRFToken(session.SessionToken),
	}

	if r.Method == http.MethodPost {
		currentPassword := r.FormValue("current_password")
		newPassword := r.FormValue("new_password")
		confirmPassword := r.FormValue("confirm_password")

		if newPassword != confirmPassword {
			data["Error"] = "New passwords do not match"
			h.renderWithRequest(w, r, "change_password.html", data)
			return
		}

		if err := h.service.ChangePassword(customer.ID, currentPassword, newPassword); err != nil {
			data["Error"] = err.Error()
			h.renderWithRequest(w, r, "change_password.html", data)
			return
		}

		// Create notification
		h.service.CreateNotification(customer.ID, "security", "Password Changed",
			"Your password was successfully changed.", "/security")

		http.Redirect(w, r, "/settings?success=password", http.StatusSeeOther)
		return
	}

	h.renderWithRequest(w, r, "change_password.html", data)
}

// ============================================================================
// Email Preferences Handler
// ============================================================================

// HandleEmailPreferences handles email notification preferences
func (h *WebHandler) HandleEmailPreferences(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	prefs, _ := h.service.GetEmailPreferences(customer.ID)

	data := map[string]interface{}{
		"Customer":    customer,
		"CSRFToken":   deriveCSRFToken(session.SessionToken),
		"Preferences": prefs,
	}

	if r.Method == http.MethodPost {
		prefs.NotifyDeposits = r.FormValue("notify_deposits") == "on"
		prefs.NotifyOrders = r.FormValue("notify_orders") == "on"
		prefs.NotifyWithdrawals = r.FormValue("notify_withdrawals") == "on"
		prefs.NotifySecurity = r.FormValue("notify_security") == "on"
		prefs.NotifyMarketing = r.FormValue("notify_marketing") == "on"

		if err := h.service.UpdateEmailPreferences(customer.ID, prefs); err != nil {
			data["Error"] = "Failed to update preferences"
			h.renderWithRequest(w, r, "email_preferences.html", data)
			return
		}

		data["Success"] = "Email preferences updated"
		data["Preferences"] = prefs
	}

	h.renderWithRequest(w, r, "email_preferences.html", data)
}

// HandleEmailChange handles email address change
func (h *WebHandler) HandleEmailChange(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"LoggedIn":      true,
		"Customer":      customer,
		"CSRFToken":     deriveCSRFToken(session.SessionToken),
		"Title":         "Change Email",
		"PendingChange": h.service.GetPendingEmailChange(customer.ID),
	}

	if r.Method == http.MethodPost {
		action := r.FormValue("action")

		if action == "cancel" {
			h.service.CancelEmailChange(customer.ID)
			data["Success"] = "Email change request cancelled"
			data["PendingChange"] = nil
		} else {
			newEmail := strings.TrimSpace(r.FormValue("new_email"))

			if err := h.service.RequestEmailChange(customer.ID, newEmail); err != nil {
				data["Error"] = err.Error()
			} else {
				data["Success"] = "Verification emails sent to both addresses. Please check your inbox."
				data["PendingChange"] = h.service.GetPendingEmailChange(customer.ID)
			}
		}
	}

	h.renderWithRequest(w, r, "email_change.html", data)
}

// HandleEmailChangeVerify handles email change verification
func (h *WebHandler) HandleEmailChangeVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	confirmType := r.URL.Query().Get("type")

	data := map[string]interface{}{
		"Title": "Email Verification",
	}

	if token == "" || confirmType == "" {
		data["Error"] = "Invalid verification link"
		h.renderWithRequest(w, r, "email_change_verify.html", data)
		return
	}

	completed, err := h.service.ConfirmEmailChange(token, confirmType)
	if err != nil {
		data["Error"] = err.Error()
	} else if completed {
		data["Success"] = "Email address has been changed successfully!"
		data["Completed"] = true
	} else {
		if confirmType == "old" {
			data["Success"] = "Current email verified. Please also verify the new email address."
		} else {
			data["Success"] = "New email verified. Please also verify from your current email."
		}
		data["Partial"] = true
	}

	h.renderWithRequest(w, r, "email_change_verify.html", data)
}

// ============================================================================
// API Key Handler
// ============================================================================

// HandleAPIKey handles API key display and regeneration
func (h *WebHandler) HandleAPIKey(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"Customer":     customer,
		"CSRFToken":    deriveCSRFToken(session.SessionToken),
		"APIKeyMasked": h.service.GetAPIKeyMasked(customer.ID),
	}

	if r.Method == http.MethodPost && r.FormValue("action") == "regenerate" {
		newKey, err := h.service.RegenerateAPIKey(customer.ID)
		if err != nil {
			data["Error"] = "Failed to regenerate API key"
			h.renderWithRequest(w, r, "api_key.html", data)
			return
		}

		// Show the full key once after regeneration
		data["NewAPIKey"] = newKey
		data["Success"] = "API key regenerated successfully. Copy it now - it won't be shown again."

		h.service.CreateNotification(customer.ID, "security", "API Key Regenerated",
			"Your API key was regenerated. The old key no longer works.", "/settings/api-key")
	}

	h.renderWithRequest(w, r, "api_key.html", data)
}

// ============================================================================
// Withdrawal Whitelist Handler
// ============================================================================

// HandleWhitelist handles withdrawal address whitelist
func (h *WebHandler) HandleWhitelist(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	addresses, _ := h.service.GetWhitelistedAddresses(customer.ID)

	data := map[string]interface{}{
		"Customer":  customer,
		"CSRFToken": deriveCSRFToken(session.SessionToken),
		"Addresses": addresses,
	}

	if r.Method == http.MethodPost {
		action := r.FormValue("action")

		if action == "add" {
			address := strings.TrimSpace(r.FormValue("btc_address"))
			label := strings.TrimSpace(r.FormValue("label"))

			if err := h.service.AddWhitelistedAddress(customer.ID, address, label); err != nil {
				data["Error"] = err.Error()
			} else {
				data["Success"] = "Address added. It will be usable for withdrawals in 24 hours."
				addresses, _ = h.service.GetWhitelistedAddresses(customer.ID)
				data["Addresses"] = addresses
			}
		} else if action == "remove" {
			addressID, _ := strconv.Atoi(r.FormValue("address_id"))
			if err := h.service.RemoveWhitelistedAddress(customer.ID, addressID); err != nil {
				data["Error"] = err.Error()
			} else {
				data["Success"] = "Address removed from whitelist"
				addresses, _ = h.service.GetWhitelistedAddresses(customer.ID)
				data["Addresses"] = addresses
			}
		}
	}

	h.renderWithRequest(w, r, "whitelist.html", data)
}

// HandleBalanceAlerts handles balance alert settings
func (h *WebHandler) HandleBalanceAlerts(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	alert := h.service.GetBalanceAlert(customer.ID)
	balance, _ := h.service.GetBalance(customer.ID)

	data := map[string]interface{}{
		"LoggedIn":     true,
		"Customer":     customer,
		"CSRFToken":    deriveCSRFToken(session.SessionToken),
		"Title":        "Balance Alerts",
		"Alert":        alert,
		"ThresholdBTC": fmt.Sprintf("%.8f", float64(alert.ThresholdSat)/100000000),
		"BalanceBTC":   balance.BalanceBTC,
	}

	if r.Method == http.MethodPost {
		thresholdStr := r.FormValue("threshold")
		threshold, err := strconv.ParseFloat(thresholdStr, 64)
		if err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 {
			data["Error"] = "Invalid threshold amount"
			h.renderWithRequest(w, r, "balance_alerts.html", data)
			return
		}

		thresholdSat := int64(threshold * 100000000)
		notifyEmail := r.FormValue("notify_email") == "on"
		notifyInApp := r.FormValue("notify_inapp") == "on"
		enabled := r.FormValue("enabled") == "on"

		if err := h.service.UpdateBalanceAlert(customer.ID, thresholdSat, notifyEmail, notifyInApp, enabled); err != nil {
			data["Error"] = "Failed to update settings"
		} else {
			data["Success"] = "Balance alert settings updated"
			alert = h.service.GetBalanceAlert(customer.ID)
			data["Alert"] = alert
			data["ThresholdBTC"] = fmt.Sprintf("%.8f", float64(alert.ThresholdSat)/100000000)
		}
	}

	h.renderWithRequest(w, r, "balance_alerts.html", data)
}

// ============================================================================
// Support Tickets Handler
// ============================================================================

// HandleTickets shows the support ticket list
func (h *WebHandler) HandleTickets(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	tickets, _ := h.service.GetCustomerTickets(customer.ID)

	data := map[string]interface{}{
		"LoggedIn":  true,
		"Customer":  customer,
		"CSRFToken": deriveCSRFToken(session.SessionToken),
		"Title":     "Support Tickets",
		"Tickets":   tickets,
	}

	h.renderWithRequest(w, r, "tickets.html", data)
}

// HandleNewTicket handles creating a new support ticket
func (h *WebHandler) HandleNewTicket(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"LoggedIn":  true,
		"Customer":  customer,
		"CSRFToken": deriveCSRFToken(session.SessionToken),
		"Title":     "New Support Ticket",
	}

	if r.Method == http.MethodPost {
		subject := strings.TrimSpace(r.FormValue("subject"))
		category := r.FormValue("category")
		message := strings.TrimSpace(r.FormValue("message"))

		if subject == "" || message == "" {
			data["Error"] = "Subject and message are required"
			h.renderWithRequest(w, r, "ticket_new.html", data)
			return
		}

		if len(subject) > 200 {
			data["Error"] = "Subject must be under 200 characters"
			h.renderWithRequest(w, r, "ticket_new.html", data)
			return
		}

		if category == "" {
			category = "general"
		}

		ticket, err := h.service.CreateTicket(customer.ID, subject, category, message)
		if err != nil {
			data["Error"] = "Failed to create ticket. Please try again."
			h.renderWithRequest(w, r, "ticket_new.html", data)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/support/ticket/%d", ticket.ID), http.StatusSeeOther)
		return
	}

	h.renderWithRequest(w, r, "ticket_new.html", data)
}

// HandleTicketView shows a single ticket with its messages
func (h *WebHandler) HandleTicketView(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Extract ticket ID from URL: /support/ticket/123
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Redirect(w, r, "/support", http.StatusSeeOther)
		return
	}

	ticketID, err := strconv.Atoi(parts[3])
	if err != nil {
		http.Redirect(w, r, "/support", http.StatusSeeOther)
		return
	}

	ticket, err := h.service.GetTicket(customer.ID, ticketID)
	if err != nil {
		http.Redirect(w, r, "/support", http.StatusSeeOther)
		return
	}

	messages, _ := h.service.GetTicketMessages(ticketID)

	data := map[string]interface{}{
		"LoggedIn":  true,
		"Customer":  customer,
		"CSRFToken": deriveCSRFToken(session.SessionToken),
		"Title":     fmt.Sprintf("Ticket #%d", ticket.ID),
		"Ticket":    ticket,
		"Messages":  messages,
	}

	// Handle new message POST
	if r.Method == http.MethodPost {
		action := r.FormValue("action")

		if action == "close" {
			if err := h.service.CloseTicket(ticketID, customer.ID); err == nil {
				http.Redirect(w, r, fmt.Sprintf("/support/ticket/%d", ticketID), http.StatusSeeOther)
				return
			}
		} else {
			message := strings.TrimSpace(r.FormValue("message"))
			if message != "" {
				if err := h.service.AddTicketMessage(ticketID, customer.ID, message, "customer"); err == nil {
					http.Redirect(w, r, fmt.Sprintf("/support/ticket/%d", ticketID), http.StatusSeeOther)
					return
				}
			}
		}

		// Refresh data
		ticket, _ = h.service.GetTicket(customer.ID, ticketID)
		messages, _ = h.service.GetTicketMessages(ticketID)
		data["Ticket"] = ticket
		data["Messages"] = messages
	}

	h.renderWithRequest(w, r, "ticket_view.html", data)
}

// ============================================================================
// Language Handler
// ============================================================================

// HandleLanguage shows language selection page
func (h *WebHandler) HandleLanguage(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	currentLang := h.getLang(r)

	// Only show languages that have translation files loaded
	availableLanguages := make(map[string]string)
	for _, lang := range h.translator.Languages() {
		if name, ok := i18n.SupportedLanguages[lang]; ok {
			availableLanguages[lang] = name
		}
	}

	data := map[string]interface{}{
		"Title":       "Language Settings",
		"CurrentLang": currentLang,
		"Languages":   availableLanguages,
	}

	if session != nil && customer != nil {
		data["LoggedIn"] = true
		data["Customer"] = customer
		data["CSRFToken"] = deriveCSRFToken(session.SessionToken)
	}

	h.renderWithRequest(w, r, "language.html", data)
}

// HandleSetLanguage sets the user's preferred language via cookie
func (h *WebHandler) HandleSetLanguage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/language", http.StatusSeeOther)
		return
	}

	lang := r.FormValue("lang")
	if lang == "" || !h.translator.HasLanguage(lang) {
		lang = "en"
	}

	// Set language cookie (1 year expiry)
	http.SetCookie(w, &http.Cookie{
		Name:     langCookieName,
		Value:    lang,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	// Redirect back to referrer or dashboard
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/dashboard"
	}
	http.Redirect(w, r, referer, http.StatusSeeOther)
}

// ============================================================================
// Notifications Handler
// ============================================================================

// HandleNotifications shows notifications page
func (h *WebHandler) HandleNotifications(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	notifications, _ := h.service.GetNotifications(customer.ID, 100)

	data := map[string]interface{}{
		"Customer":      customer,
		"CSRFToken":     deriveCSRFToken(session.SessionToken),
		"Notifications": notifications,
		"UnreadCount":   h.service.GetUnreadNotificationCount(customer.ID),
	}

	h.renderWithRequest(w, r, "notifications.html", data)
}

// HandleMarkNotificationRead marks a notification as read
func (h *WebHandler) HandleMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	notificationID, _ := strconv.Atoi(r.FormValue("notification_id"))
	h.service.MarkNotificationRead(customer.ID, notificationID)

	// Return to notifications or respond with JSON
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	} else {
		http.Redirect(w, r, "/notifications", http.StatusSeeOther)
	}
}

// HandleMarkAllNotificationsRead marks all notifications as read
func (h *WebHandler) HandleMarkAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	h.service.MarkAllNotificationsRead(customer.ID)
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// ============================================================================
// Admin Handlers
// ============================================================================

// HandleAdmin shows admin dashboard
func (h *WebHandler) HandleAdmin(w http.ResponseWriter, r *http.Request) {
	stats := h.service.GetAdminDashboardStats()
	pendingWithdrawals, _ := h.service.GetPendingWithdrawalsForAdmin()
	recentOrders, _ := h.service.GetRecentOrdersForAdmin(10)
	recentCustomers, _ := h.service.GetRecentCustomersForAdmin(10)

	// Get Braiins balance
	braiinsBalance := "N/A"
	braiinsActiveBids := 0
	if bal, err := h.service.GetBraiinsBalance(); err == nil {
		braiinsBalance = fmt.Sprintf("%.8f", float64(bal.AvailableSat)/100000000)
	}

	adminSession, _ := r.Cookie("admin_session")
	csrfToken := ""
	if adminSession != nil {
		csrfToken = deriveCSRFToken(adminSession.Value)
	}

	data := map[string]interface{}{
		"Stats":              stats,
		"PendingWithdrawals": pendingWithdrawals,
		"RecentOrders":       recentOrders,
		"RecentCustomers":    recentCustomers,
		"BraiinsBalance":     braiinsBalance,
		"BraiinsActiveBids":  braiinsActiveBids,
		"CSRFToken":          csrfToken,
	}

	h.renderWithRequest(w, r, "admin.html", data)
}

// HandleAdminWithdrawal handles withdrawal approval/rejection
func (h *WebHandler) HandleAdminWithdrawal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	adminUsername := r.Header.Get("X-Admin-Username")

	// Parse URL: /admin/withdrawal/{id}/approve or /admin/withdrawal/{id}/reject
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.Redirect(w, r, "/admin?error=invalid_request", http.StatusSeeOther)
		return
	}

	withdrawalID, err := strconv.Atoi(parts[2])
	if err != nil {
		http.Redirect(w, r, "/admin?error=invalid_id", http.StatusSeeOther)
		return
	}

	action := parts[3]
	switch action {
	case "approve":
		if err := h.service.AdminApproveWithdrawal(adminUsername, withdrawalID); err != nil {
			http.Redirect(w, r, "/admin?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin?success=withdrawal_approved", http.StatusSeeOther)

	case "reject":
		reason := r.FormValue("reason")
		if reason == "" {
			reason = "Rejected by admin"
		}
		if err := h.service.AdminRejectWithdrawal(adminUsername, withdrawalID, reason); err != nil {
			http.Redirect(w, r, "/admin?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin?success=withdrawal_rejected", http.StatusSeeOther)

	case "complete":
		txid := r.FormValue("txid")
		if err := h.service.AdminCompleteWithdrawal(adminUsername, withdrawalID, txid); err != nil {
			http.Redirect(w, r, "/admin?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin?success=withdrawal_completed", http.StatusSeeOther)

	default:
		http.Redirect(w, r, "/admin?error=invalid_action", http.StatusSeeOther)
	}
}

// HandleAdminCustomers shows customer list
func (h *WebHandler) HandleAdminCustomers(w http.ResponseWriter, r *http.Request) {
	customers, _ := h.service.GetRecentCustomersForAdmin(100)

	data := map[string]interface{}{
		"Customers": customers,
	}

	h.renderWithRequest(w, r, "admin_customers.html", data)
}

// HandleAdminOrders shows order list
func (h *WebHandler) HandleAdminOrders(w http.ResponseWriter, r *http.Request) {
	orders, _ := h.service.GetRecentOrdersForAdmin(100)

	data := map[string]interface{}{
		"Orders": orders,
	}

	h.renderWithRequest(w, r, "admin_orders.html", data)
}

// HandleAdminBraiinsPayments shows and manages Braiins payment tracking
func (h *WebHandler) HandleAdminBraiinsPayments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodPost {
		action := r.FormValue("action")
		switch action {
		case "sync":
			// Trigger manual sync
			if err := h.service.SyncBraiinsPayments(); err != nil {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": err.Error(),
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "sync completed",
			})
			return

		case "mark_paid":
			paymentID, _ := strconv.Atoi(r.FormValue("payment_id"))
			txid := r.FormValue("txid")
			if paymentID == 0 || txid == "" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": "payment_id and txid required",
				})
				return
			}
			if err := h.service.MarkBraiinsPaymentPaid(paymentID, txid); err != nil {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": err.Error(),
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "payment marked as paid",
			})
			return
		}
	}

	// GET - return summary and unpaid orders
	summary, err := h.service.GetBraiinsPaymentsSummary()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	unpaid, err := h.service.GetUnpaidBraiinsOrders()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"summary": summary,
		"unpaid":  unpaid,
	})
}

// HandleAdminLogin handles admin login
func (h *WebHandler) HandleAdminLogin(w http.ResponseWriter, r *http.Request) {
	// Check if already logged in
	if cookie, err := r.Cookie("admin_session"); err == nil {
		var adminID int
		h.service.db.QueryRow(`
			SELECT admin_id FROM rental_admin_sessions
			WHERE session_token = $1 AND expires_at > NOW()
		`, HashToken(cookie.Value)).Scan(&adminID)
		if adminID > 0 {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
	}

	data := map[string]interface{}{
		"CSRFToken":        generateCSRFToken(),
		"TurnstileSiteKey": h.turnstileSiteKey,
	}

	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")

		// Rate limit admin login attempts
		clientIP := getClientIPWeb(r)
		rateLimitKey := fmt.Sprintf("admin_login:%s", clientIP)
		if !h.service.CheckRateLimit(rateLimitKey, "admin_login", 5, 300) {
			data["Error"] = "Too many login attempts. Try again in 5 minutes."
			h.renderWithRequest(w, r, "admin_login.html", data)
			return
		}

		// Verify Turnstile CAPTCHA
		turnstileToken := r.FormValue("cf-turnstile-response")
		if valid, err := VerifyTurnstile(h.turnstileSecret, turnstileToken, clientIP); !valid {
			if err != nil {
				log.Printf("Turnstile verification error: %v", err)
			}
			data["Error"] = "CAPTCHA verification failed. Please try again."
			h.renderWithRequest(w, r, "admin_login.html", data)
			return
		}

		// Verify credentials
		var admin Admin
		err := h.service.db.QueryRow(`
			SELECT id, username, password_hash, role
			FROM rental_admins
			WHERE username = $1
		`, username).Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.Role)

		if err != nil || !VerifyPassword(password, admin.PasswordHash) {
			data["Error"] = "Invalid username or password"
			h.renderWithRequest(w, r, "admin_login.html", data)
			return
		}

		// Create admin session
		sessionToken, _ := GenerateToken()
		expiresAt := time.Now().Add(8 * time.Hour)

		_, err = h.service.db.Exec(`
			INSERT INTO rental_admin_sessions (admin_id, session_token, expires_at, ip_address)
			VALUES ($1, $2, $3, $4)
		`, admin.ID, HashToken(sessionToken), expiresAt, clientIP)
		if err != nil {
			data["Error"] = "Failed to create session"
			h.renderWithRequest(w, r, "admin_login.html", data)
			return
		}

		// Update last login
		h.service.db.Exec(`UPDATE rental_admins SET last_login = NOW() WHERE id = $1`, admin.ID)

		// Log the login
		h.service.logAuditAction(admin.Username, "admin_login", "admin", admin.ID, clientIP)

		// Set cookie
		http.SetCookie(w, &http.Cookie{
			Name:     "admin_session",
			Value:    sessionToken,
			Path:     "/admin",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   8 * 60 * 60, // 8 hours
		})

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	h.renderWithRequest(w, r, "admin_login.html", data)
}

// Handle404 renders the 404 error page
func (h *WebHandler) Handle404(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	data := map[string]interface{}{}

	// Check if logged in
	if session, customer := h.getSession(r); session != nil && customer != nil {
		data["LoggedIn"] = true
		data["Customer"] = customer
	}

	h.renderWithRequest(w, r, "404.html", data)
}

// Handle500 renders the 500 error page
func (h *WebHandler) Handle500(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusInternalServerError)
	h.renderWithRequest(w, r, "500.html", map[string]interface{}{})
}

// HandleAdminLogout handles admin logout
func (h *WebHandler) HandleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("admin_session"); err == nil {
		// Delete session from database
		h.service.db.Exec(`DELETE FROM rental_admin_sessions WHERE session_token = $1`, HashToken(cookie.Value))
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// HandleExportOrdersCSV exports orders as CSV
func (h *WebHandler) HandleExportOrdersCSV(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	orders, err := h.service.GetOrders(customer.ID)
	if err != nil {
		http.Error(w, "Failed to fetch orders", http.StatusInternalServerError)
		return
	}

	// Set CSV headers
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=hashforge-orders-%s.csv", time.Now().Format("2006-01-02")))

	// Write CSV header
	fmt.Fprintf(w, "Order ID,Status,Mining Mode,Hashrate (PH/s),Budget (BTC),Spent (BTC),Created,Started,Completed\n")

	// Write order rows
	for _, o := range orders {
		startedAt := ""
		if o.StartedAt != nil {
			startedAt = o.StartedAt.Format("2006-01-02 15:04:05")
		}
		completedAt := ""
		if o.CompletedAt != nil {
			completedAt = o.CompletedAt.Format("2006-01-02 15:04:05")
		}

		fmt.Fprintf(w, "%d,%s,%s,%.0f,%.8f,%.8f,%s,%s,%s\n",
			o.ID,
			o.Status,
			o.MiningMode,
			o.SpeedLimitPH,
			float64(o.BudgetSat)/100000000,
			float64(o.AmountSpentSat)/100000000,
			o.CreatedAt.Format("2006-01-02 15:04:05"),
			startedAt,
			completedAt,
		)
	}
}

// HandleExportTransactionsCSV exports transactions as CSV
func (h *WebHandler) HandleExportTransactionsCSV(w http.ResponseWriter, r *http.Request) {
	session, customer := h.getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Get all transactions (up to 10000 for export)
	transactions, _, err := h.service.GetTransactionHistory(customer.ID, 10000, 0)
	if err != nil {
		http.Error(w, "Failed to fetch transactions", http.StatusInternalServerError)
		return
	}

	// Set CSV headers
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=hashforge-transactions-%s.csv", time.Now().Format("2006-01-02")))

	// Write CSV header
	fmt.Fprintf(w, "ID,Type,Amount (BTC),BTC TxID,Order ID,Memo,Date\n")

	// Write transaction rows
	for _, t := range transactions {
		orderID := ""
		if t.OrderID != nil {
			orderID = fmt.Sprintf("%d", *t.OrderID)
		}

		// Escape memo field (may contain commas)
		memo := strings.ReplaceAll(t.Memo, "\"", "\"\"")
		if strings.ContainsAny(memo, ",\"\n") {
			memo = "\"" + memo + "\""
		}

		fmt.Fprintf(w, "%d,%s,%.8f,%s,%s,%s,%s\n",
			t.ID,
			t.TxType,
			float64(t.AmountSat)/100000000,
			t.BTCTxID,
			orderID,
			memo,
			t.CreatedAt.Format("2006-01-02 15:04:05"),
		)
	}
}

// RegisterWebRoutes registers all web UI routes
func (h *WebHandler) RegisterRoutes(mux *http.ServeMux) {
	// Static files
	fs := http.FileServer(http.Dir(h.staticDir))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	// Public pages
	mux.HandleFunc("/", h.HandleHome)
	mux.HandleFunc("/register", h.HandleRegister)
	mux.HandleFunc("/login", h.HandleLogin)
	mux.HandleFunc("/logout", h.HandleLogout)
	mux.HandleFunc("/verify-email", h.HandleVerifyEmail)
	mux.HandleFunc("/resend-verification", h.HandleResendVerification)
	mux.HandleFunc("/forgot-password", h.HandleForgotPassword)
	mux.HandleFunc("/reset-password", h.HandleResetPassword)

	// Language selection
	mux.HandleFunc("/language", h.HandleLanguage)
	mux.HandleFunc("/set-language", h.HandleSetLanguage)

	// Protected pages
	mux.HandleFunc("/dashboard", h.requireAuth(h.HandleDashboard))
	mux.HandleFunc("/deposit", h.requireAuth(h.HandleDeposit))
	// State-changing routes wrapped with CSRF protection
	mux.HandleFunc("/order/new", h.requireAuth(h.csrfProtect(h.HandleNewOrder)))
	mux.HandleFunc("/order/", h.requireAuth(h.csrfProtect(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") && r.Method == http.MethodPost {
			h.HandleCancelOrder(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/extend") {
			h.HandleExtendOrder(w, r)
			return
		}
		// Show order detail page
		h.HandleOrderDetail(w, r)
	})))
	mux.HandleFunc("/withdraw", h.requireAuth(h.csrfProtect(h.HandleWithdraw)))
	mux.HandleFunc("/settings", h.requireAuth(h.csrfProtect(h.HandleSettings)))
	mux.HandleFunc("/settings/export", h.requireAuth(h.HandleExportData))
	mux.HandleFunc("/settings/delete-account", h.requireAuth(h.csrfProtect(h.HandleDeleteAccount)))
	mux.HandleFunc("/security/logout-all", h.requireAuth(h.csrfProtect(h.HandleLogoutAllSessions)))
	mux.HandleFunc("/faq", h.HandleFAQ)
	mux.HandleFunc("/terms", h.HandleTerms)
	mux.HandleFunc("/privacy", h.HandlePrivacy)
	mux.HandleFunc("/risk", h.HandleRisk)
	mux.HandleFunc("/refunds", h.HandleRefunds)
	mux.HandleFunc("/contact", h.HandleContact)
	mux.HandleFunc("/how-it-works", h.HandleHowItWorks)
	mux.HandleFunc("/status", h.HandleStatus)
	mux.HandleFunc("/orders", h.requireAuth(h.HandleOrders))
	mux.HandleFunc("/orders/analytics", h.requireAuth(h.HandleOrderAnalytics))
	mux.HandleFunc("/orders/export", h.requireAuth(h.HandleExportOrdersCSV))
	mux.HandleFunc("/transactions", h.requireAuth(h.HandleTransactions))
	mux.HandleFunc("/transactions/export", h.requireAuth(h.HandleExportTransactionsCSV))
	mux.HandleFunc("/blocks", h.requireAuth(h.HandleBlocks))
	mux.HandleFunc("/mining/", h.requireAuth(h.HandleMining))
	mux.HandleFunc("/solo", h.requireAuth(h.HandleSolo))
	mux.HandleFunc("/pplns", h.requireAuth(h.HandlePPLNS))
	mux.HandleFunc("/api/order/", h.requireAuth(h.HandleOrderAPI))

	// Pool API proxy for mining dashboard (bypasses CORS)
	mux.HandleFunc("/pool-api/", h.HandlePoolProxy)

	// Support tickets
	mux.HandleFunc("/support", h.requireAuth(h.HandleTickets))
	mux.HandleFunc("/support/new", h.requireAuth(h.csrfProtect(h.HandleNewTicket)))
	mux.HandleFunc("/support/ticket/", h.requireAuth(h.csrfProtect(h.HandleTicketView)))

	// 2FA pages - protected with CSRF
	mux.HandleFunc("/2fa/setup", h.requireAuth(h.csrfProtect(h.Handle2FASetup)))
	mux.HandleFunc("/2fa/verify", h.requireAuth(h.csrfProtect(h.Handle2FAVerify)))
	mux.HandleFunc("/2fa/backup-codes", h.requireAuth(h.csrfProtect(h.Handle2FABackupCodes)))

	// Security pages
	mux.HandleFunc("/security", h.requireAuth(h.HandleSecurity))
	mux.HandleFunc("/security/sessions", h.requireAuth(h.csrfProtect(h.HandleSessions)))
	mux.HandleFunc("/security/change-password", h.requireAuth(h.csrfProtect(h.HandleChangePassword)))

	// Email preferences
	mux.HandleFunc("/settings/email", h.requireAuth(h.csrfProtect(h.HandleEmailPreferences)))
	mux.HandleFunc("/settings/email/change", h.requireAuth(h.csrfProtect(h.HandleEmailChange)))
	mux.HandleFunc("/settings/email/verify", h.HandleEmailChangeVerify)

	// API key management
	mux.HandleFunc("/settings/api-key", h.requireAuth(h.csrfProtect(h.HandleAPIKey)))

	// Withdrawal whitelist
	mux.HandleFunc("/settings/whitelist", h.requireAuth(h.csrfProtect(h.HandleWhitelist)))
	mux.HandleFunc("/settings/balance-alerts", h.requireAuth(h.csrfProtect(h.HandleBalanceAlerts)))

	// Notifications
	mux.HandleFunc("/notifications", h.requireAuth(h.HandleNotifications))
	mux.HandleFunc("/notifications/read", h.requireAuth(h.csrfProtect(h.HandleMarkNotificationRead)))
	mux.HandleFunc("/notifications/read-all", h.requireAuth(h.csrfProtect(h.HandleMarkAllNotificationsRead)))

	// WebSocket for real-time updates
	mux.HandleFunc("/ws", h.HandleWebSocket)

	// Admin routes
	mux.HandleFunc("/admin/login", h.HandleAdminLogin)
	mux.HandleFunc("/admin/logout", h.HandleAdminLogout)
	// Protected admin routes
	mux.HandleFunc("/admin", h.requireAdmin(h.HandleAdmin))
	// Withdrawal operations require admin or superadmin role
	mux.HandleFunc("/admin/withdrawal/", h.requireAdminRole("admin", "superadmin")(h.csrfProtect(h.HandleAdminWithdrawal)))
	mux.HandleFunc("/admin/customers", h.requireAdmin(h.HandleAdminCustomers))
	mux.HandleFunc("/admin/orders", h.requireAdmin(h.HandleAdminOrders))
	mux.HandleFunc("/admin/braiins-payments", h.requireAdminRole("superadmin")(h.HandleAdminBraiinsPayments))
}

// NotFoundHandler returns a handler that catches all unmatched routes
func (h *WebHandler) NotFoundHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.Handle404(w, r)
	}
}

// HandlePoolProxy proxies requests to pool.bch2.org to bypass CORS
func (h *WebHandler) HandlePoolProxy(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests for safety
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Build the target URL - preserve the path after /pool-api/
	targetPath := strings.TrimPrefix(r.URL.Path, "/pool-api")
	targetURL := "https://pool.bch2.org" + targetPath
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Create the proxy request
	proxyReq, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		log.Printf("Pool proxy error creating request: %v", err)
		http.Error(w, "Proxy error", http.StatusInternalServerError)
		return
	}

	// Forward relevant headers
	proxyReq.Header.Set("Accept", "application/json")
	proxyReq.Header.Set("User-Agent", "HashForge-Proxy/1.0")

	// Make the request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("Pool proxy error: %v", err)
		http.Error(w, "Proxy error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Set CORS headers for the proxied response
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Write status code and body
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
