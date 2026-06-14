package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/config"
	"github.com/whatpilot/backend/handlers"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
	"github.com/whatpilot/backend/worker"
)

func main() {
	config.Load()
	startupGuard()

	// ── Directories ───────────────────────────────────────────────────────────
	for _, d := range []string{"data", "data/sessions"} {
		if err := os.MkdirAll(d, 0755); err != nil {
			log.Fatalf("create dir %s: %v", d, err)
		}
	}

	// ── Database ──────────────────────────────────────────────────────────────
	db, err := store.New(config.App.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// ── WhatsApp Registry (one manager per merchant) ──────────────────────────
	registry, err := whatsapp.NewRegistry("data/sessions")
	if err != nil {
		log.Fatalf("init registry: %v", err)
	}
	// Wire opt-out handler so incoming "STOP" messages mark contacts in our DB.
	// The registry injects this into each manager as shops connect.
	registry.SetOptOutHandler(func(shop, phone string) {
		db.SetContactOptOut(shop, phone, true)
		slog.Info("contact opted out", "shop", shop, "phone", phone)
	})
	// Restore existing WhatsApp sessions from disk.
	registry.ConnectAll()

	// ── Background worker (persistent job queue) ───────────────────────────────
	wrk := worker.New(db, registry)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go wrk.Run(ctx)

	// ── Gin router ────────────────────────────────────────────────────────────
	if config.App.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.Logger())
	r.Use(middleware.CORS())

	// Rate limiter: 60 burst, 1 req/s sustained per shop for the send endpoint.
	sendRL := middleware.NewRateLimiter(60, 1)

	// ── Handler instances ─────────────────────────────────────────────────────
	waH   := handlers.NewWhatsAppHandler(registry, db)
	shopH := handlers.NewShopifyHandler(registry, db)
	autoH := handlers.NewAutomationHandler(db)
	tmplH := handlers.NewTemplateHandler(db)
	ctctH := handlers.NewContactHandler(db)
	stgsH := handlers.NewSettingsHandler(db)
	gdprH := handlers.NewGDPRHandler(db, registry)
	anlH  := handlers.NewAnalyticsHandler(db)
	admH  := handlers.NewAdminHandler(db, registry, config.App.DBPath)
	sessH := handlers.NewSessionHandler(db)

	// ── Authenticated API routes (/api/*) ─────────────────────────────────────
	// Every request must carry Authorization: Bearer <BACKEND_API_KEY>
	// and X-Shop-Domain: <shop>.myshopify.com
	api := r.Group("/api")
	api.Use(middleware.Shop(config.App.BackendAPIKey))
	{
		wa := api.Group("/whatsapp")
		wa.GET("/status",     waH.Status)
		wa.GET("/qr",         waH.StreamQR)
		wa.POST("/disconnect", waH.Disconnect)
		wa.POST("/logout",    waH.Logout)
		wa.POST("/send",      sendRL.Limit(), waH.SendMessage) // rate-limited
		wa.GET("/logs",       waH.ListLogs)

		auto := api.Group("/automations")
		auto.GET("",                  autoH.List)
		auto.POST("",                 autoH.Create)
		auto.PUT("/:id",              autoH.Update)
		auto.DELETE("/:id",           autoH.Delete)
		auto.PATCH("/:id/toggle",     autoH.Toggle)
		auto.PATCH("/:id/template",   autoH.UpdateTemplate)

		tmpl := api.Group("/templates")
		tmpl.GET("",              tmplH.List)
		tmpl.POST("",             tmplH.Create)
		tmpl.POST("/seed",        tmplH.Seed)
		tmpl.GET("/seeded",       tmplH.IsSeeded)
		tmpl.PUT("/:id",          tmplH.Update)
		tmpl.PATCH("/:id/toggle", tmplH.Toggle)
		tmpl.DELETE("/:id",       tmplH.Delete)

		ctct := api.Group("/contacts")
		ctct.GET("",        ctctH.List)
		ctct.POST("",       ctctH.Create)
		ctct.DELETE("/:id", ctctH.Delete)

		stgs := api.Group("/settings")
		stgs.GET("", stgsH.Get)
		stgs.PUT("", stgsH.Save)

		anlyt := api.Group("/analytics")
		anlyt.GET("", anlH.Overview)
	}

	// ── Shopify webhook routes (HMAC verified, no API key header) ─────────────
	hooks := r.Group("/webhooks")
	{
		hooks.POST("/orders/created",   shopH.OrderCreated)
		hooks.POST("/orders/fulfilled", shopH.OrderFulfilled)
		hooks.POST("/orders/cancelled", shopH.OrderCancelled)
		hooks.POST("/checkouts/create", shopH.AbandonedCart)
	}

	// ── Internal GDPR routes (called by frontend with API key) ────────────────
	// These are NOT under /api so the Shop middleware doesn't apply —
	// the GDPR handler reads shop from the request body (Shopify webhook payload).
	internal := r.Group("/internal")
	internal.Use(func(c *gin.Context) {
		if config.App.BackendAPIKey != "" &&
			c.GetHeader("Authorization") != "Bearer "+config.App.BackendAPIKey {
			c.AbortWithStatusJSON(401, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	})
	{
		// Called by the Remix frontend after OAuth to store the access token
		// so the backend can call the Shopify Admin API (e.g. order tagging).
		// ── Shopify session storage (replaces Prisma/PostgreSQL) ─────────────
		internal.GET("/sessions/:id",           sessH.Load)
		internal.POST("/sessions",              sessH.Store)
		internal.DELETE("/sessions/:id",        sessH.Delete)
		internal.POST("/sessions/delete-multi", sessH.DeleteMulti)
		internal.GET("/sessions/by-shop/:shop", sessH.ByShop)

		internal.POST("/register-shop", func(c *gin.Context) {
			var req struct {
				ShopDomain  string `json:"shop_domain"  binding:"required"`
				AccessToken string `json:"access_token" binding:"required"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			if err := db.SetShopToken(req.ShopDomain, req.AccessToken); err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			c.JSON(200, gin.H{"ok": true})
		})

		internal.POST("/gdpr/customer-redact", gdprH.CustomerRedact)
		internal.POST("/gdpr/shop-redact",     gdprH.ShopRedact)
		internal.GET("/gdpr/customer-data",    gdprH.CustomerData)
	}

	// ── Admin login (public — no auth required) ───────────────────────────────
	r.POST("/admin/login", admH.Login)

	// ── Admin routes (protected by ADMIN_API_KEY) ─────────────────────────────
	adm := r.Group("/admin")
	adm.Use(handlers.AdminAuth(db, config.App.AdminAPIKey))
	{
		adm.GET("/stats",                    admH.GlobalStats)
		adm.GET("/server-status",            admH.ServerStatus)
		adm.GET("/profile",                  admH.GetProfile)
		adm.PUT("/profile",                  admH.UpdateProfile)
		adm.POST("/change-password",         admH.ChangePassword)
		adm.GET("/order-tags",               admH.GetOrderTags)
		adm.PUT("/order-tags",               admH.UpdateOrderTags)
		adm.GET("/shops",                    admH.ListShops)
		adm.DELETE("/shops/:shop",           admH.PurgeShop)
		adm.GET("/plans",                    admH.ListPlans)
		adm.POST("/plans/seed",              admH.SeedPlans)
		adm.PUT("/plans/:key",               admH.UpdatePlan)
		adm.GET("/announcements",            admH.ListAnnouncements)
		adm.POST("/announcements",           admH.CreateAnnouncement)
		adm.PUT("/announcements/:id",        admH.UpdateAnnouncement)
		adm.DELETE("/announcements/:id",     admH.DeleteAnnouncement)
	}

	// ── Public routes (no auth — read-only, consumed by merchant frontend) ────
	pub := r.Group("/api/public")
	{
		pub.GET("/plans",         admH.PublicPlans)
		pub.GET("/announcements", admH.PublicAnnouncements)
	}

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	addr := ":" + config.App.Port
	slog.Info("backend starting", "addr", addr, "env", config.App.Environment)
	if err := r.Run(addr); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// startupGuard panics on misconfiguration that would silently break production.
func startupGuard() {
	if config.App.Environment != "production" {
		return
	}
	fatal := func(msg string) { log.Fatalf("startup guard: %s", msg) }

	if config.App.ShopifyAPISecret == "" {
		fatal("SHOPIFY_API_SECRET must be set in production — webhook HMAC verification is disabled without it")
	}
	if config.App.BackendAPIKey == "" {
		fatal("BACKEND_API_KEY must be set in production — the API is open to the internet without it")
	}
}
