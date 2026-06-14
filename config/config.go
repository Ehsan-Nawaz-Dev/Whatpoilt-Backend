package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port             string
	Environment      string
	ShopifyAPISecret string
	BackendAPIKey    string // shared secret between frontend and this server
	AdminAPIKey      string // secret for the admin panel
	FrontendURL      string
	AdminPanelURL    string
	DBPath           string
}

var App *Config

func Load() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}
	App = &Config{
		Port:             getEnv("PORT", "8080"),
		Environment:      getEnv("ENVIRONMENT", "development"),
		ShopifyAPISecret: getEnv("SHOPIFY_API_SECRET", ""),
		BackendAPIKey:    getEnv("BACKEND_API_KEY", ""),
		AdminAPIKey:      getEnv("ADMIN_API_KEY", ""),
		FrontendURL:      getEnv("FRONTEND_URL", "http://localhost:3000"),
		AdminPanelURL:    getEnv("ADMIN_PANEL_URL", "http://localhost:5173"),
		DBPath:           getEnv("DB_PATH", "./data/whatsapp.db"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
