package rental

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/smtp"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EmailConfig holds SMTP configuration
type EmailConfig struct {
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	FromAddress  string
	FromName     string
	BaseURL      string // e.g., https://hashforge.bch2.org
}

// EmailSender handles sending emails
type EmailSender struct {
	config *EmailConfig
}

// NewEmailSender creates a new email sender
func NewEmailSender(config *EmailConfig) *EmailSender {
	return &EmailSender{config: config}
}

// emailRegex for basic email validation
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// ValidateEmail checks if an email address is valid
func ValidateEmail(email string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return fmt.Errorf("email is required")
	}
	if len(email) > 255 {
		return fmt.Errorf("email too long")
	}
	if !emailRegex.MatchString(email) {
		return fmt.Errorf("invalid email format")
	}
	return nil
}

// NormalizeEmail normalizes an email address
func NormalizeEmail(email string) string {
	return strings.TrimSpace(strings.ToLower(email))
}

// GenerateToken generates a random token for email verification
func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// HashToken hashes a token using SHA256 for secure storage
// Tokens should be hashed before storing in the database
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// SendVerificationEmail sends an email verification link
func (e *EmailSender) SendVerificationEmail(toEmail, token string) error {
	if e.config == nil {
		fmt.Printf("EMAIL: Verification link for %s (no config)\n", toEmail)
		return nil
	}
	if e.config.SMTPHost == "" {
		// Log but don't fail if email not configured (for development)
		// Don't log full token - security risk
		fmt.Printf("EMAIL: Verification email for %s (SMTP not configured, token=%s...)\n", toEmail, token[:8])
		return nil
	}

	subject := "Verify your HashForge account"
	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", e.config.BaseURL, token)

	body := fmt.Sprintf(`Welcome to HashForge!

Please verify your email address by clicking the link below:

%s

This link expires in 24 hours.

If you didn't create an account, you can ignore this email.

---
HashForge by Forge Pool
`, verifyURL)

	return e.sendEmail(toEmail, subject, body)
}

// SendPasswordResetEmail sends a password reset link
func (e *EmailSender) SendPasswordResetEmail(toEmail, token string) error {
	if e.config == nil {
		fmt.Printf("EMAIL: Password reset link for %s (no config)\n", toEmail)
		return nil
	}
	if e.config.SMTPHost == "" {
		// Don't log full token - security risk
		fmt.Printf("EMAIL: Password reset email for %s (SMTP not configured, token=%s...)\n", toEmail, token[:8])
		return nil
	}

	subject := "Reset your HashForge password"
	resetURL := fmt.Sprintf("%s/reset-password?token=%s", e.config.BaseURL, token)

	body := fmt.Sprintf(`You requested a password reset for your HashForge account.

Click the link below to set a new password:

%s

This link expires in 1 hour.

If you didn't request this, you can ignore this email.

---
HashForge by Forge Pool
`, resetURL)

	return e.sendEmail(toEmail, subject, body)
}

// SendWelcomeEmail sends a welcome email after verification
func (e *EmailSender) SendWelcomeEmail(toEmail string) error {
	if e.config == nil || e.config.SMTPHost == "" {
		fmt.Printf("EMAIL: Welcome email for %s (email not configured)\n", toEmail)
		return nil
	}

	subject := "Welcome to HashForge"
	body := `Your email has been verified. Welcome to HashForge!

You can now log in and start renting BCH2 hashpower.

Getting started:
1. Deposit BTC to your deposit address
2. Wait for 3 confirmations
3. Place an order for hashpower
4. Earn BCH2 mining rewards

---
HashForge by Forge Pool
`
	return e.sendEmail(toEmail, subject, body)
}

// SendDepositConfirmedEmail notifies user of confirmed deposit
func (e *EmailSender) SendDepositConfirmedEmail(toEmail string, amountBTC string, txid string) error {
	if e.config.SMTPHost == "" {
		return nil
	}

	subject := "Deposit Confirmed - HashForge"
	body := fmt.Sprintf(`Your deposit has been confirmed!

Amount: %s BTC
Transaction: %s

Your funds are now available in your HashForge account. You can start renting hashpower immediately.

Dashboard: %s/dashboard

---
HashForge by Forge Pool
`, amountBTC, txid, e.config.BaseURL)

	return e.sendEmail(toEmail, subject, body)
}

// SendOrderStartedEmail notifies user that their order has started mining
func (e *EmailSender) SendOrderStartedEmail(toEmail string, orderID int, hashratePH float64, budgetBTC string, miningMode string) error {
	if e.config.SMTPHost == "" {
		return nil
	}

	subject := fmt.Sprintf("Order #%d Started - HashForge", orderID)
	body := fmt.Sprintf(`Your hashrate rental order has started!

Order ID: #%d
Hashrate: %.0f PH/s
Budget: %s BTC
Mode: %s

Your rented hashpower is now mining BCH2 on Forge Pool. Mining rewards will be sent to your registered BCH2 address.

View Order: %s/mining/%d

Note: Orders cannot be cancelled within the first 60 minutes.

---
HashForge by Forge Pool
`, orderID, hashratePH, budgetBTC, miningMode, e.config.BaseURL, orderID)

	return e.sendEmail(toEmail, subject, body)
}

