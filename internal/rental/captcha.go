package rental

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// TurnstileResponse represents the response from Cloudflare Turnstile verification
type TurnstileResponse struct {
	Success     bool     `json:"success"`
	ChallengeTs string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
	ErrorCodes  []string `json:"error-codes"`
	Action      string   `json:"action"`
	Cdata       string   `json:"cdata"`
}

// VerifyTurnstile verifies a Cloudflare Turnstile token
func VerifyTurnstile(secret, token, remoteIP string) (bool, error) {
	if secret == "" {
		// Check if we're in production - fail if Turnstile not configured
		if os.Getenv("PRODUCTION") == "true" || os.Getenv("ENV") == "production" {
			return false, fmt.Errorf("CAPTCHA verification required but not configured")
		}
		// Turnstile not configured, skip verification in development
		return true, nil
	}

	if token == "" {
		return false, fmt.Errorf("missing captcha token")
	}

	data := url.Values{}
	data.Set("secret", secret)
	data.Set("response", token)
	if remoteIP != "" {
		data.Set("remoteip", remoteIP)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(
		"https://challenges.cloudflare.com/turnstile/v0/siteverify",
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return false, fmt.Errorf("failed to verify captcha: %w", err)
	}
	defer resp.Body.Close()

	var result TurnstileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("failed to decode captcha response: %w", err)
	}

	if !result.Success {
		if len(result.ErrorCodes) > 0 {
			return false, fmt.Errorf("captcha verification failed: %s", strings.Join(result.ErrorCodes, ", "))
		}
		return false, fmt.Errorf("captcha verification failed")
	}

	return true, nil
}
