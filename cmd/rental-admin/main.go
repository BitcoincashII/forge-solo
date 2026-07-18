package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	db := connectDB()
	defer db.Close()

	switch os.Args[1] {
	case "create-admin":
		if len(os.Args) < 4 {
			fmt.Println("Usage: rental-admin create-admin <username> <password> [role]")
			return
		}
		username := os.Args[2]
		password := os.Args[3]
		role := "admin"
		if len(os.Args) > 4 {
			role = os.Args[4]
		}
		createAdmin(db, username, password, role)

	case "list-admins":
		listAdmins(db)

	case "reset-password":
		if len(os.Args) < 4 {
			fmt.Println("Usage: rental-admin reset-password <username> <new-password>")
			return
		}
		resetPassword(db, os.Args[2], os.Args[3])

	case "delete-admin":
		if len(os.Args) < 3 {
			fmt.Println("Usage: rental-admin delete-admin <username>")
			return
		}
		deleteAdmin(db, os.Args[2])

	case "regenerate-api-key":
		if len(os.Args) < 3 {
			fmt.Println("Usage: rental-admin regenerate-api-key <username>")
			return
		}
		regenerateAPIKey(db, os.Args[2])

	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("Forge Pool Rental Admin Tool")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  rental-admin create-admin <username> <password> [role]")
	fmt.Println("  rental-admin list-admins")
	fmt.Println("  rental-admin reset-password <username> <new-password>")
	fmt.Println("  rental-admin delete-admin <username>")
	fmt.Println("  rental-admin regenerate-api-key <username>")
	fmt.Println("")
	fmt.Println("Roles: admin, superadmin")
	fmt.Println("")
	fmt.Println("Note: API keys are stored as hashes and cannot be retrieved.")
	fmt.Println("      Use regenerate-api-key if you lose access.")
}

func connectDB() *sql.DB {
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "forge")
	dbPass := getEnv("DB_PASSWORD", "forgepool")
	dbName := getEnv("DB_NAME", "forgepool")
	dbSSLMode := getEnv("DB_SSLMODE", "require")
	dbSSLRootCert := getEnv("DB_SSLROOTCERT", "")

	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		dbHost, dbPort, dbUser, dbPass, dbName, dbSSLMode,
	)

	// Add SSL root cert if specified
	if dbSSLRootCert != "" {
		connStr += fmt.Sprintf(" sslrootcert=%s", dbSSLRootCert)
	}

	if dbSSLMode == "disable" {
		log.Println("WARNING: Database SSL is disabled - not recommended for production")
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	return db
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func generateAPIKey() string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		log.Fatalf("failed to generate API key: %v", err)
	}
	return hex.EncodeToString(bytes)
}

// hashAPIKey hashes an API key using SHA256 for secure storage
func hashAPIKey(apiKey string) string {
	hash := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(hash[:])
}

// bcryptCost matches the cost used in password.go for consistency
const bcryptCost = 12

func createAdmin(db *sql.DB, username, password, role string) {
	// Hash password with same cost as customer passwords
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	// Generate API key and hash it for storage
	apiKey := generateAPIKey()
	apiKeyHash := hashAPIKey(apiKey)

	_, err = db.Exec(`
		INSERT INTO rental_admins (username, password_hash, api_key, role)
		VALUES ($1, $2, $3, $4)
	`, username, string(hash), apiKeyHash, role)
	if err != nil {
		log.Fatalf("Failed to create admin: %v", err)
	}

	fmt.Printf("Admin created successfully!\n")
	fmt.Printf("  Username: %s\n", username)
	fmt.Printf("  Role:     %s\n", role)
	fmt.Printf("  API Key:  %s\n", apiKey)
	fmt.Println("")
	fmt.Println("IMPORTANT: Save this API key NOW - it cannot be retrieved later!")
	fmt.Println("           The key is stored as a hash for security.")
}

func listAdmins(db *sql.DB) {
	rows, err := db.Query(`
		SELECT id, username, role, created_at, last_login
		FROM rental_admins
		ORDER BY id
	`)
	if err != nil {
		log.Fatalf("Failed to list admins: %v", err)
	}
	defer rows.Close()

	fmt.Printf("%-4s %-20s %-12s %-20s %-20s\n", "ID", "Username", "Role", "Created", "Last Login")
	fmt.Println("------------------------------------------------------------------------------------")

	for rows.Next() {
		var id int
		var username, role string
		var createdAt, lastLogin sql.NullTime
		rows.Scan(&id, &username, &role, &createdAt, &lastLogin)

		lastLoginStr := "never"
		if lastLogin.Valid {
			lastLoginStr = lastLogin.Time.Format("2006-01-02 15:04:05")
		}
		createdStr := ""
		if createdAt.Valid {
			createdStr = createdAt.Time.Format("2006-01-02 15:04:05")
		}

		fmt.Printf("%-4d %-20s %-12s %-20s %-20s\n", id, username, role, createdStr, lastLoginStr)
	}
}

func resetPassword(db *sql.DB, username, newPassword string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		log.Fatalf("Failed to hash password: %v", err)
	}

	result, err := db.Exec(`
		UPDATE rental_admins SET password_hash = $1 WHERE username = $2
	`, string(hash), username)
	if err != nil {
		log.Fatalf("Failed to reset password: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		fmt.Printf("Admin '%s' not found\n", username)
		return
	}

	fmt.Printf("Password reset for '%s'\n", username)
}

func deleteAdmin(db *sql.DB, username string) {
	result, err := db.Exec(`DELETE FROM rental_admins WHERE username = $1`, username)
	if err != nil {
		log.Fatalf("Failed to delete admin: %v", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		fmt.Printf("Admin '%s' not found\n", username)
		return
	}

	fmt.Printf("Admin '%s' deleted\n", username)
}

func regenerateAPIKey(db *sql.DB, username string) {
	// Check if admin exists
	var adminID int
	err := db.QueryRow(`SELECT id FROM rental_admins WHERE username = $1`, username).Scan(&adminID)
	if err == sql.ErrNoRows {
		fmt.Printf("Admin '%s' not found\n", username)
		return
	}
	if err != nil {
		log.Fatalf("Failed to find admin: %v", err)
	}

	// Generate new API key and hash it
	newAPIKey := generateAPIKey()
	apiKeyHash := hashAPIKey(newAPIKey)

	_, err = db.Exec(`UPDATE rental_admins SET api_key = $1 WHERE id = $2`, apiKeyHash, adminID)
	if err != nil {
		log.Fatalf("Failed to update API key: %v", err)
	}

	fmt.Printf("API key regenerated for '%s'\n", username)
	fmt.Printf("  New API Key: %s\n", newAPIKey)
	fmt.Println("")
	fmt.Println("IMPORTANT: Save this API key NOW - it cannot be retrieved later!")
	fmt.Println("           The old API key is now invalid.")
}
