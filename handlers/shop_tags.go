package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/store"
)

// ShopTagsHandler handles per-shop order tag customisation from the Shopify embedded app.
type ShopTagsHandler struct {
	db *store.DB
}

func NewShopTagsHandler(db *store.DB) *ShopTagsHandler {
	return &ShopTagsHandler{db: db}
}

// GET /api/order-tags
// Returns the effective tag for each taggable trigger for this shop.
// Per-shop value wins over global admin value; both fall back to the built-in default.
func (h *ShopTagsHandler) Get(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	tags := make(map[string]string, len(taggableTriggers))
	for _, trigger := range taggableTriggers {
		key := "order_tag_" + trigger
		// 1. Per-shop override set by the merchant
		v := h.db.GetShopOrderTag(shop, key)
		// 2. Global admin override
		if v == "" {
			v = h.db.GetAdminConfigValue(key)
		}
		// 3. Built-in default
		if v == "" {
			v = globalTagDefaults[trigger]
		}
		tags[trigger] = v
	}
	c.JSON(200, tags)
}

// PUT /api/order-tags
// Stores per-shop tag overrides. Send "" to clear an override and revert to default.
func (h *ShopTagsHandler) Update(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	allowed := make(map[string]bool, len(taggableTriggers))
	for _, t := range taggableTriggers {
		allowed[t] = true
	}
	for trigger, tag := range req {
		if allowed[trigger] {
			h.db.SetShopOrderTag(shop, "order_tag_"+trigger, tag)
		}
	}
	c.JSON(200, req)
}