// SendOrderCompletedEmail notifies user that their order has completed
func (e *EmailSender) SendOrderCompletedEmail(toEmail string, orderID int, spentBTC string, refundBTC string) error {
	if e.config.SMTPHost == "" {
		return nil
	}

	subject := fmt.Sprintf("Order #%d Completed - HashForge", orderID)

	refundLine := ""
	if refundBTC != "0.00000000" && refundBTC != "" {
		refundLine = fmt.Sprintf("\nRefund: %s BTC (credited to your balance)", refundBTC)
	}

	body := fmt.Sprintf(`Your hashrate rental order has completed.

Order ID: #%d
Total Spent: %s BTC%s

Thank you for using HashForge! View your mining stats and place another order anytime.

Dashboard: %s/dashboard
View Order: %s/mining/%d

---
HashForge by Forge Pool
`, orderID, spentBTC, refundLine, e.config.BaseURL, e.config.BaseURL, orderID)

	return e.sendEmail(toEmail, subject, body)
}

// SendWithdrawalProcessedEmail notifies user that their withdrawal has been sent
func (e *EmailSender) SendWithdrawalProcessedEmail(toEmail string, amountBTC string, address string, txid string) error {
	if e.config.SMTPHost == "" {
		return nil
	}

	subject := "Withdrawal Sent - HashForge"
	body := fmt.Sprintf(`Your withdrawal has been processed!

Amount: %s BTC
Address: %s
Transaction: %s

The transaction has been broadcast to the Bitcoin network. It should confirm within 10-60 minutes depending on network conditions.

---
HashForge by Forge Pool
`, amountBTC, address, txid)

	return e.sendEmail(toEmail, subject, body)
}

// SendWithdrawalRejectedEmail notifies user that their withdrawal was rejected
func (e *EmailSender) SendWithdrawalRejectedEmail(toEmail string, amountBTC string, reason string) error {
	if e.config.SMTPHost == "" {
		return nil
	}

	subject := "Withdrawal Rejected - HashForge"
	body := fmt.Sprintf(`Your withdrawal request has been rejected.

Amount: %s BTC
Reason: %s

The funds have been returned to your account balance. If you believe this is an error, please contact support.

Dashboard: %s/dashboard

---
HashForge by Forge Pool
`, amountBTC, reason, e.config.BaseURL)

	return e.sendEmail(toEmail, subject, body)
}

// sendEmail sends an email via SMTP
func (e *EmailSender) sendEmail(to, subject, body string) error {
	if e.config.SMTPHost == "" {
		return nil
	}

	from := e.config.FromAddress
	if e.config.FromName != "" {
		from = fmt.Sprintf("%s <%s>", e.config.FromName, e.config.FromAddress)
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		from, to, subject, body)

	addr := net.JoinHostPort(e.config.SMTPHost, strconv.Itoa(e.config.SMTPPort))

	// Connect to SMTP server with timeout to prevent goroutine leaks
	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP: %w", err)
	}
	// Set deadline for the entire SMTP conversation
	conn.SetDeadline(time.Now().Add(60 * time.Second))

	client, err := smtp.NewClient(conn, e.config.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create SMTP client: %w", err)
	}
	defer client.Close()

	// STARTTLS for external SMTP servers (port 587)
	if e.config.SMTPPort == 587 {
		tlsConfig := &tls.Config{ServerName: e.config.SMTPHost}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}

	// Auth if configured
	if e.config.SMTPUser != "" {
		auth := smtp.PlainAuth("", e.config.SMTPUser, e.config.SMTPPassword, e.config.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}

	// Set sender and recipient
	if err := client.Mail(e.config.FromAddress); err != nil {
		return fmt.Errorf("SMTP MAIL failed: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT failed: %w", err)
	}

	// Send message body
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %w", err)
	}
	_, err = w.Write([]byte(msg))
	if err != nil {
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	err = w.Close()
	if err != nil {
		return fmt.Errorf("SMTP close failed: %w", err)
	}

	return client.Quit()
}

// SendNewIPLoginEmail notifies user of login from new IP
func (e *EmailSender) SendNewIPLoginEmail(toEmail string, ipAddress string) error {
	if e.config.SMTPHost == "" {
		return nil
	}

	subject := "Security Alert: New Login Location - HashForge"
	body := fmt.Sprintf(`We detected a login to your HashForge account from a new location.

IP Address: %s
Time: %s UTC

If this was you, you can ignore this email.

If this wasn't you:
1. Change your password immediately: %s/security/change-password
2. Review your active sessions: %s/security/sessions
3. Enable 2FA if you haven't: %s/2fa/setup

---
HashForge by Forge Pool
`, ipAddress, time.Now().UTC().Format("Jan 2, 2006 15:04"), e.config.BaseURL, e.config.BaseURL, e.config.BaseURL)

	return e.sendEmail(toEmail, subject, body)
}

// SendBraiinsBalanceAlertEmail notifies admin of low Braiins balance
func (e *EmailSender) SendBraiinsBalanceAlertEmail(toEmail string, currentBTC string, thresholdBTC string) error {
	if e.config.SMTPHost == "" {
		return nil
	}

	subject := "ALERT: Low Braiins Balance - HashForge"
	body := fmt.Sprintf(`WARNING: Your Braiins hashpower account balance is low!

Current Balance: %s BTC
Alert Threshold: %s BTC

Please top up your Braiins account to ensure uninterrupted service for customers.

Action Required:
1. Log into Braiins: https://hashpower.braiins.com/
2. Deposit BTC to your account
3. Verify balance is above threshold

---
HashForge Admin Alert
`, currentBTC, thresholdBTC)

	return e.sendEmail(toEmail, subject, body)
}

// Token expiration duration
const EmailTokenExpiry = 24 * time.Hour
