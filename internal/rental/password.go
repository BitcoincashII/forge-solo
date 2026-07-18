package rental

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptCost     = 12
	minPasswordLen = 12
)

// Common passwords to reject (top 100 most common)
var commonPasswords = map[string]bool{
	"password": true, "123456": true, "12345678": true, "qwerty": true,
	"abc123": true, "monkey": true, "1234567": true, "letmein": true,
	"trustno1": true, "dragon": true, "baseball": true, "iloveyou": true,
	"master": true, "sunshine": true, "ashley": true, "bailey": true,
	"passw0rd": true, "shadow": true, "123123": true, "654321": true,
	"superman": true, "qazwsx": true, "michael": true, "football": true,
	"password1": true, "password123": true, "welcome": true, "welcome1": true,
	"admin": true, "login": true, "bitcoin": true, "crypto": true,
	"blockchain": true, "wallet": true, "mining": true, "hashrate": true,
}

// HashPassword hashes a password using bcrypt
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(bytes), nil
}

// VerifyPassword checks if a password matches its hash
func VerifyPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// ValidatePassword checks password strength requirements
func ValidatePassword(password string) error {
	if len(password) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}

	// Check for common passwords
	if commonPasswords[strings.ToLower(password)] {
		return fmt.Errorf("password is too common, please choose a stronger one")
	}

	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, c := range password {
		switch {
		case unicode.IsUpper(c):
			hasUpper = true
		case unicode.IsLower(c):
			hasLower = true
		case unicode.IsDigit(c):
			hasDigit = true
		case unicode.IsPunct(c) || unicode.IsSymbol(c):
			hasSpecial = true
		}
	}

	if !hasUpper || !hasLower || !hasDigit || !hasSpecial {
		return fmt.Errorf("password must contain uppercase, lowercase, number, and special character (!@#$%%^&*)")
	}

	return nil
}
