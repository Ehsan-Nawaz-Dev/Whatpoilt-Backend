package handlers

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/config"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
)

type SettingsHandler struct{ db *store.DB }

func NewSettingsHandler(db *store.DB) *SettingsHandler { return &SettingsHandler{db: db} }

// GET /api/settings
func (h *SettingsHandler) Get(c *gin.Context) {
	s, err := h.db.GetSettings(middleware.ShopFrom(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s)
}

// GET /api/settings/auth-status
//
// Per-merchant Shopify connection status for the multi-tenant SaaS frontend.
// The "Reconnect Shopify" banner polls this and only shows for the shop whose
// token Shopify has rejected. reauthUrl points at the embedded OAuth flow.
func (h *SettingsHandler) AuthStatus(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	st := h.db.GetReauthStatus(shop)

	var detectedAt interface{}
	if st.DetectedAt != nil {
		detectedAt = st.DetectedAt
	}

	c.JSON(http.StatusOK, gin.H{
		"authenticated": !st.NeedsReauth,
		"needsReauth":   st.NeedsReauth,
		"reason":        st.Reason,
		"detectedAt":    detectedAt,
		"reauthUrl":     fmt.Sprintf("%s/auth?shop=%s", config.App.FrontendURL, shop),
	})
}

// PUT /api/settings
func (h *SettingsHandler) Save(c *gin.Context) {
	var s models.Settings
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if s.TypingSpeedCPM < 60  { s.TypingSpeedCPM = 60  }
	if s.TypingSpeedCPM > 600 { s.TypingSpeedCPM = 600 }
	if s.MinTypingSeconds < 1 { s.MinTypingSeconds = 1  }
	if s.MaxTypingSeconds < s.MinTypingSeconds { s.MaxTypingSeconds = s.MinTypingSeconds + 1 }
	if s.ReadDelayMinSeconds < 0 { s.ReadDelayMinSeconds = 0 }
	if s.ReadDelayMaxSeconds < s.ReadDelayMinSeconds { s.ReadDelayMaxSeconds = s.ReadDelayMinSeconds }

	if err := h.db.SaveSettings(middleware.ShopFrom(c), s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s)
}
