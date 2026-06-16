package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/store"
)

type AnalyticsHandler struct{ db *store.DB }

func NewAnalyticsHandler(db *store.DB) *AnalyticsHandler { return &AnalyticsHandler{db: db} }

// GET /api/analytics?days=30
// days query param controls the time window (7, 30, 90). Defaults to 30.
func (h *AnalyticsHandler) Overview(c *gin.Context) {
	shop := middleware.ShopFrom(c)

	days := 30
	if d, err := strconv.Atoi(c.Query("days")); err == nil && d > 0 && d <= 90 {
		days = d
	}

	data, err := h.db.GetAnalytics(shop, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

// GET /api/analytics/optouts?days=30
func (h *AnalyticsHandler) OptOutTrends(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	days := 30
	if d, err := strconv.Atoi(c.Query("days")); err == nil && d > 0 && d <= 90 {
		days = d
	}
	c.JSON(http.StatusOK, h.db.OptOutTrends(shop, days))
}

// GET /api/analytics/revenue?hours=24
func (h *AnalyticsHandler) RevenueAttribution(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	hours := 24
	if h2, err := strconv.Atoi(c.Query("hours")); err == nil && h2 > 0 {
		hours = h2
	}
	data, err := h.db.GetAnalytics(shop, hours/24+1)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"wa_influenced_window_hours": hours, "analytics": data})
}
