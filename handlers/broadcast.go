package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
)

type BroadcastHandler struct {
	db       *store.DB
	registry *whatsapp.Registry
}

func NewBroadcastHandler(db *store.DB, registry *whatsapp.Registry) *BroadcastHandler {
	return &BroadcastHandler{db: db, registry: registry}
}

// POST /api/broadcasts
// Enqueues a template message to every non-opted-out contact for the shop.
// Optional delay_minutes staggers each send to avoid rate limits.
func (h *BroadcastHandler) Send(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	var req models.BroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tmpl, err := h.db.GetTemplate(req.TemplateID, shop)
	if err != nil || !tmpl.IsActive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "template not found or inactive"})
		return
	}

	contacts, err := h.db.AllActiveContacts(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(contacts) == 0 {
		c.JSON(http.StatusOK, gin.H{"queued": 0, "message": "no active contacts"})
		return
	}

	stagger := time.Duration(req.DelayMinutes) * time.Minute
	now := time.Now()
	queued := 0
	for i, ct := range contacts {
		vars := map[string]string{"name": ct.Name, "phone": ct.Phone}
		msg := resolveTemplate(tmpl.Content, vars)
		runAt := now.Add(time.Duration(i) * stagger)
		if err := h.db.EnqueueJob(shop, "", tmpl.ID, ct.Phone, msg,
			tmpl.MessageType, tmpl.Options, runAt); err != nil {
			slog.Warn("broadcast: enqueue failed", "shop", shop, "phone", ct.Phone, "err", err)
			continue
		}
		queued++
	}

	slog.Info("broadcast queued", "shop", shop, "template", tmpl.Name, "queued", queued, "total", len(contacts))
	c.JSON(http.StatusOK, gin.H{
		"queued":  queued,
		"total":   len(contacts),
		"message": fmt.Sprintf("queued %d/%d messages", queued, len(contacts)),
	})
}
