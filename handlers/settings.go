package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
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
