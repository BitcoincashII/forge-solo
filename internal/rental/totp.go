package rental

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// TOTP parameters
	totpDigits   = 6
	totpPeriod   = 30 // seconds
	totpSkew     = 1  // Allow 1 period before/after for clock drift

	// Backup codes
	backupCodeCount  = 10             // Give users 10 backup codes
	backupCodeLength = 24             // 24 hex chars = 12 bytes = 96 bits entropy
)

// encryptionKey holds the AES-256 key for encrypting sensitive data
var (
	encryptionKey     []byte
	encryptionKeyOnce sync.Once
	encryptionEnabled bool
)

// initEncryptionKey initializes the encryption key from environment
func initEncryptionKey() {
	keyHex := os.Getenv("TOTP_ENCRYPTION_KEY")
	if keyHex == "" {
		// Check if we're in production - fail if encryption not configured
		if os.Getenv("PRODUCTION") == "true" || os.Getenv("ENV") == "production" {
			log.Fatal("FATAL: TOTP_ENCRYPTION_KEY must be configured in production")
		}
		// In development, generate ephemeral key (2FA will not survive restart)
		log.Println("SECURITY WARNING: TOTP_ENCRYPTION_KEY not set - generating ephemeral key for development")
		log.Println("SECURITY WARNING: 2FA secrets will NOT survive restart!")
		log.Println("SECURITY WARNING: Generate a persistent key with: openssl rand -hex 32")
		encryptionKey = make([]byte, 32)
		if _, err := rand.Read(encryptionKey); err != nil {
			log.Fatalf("FATAL: Failed to generate ephemeral encryption key: %v", err)
		}
		encryptionEnabled = true
		return
	}

	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		// Invalid key format - must be 64 hex chars (32 bytes for AES-256)
		log.Fatalf("FATAL: Invalid TOTP_ENCRYPTION_KEY format (expected 64 hex chars, got %d)", len(keyHex))
	}

	encryptionKey = key
	encryptionEnabled = true
	log.Println("TOTP secret encryption enabled")
}

// getEncryptionKey returns the encryption key, initializing if needed
func getEncryptionKey() ([]byte, bool) {
	encryptionKeyOnce.Do(initEncryptionKey)
	return encryptionKey, encryptionEnabled
}

// EncryptTOTPSecret encrypts a TOTP secret using AES-256-GCM
// Returns base64-encoded ciphertext with "enc:" prefix
// SECURITY: Encryption is ALWAYS required - fails if key not configured
func EncryptTOTPSecret(secret string) (string, error) {
	key, enabled := getEncryptionKey()
	if !enabled {
		return "", fmt.Errorf("TOTP encryption key not configured - cannot store 2FA secrets securely")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(secret), nil)
	return "enc:" + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptTOTPSecret decrypts a TOTP secret
// SECURITY: Legacy unencrypted secrets are rejected in production
func DecryptTOTPSecret(encrypted string) (string, error) {
	if !strings.HasPrefix(encrypted, "enc:") {
		// Legacy unencrypted secret - only allow in non-production for migration
		if os.Getenv("PRODUCTION") == "true" || os.Getenv("ENV") == "production" {
			return "", fmt.Errorf("unencrypted TOTP secrets not allowed in production - migrate existing secrets")
		}
		log.Println("SECURITY WARNING: Decrypting legacy unencrypted TOTP secret - should be migrated")
		return encrypted, nil
	}

	key, enabled := getEncryptionKey()
	if !enabled {
		return "", fmt.Errorf("encryption key not configured but encrypted secret found")
	}

	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encrypted, "enc:"))
	if err != nil {
		return "", fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	if len(ciphertext) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

// GenerateTOTPSecret generates a new random TOTP secret
func GenerateTOTPSecret() (string, error) {
	// Generate 20 bytes of randomness (160 bits, standard for TOTP)
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("failed to generate secret: %w", err)
	}

	// Encode as base32 (without padding)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	return encoded, nil
}

// GenerateTOTPURI generates an otpauth:// URI for QR codes
func GenerateTOTPURI(secret, email, issuer string) string {
	// Format: otpauth://totp/ISSUER:ACCOUNT?secret=SECRET&issuer=ISSUER
	account := email
	if account == "" {
		account = "customer"
	}
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&digits=%d&period=%d",
		issuer, account, secret, issuer, totpDigits, totpPeriod)
}

// ValidateTOTPCode validates a TOTP code against a secret
func ValidateTOTPCode(secret, code string) bool {
	if len(code) != totpDigits {
		return false
	}

	// Decode the secret
	secretBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(
		strings.ToUpper(secret))
	if err != nil {
		return false
	}

	// Get current time period
	now := time.Now().Unix()
	counter := now / totpPeriod

	// Check current period and skew windows
	// Use constant-time comparison to prevent timing attacks
	for i := -totpSkew; i <= totpSkew; i++ {
		expected := generateTOTP(secretBytes, counter+int64(i))
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return true
		}
	}

	return false
}

// generateTOTP generates a TOTP code for a given counter
func generateTOTP(secret []byte, counter int64) string {
	// Convert counter to big-endian bytes
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))

	// HMAC-SHA1
	mac := hmac.New(sha1.New, secret)
	mac.Write(buf)
	hash := mac.Sum(nil)

	// Dynamic truncation
	offset := hash[len(hash)-1] & 0x0f
	code := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff

	// Get digits
	code = code % 1000000 // 6 digits

	return fmt.Sprintf("%06d", code)
}

// GenerateBackupCodes generates a set of one-time backup codes
func GenerateBackupCodes(count int) ([]string, error) {
	codes := make([]string, count)
	for i := 0; i < count; i++ {
		code, err := generateBackupCode()
		if err != nil {
			return nil, err
		}
		codes[i] = code
	}
	return codes, nil
}

// generateBackupCode generates a single backup code
func generateBackupCode() (string, error) {
	bytes := make([]byte, backupCodeLength/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	// Format as XXXX-XXXX-XXXX-XXXX-XXXX-XXXX for readability (24 chars = 96 bits)
	hexStr := hex.EncodeToString(bytes)
	return strings.ToUpper(
		hexStr[0:4] + "-" + hexStr[4:8] + "-" + hexStr[8:12] + "-" +
			hexStr[12:16] + "-" + hexStr[16:20] + "-" + hexStr[20:24]), nil
}

// HashBackupCode hashes a backup code for storage
func HashBackupCode(code string) string {
	// Normalize: remove dashes, uppercase
	normalized := strings.ToUpper(strings.ReplaceAll(code, "-", ""))
	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:])
}

// VerifyBackupCode checks if a code matches a hash
// Uses constant-time comparison to prevent timing attacks
func VerifyBackupCode(code, hash string) bool {
	computedHash := HashBackupCode(code)
	return subtle.ConstantTimeCompare([]byte(computedHash), []byte(hash)) == 1
}

// GenerateSessionToken generates a secure session token
func GenerateSessionToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
