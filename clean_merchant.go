package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: go run clean_merchant.go <shop-domain>")
	}
	shop := os.Args[1]

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/whatsapp.db"
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Failed to open db: %v", err)
	}
	defer db.Close()

	// 1. Delete from shop_tokens
	res1, err := db.Exec("DELETE FROM shop_tokens WHERE shop_domain = ?", shop)
	if err != nil {
		log.Fatalf("Failed to delete from shop_tokens: %v", err)
	}
	affected1, _ := res1.RowsAffected()

	// 2. Delete from shopify_sessions
	res2, err := db.Exec("DELETE FROM shopify_sessions WHERE shop = ?", shop)
	if err != nil {
		log.Fatalf("Failed to delete from shopify_sessions: %v", err)
	}
	affected2, _ := res2.RowsAffected()

	fmt.Printf("✅ Successfully cleaned merchant data for %s\n", shop)
	fmt.Printf("   - Deleted %d records from shop_tokens\n", affected1)
	fmt.Printf("   - Deleted %d records from shopify_sessions\n", affected2)
	fmt.Println("You can now do a completely fresh install of the app on this store to get a new token.")
}
