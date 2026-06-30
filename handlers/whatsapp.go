package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
)

type WhatsAppHandler struct {
	registry *whatsapp.Registry
	db       *store.DB
}

func NewWhatsAppHandler(registry *whatsapp.Registry, db *store.DB) *WhatsAppHandler {
	return &WhatsAppHandler{registry: registry, db: db}
}

// GET /api/whatsapp/status
func (h *WhatsAppHandler) Status(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	mgr, err := h.registry.For(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Auto-seed default templates on first ever request from this shop.
	if !h.db.DefaultTemplatesSeeded(shop) {
		go h.db.SeedDefaultTemplates(shop)
	}
	stats, _ := h.db.GetStats(shop)
	stats.WAStatus = string(mgr.GetStatus())
	c.JSON(http.StatusOK, gin.H{"status": string(mgr.GetStatus()), "stats": stats})
}

// POST /api/whatsapp/connect — start (or restart) the QR pairing flow.
// The flow runs in the background; the frontend polls QRPoll for the result.
func (h *WhatsAppHandler) Connect(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	mgr, err := h.registry.For(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	mgr.StartPairing()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /api/whatsapp/qr — poll the current QR image + connection status.
// Replaces the old SSE stream, which broke when proxied through Vercel's
// serverless functions (timeouts cancelled the pairing mid-handshake).
func (h *WhatsAppHandler) QRPoll(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	mgr, err := h.registry.For(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	status, qr := mgr.GetPairingState()
	c.JSON(http.StatusOK, gin.H{"status": string(status), "qr": qr})
}

// POST /api/whatsapp/disconnect
func (h *WhatsAppHandler) Disconnect(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	mgr, err := h.registry.For(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	mgr.Disconnect()
	c.JSON(http.StatusOK, gin.H{"message": "disconnected"})
}

// POST /api/whatsapp/logout
func (h *WhatsAppHandler) Logout(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	mgr, err := h.registry.For(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := mgr.Logout(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}

// POST /api/whatsapp/send  (manual send with rate limiting applied upstream)
func (h *WhatsAppHandler) SendMessage(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	mgr, err := h.registry.For(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var req models.SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if h.db.IsOptedOut(shop, req.Phone) {
		c.JSON(http.StatusForbidden, gin.H{"error": "contact has opted out"})
		return
	}

	allowed, err := h.db.CanSendWhatsAppMessage(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check plan limits"})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": "Plan message limit reached"})
		return
	}

	logEntry, _ := h.db.CreateMessageLog(shop, "", req.Phone, "", req.Message)

	cfg, _ := h.db.GetSettings(shop)
	if err := mgr.SendMessageWithTyping(req.Phone, req.Message, cfg); err != nil {
		h.db.UpdateMessageLogStatus(logEntry.ID, models.MessageStatusFailed, err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}

	h.db.UpdateMessageLogStatus(logEntry.ID, models.MessageStatusSent, "")
	c.JSON(http.StatusOK, gin.H{"message": "sent", "log_id": logEntry.ID})
}

// GET /api/whatsapp/logs
func (h *WhatsAppHandler) ListLogs(c *gin.Context) {
	logs, err := h.db.ListMessageLogs(middleware.ShopFrom(c), 100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if logs == nil {
		logs = []models.MessageLog{}
	}
	c.JSON(http.StatusOK, logs)
}

// resolveTemplate replaces <<variable>> placeholders.
func resolveTemplate(tmpl string, vars map[string]string) string {
	result := tmpl
	for k, v := range vars {
		result = strings.ReplaceAll(result, "<<"+k+">>", v)
	}
	return result
}
