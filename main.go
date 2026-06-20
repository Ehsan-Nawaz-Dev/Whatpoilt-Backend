package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/config"
	"github.com/whatpilot/backend/handlers"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
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
	// Keyword auto-reply: look up the customer's text against keyword_replies table.
	registry.SetKeywordReplyHandler(func(shop, phone, text string) bool {
		reply := db.GetKeywordReply(shop, text)
		if reply == "" {
			return false
		}
		mgr, err := registry.For(shop)
		if err != nil {
			return false
		}
		cfg, _ := db.GetSettings(shop)
		if sendErr := mgr.SendMessageWithTyping(phone, reply, cfg); sendErr != nil {
			slog.Warn("keyword reply send failed", "shop", shop, "phone", phone, "err", sendErr)
			return false
		}
		slog.Info("keyword auto-reply sent", "shop", shop, "phone", phone, "keyword", text)
		return true
	})
	dispatchPendingReply := func(shop, phone string, pc *store.PendingConfirmation) {
		mgr, err := registry.For(shop)
		if err != nil {
			return
		}
		cfg, _ := db.GetSettings(shop)
		switch pc.ReplyType {
		case "poll":
			mgr.SendPollMessage(phone, pc.ReplyMessage, pc.ReplyOptions)
		case "buttons":
			mgr.SendButtonMessage(phone, pc.ReplyMessage, pc.ReplyOptions)
		default:
			mgr.SendMessageWithTyping(phone, pc.ReplyMessage, cfg)
		}
	}

	// Plain-text message from customer: only fires pending confirmations that
	// have no trigger_option guard. Passing nil hashes skips guarded entries.
	registry.SetConfirmationHandler(func(shop, phone string) {
		pc := db.PopPendingConfirmation(shop, phone, nil)
		if pc == nil {
			return
		}
		slog.Info("text confirmation — sending pending reply", "shop", shop, "phone", phone)
		if pc.ReplyMessage != "" {
			dispatchPendingReply(shop, phone, pc)
		}
	})
	// Poll vote from customer: decrypted hashes are matched against trigger options —
	// dispatches the correct yes, no, or help reply, and sets up nested Step 2 flows.
	registry.SetPollVoteHandler(func(shop, phone string, votedHashes [][]byte) {
		pc := db.PopPendingConfirmation(shop, phone, votedHashes)
		if pc == nil {
			return
		}
		slog.Info("poll vote event processed", "shop", shop, "phone", phone, "voted_branch", pc.VotedBranch)

		var msgToSend string
		var typeToSend string
		var optsToSend []string

		switch pc.VotedBranch {
		case "yes":
			msgToSend = pc.ReplyMessage
			typeToSend = pc.ReplyType
			optsToSend = pc.ReplyOptions
		case "no":
			msgToSend = pc.ReplyNoMessage
			typeToSend = pc.ReplyNoType
			optsToSend = pc.ReplyNoOptions

			// If we matched the negative option and have Step 2 messages configured,
			// store the Step 2 pending confirmation.
			if pc.Step2YesMessage != "" {
				err := db.StorePendingConfirmationExtended(
					shop, phone,
					pc.Step2YesMessage, "text", []string{}, "✅ Yes, cancel my order",
					pc.Step2NoMessage, "text", []string{}, "❌ No, keep my order",
					pc.Step2HelpMessage, "text", []string{}, "📞 I need to speak to someone",
					"", "", "",
				)
				if err != nil {
					slog.Error("failed to store step 2 pending confirmation", "err", err)
				}
			}
		case "help":
			msgToSend = pc.ReplyHelpMessage
			typeToSend = pc.ReplyHelpType
			optsToSend = pc.ReplyHelpOptions
		}

		if msgToSend != "" {
			dispatchPendingReply(shop, phone, &store.PendingConfirmation{
				ReplyMessage: msgToSend,
				ReplyType:    typeToSend,
				ReplyOptions: optsToSend,
			})
		}
	})
	// Log incoming WhatsApp messages from customers
	registry.SetIncomingMessageHandler(func(shop, phone, content string) {
		slog.Info("incoming message received", "shop", shop, "phone", phone, "len", len(content))
		_, err := db.CreateIncomingMessageLog(shop, phone, content)
		if err != nil {
			slog.Error("failed to log incoming message", "err", err)
		}
	})
	// Restore existing WhatsApp sessions from disk.
	registry.ConnectAll()

	// ── Background worker (persistent job queue) ───────────────────────────────
	wrk := worker.New(db, registry)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go wrk.Run(ctx)

	// ── Win-back background job (runs once per day) ────────────────────────────
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				settings := db.AllShops()
				for _, shop := range settings {
					cfg, err := db.GetSettings(shop)
					if err != nil || cfg.WinBackInactiveDays <= 0 {
						continue
					}
					candidates := db.WinBackCandidates(shop, cfg.WinBackInactiveDays)
					if len(candidates) == 0 {
						continue
					}
					autos, _ := db.GetAutomationsByTrigger(shop, models.TriggerWinBack)
					if len(autos) == 0 {
						continue
					}
					shopH := handlers.NewShopifyHandler(registry, db)
					for _, c := range candidates {
						shopH.EnqueueWinBack(shop, autos, c)
					}
					slog.Info("win-back enqueued", "shop", shop, "candidates", len(candidates))
				}
			}
		}
	}()

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
	kwdH  := handlers.NewKeywordHandler(db)
	brdH  := handlers.NewBroadcastHandler(db, registry)
	inboxH := handlers.NewInboxHandler(registry, db)
	supportH := handlers.NewSupportHandler(db)
	shopTagsH := handlers.NewShopTagsHandler(db)
	retagH    := handlers.NewRetagHandler(db)

	// Seed default FAQs on startup
	db.SeedDefaultFAQs()

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
		anlyt.GET("/optouts", anlH.OptOutTrends)
		anlyt.GET("/revenue", anlH.RevenueAttribution)

		kwd := api.Group("/keywords")
		kwd.GET("",        kwdH.List)
		kwd.POST("",       kwdH.Create)
		kwd.PUT("/:id",    kwdH.Update)
		kwd.DELETE("/:id", kwdH.Delete)

		brd := api.Group("/broadcasts")
		brd.POST("", brdH.Send)

		inbox := api.Group("/inbox")
		inbox.GET("/chats",       inboxH.ListActiveChats)
		inbox.GET("/chats/:phone", inboxH.GetChatHistory)

		api.GET("/order-tags",    shopTagsH.Get)
		api.PUT("/order-tags",    shopTagsH.Update)
		api.POST("/retag-orders", retagH.Retag)

		api.POST("/support", supportH.SubmitTicket)
	}

	// ── Shopify webhook routes (HMAC verified, no API key header) ─────────────
	hooks := r.Group("/webhooks")
	{
		hooks.POST("/orders/created",   shopH.OrderCreated)
		hooks.POST("/orders/fulfilled", shopH.OrderFulfilled)
		hooks.POST("/orders/cancelled", shopH.OrderCancelled)
		hooks.POST("/checkouts/create", shopH.AbandonedCart)
		hooks.POST("/refunds/create",   shopH.RefundCreated)
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
				ShopDomain             string `json:"shop_domain"  binding:"required"`
				AccessToken            string `json:"access_token" binding:"required"`
				PlanName               string `json:"plan_name"`
				SubscriptionLineItemId string `json:"subscription_line_item_id"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			// Log token prefix so we can verify it's a fresh offline token
			prefix := req.AccessToken
			if len(prefix) > 10 {
				prefix = prefix[:10] + "…"
			}
			slog.Info("register-shop token stored", "shop", req.ShopDomain, "token_prefix", prefix, "subscription_line_item_id", req.SubscriptionLineItemId)
			if err := db.SetShopToken(req.ShopDomain, req.AccessToken); err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			if req.PlanName != "" {
				if err := db.SyncShopPlanWithLineItem(req.ShopDomain, req.PlanName, req.SubscriptionLineItemId); err != nil {
					slog.Error("failed to sync shop plan", "shop", req.ShopDomain, "plan", req.PlanName, "err", err)
				}
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
		adm.POST("/plans",                   admH.CreatePlan)
		adm.POST("/plans/seed",              admH.SeedPlans)
		adm.PUT("/plans/:key",               admH.UpdatePlan)
		adm.DELETE("/plans/:key",            admH.DeletePlan)
		adm.GET("/announcements",            admH.ListAnnouncements)
		adm.POST("/announcements",           admH.CreateAnnouncement)
		adm.PUT("/announcements/:id",        admH.UpdateAnnouncement)
		adm.DELETE("/announcements/:id",     admH.DeleteAnnouncement)

		adm.GET("/support",                  supportH.ListTickets)
		adm.PUT("/support/:id",              supportH.ReplyTicket)
		adm.DELETE("/support/:id",           supportH.DeleteTicket)
		adm.PUT("/support-info",             supportH.UpdateSupportInfo)
		adm.GET("/faqs",                     supportH.ListAdminFAQs)
		adm.POST("/faqs",                    supportH.CreateFAQ)
		adm.PUT("/faqs/:id",                 supportH.UpdateFAQ)
		adm.DELETE("/faqs/:id",              supportH.DeleteFAQ)
	}

	// ── Public routes (no auth — read-only, consumed by merchant frontend) ────
	pub := r.Group("/api/public")
	{
		pub.GET("/plans",         admH.PublicPlans)
		pub.GET("/announcements", admH.PublicAnnouncements)
		pub.GET("/support-info",  supportH.GetSupportInfo)
		pub.GET("/faqs",          supportH.ListPublicFAQs)
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
