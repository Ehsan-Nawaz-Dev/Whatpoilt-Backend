// Package handlers — GDPR compliance endpoints called by the frontend
// after validating the Shopify webhook.
package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
)

type GDPRHandler struct {
	db       *store.DB
	registry *whatsapp.Registry
}

func NewGDPRHandler(db *store.DB, registry *whatsapp.Registry) *GDPRHandler {
	return &GDPRHandler{db: db, registry: registry}
}

// POST /internal/gdpr/customer-redact
// Body: { "shop": "...", "phone": "..." }
func (h *GDPRHandler) CustomerRedact(c *gin.Context) {
	var req struct {
		Shop  string `json:"shop" binding:"required"`
		Phone string `json:"phone" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.PurgeCustomer(req.Shop, req.Phone); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "customer data purged"})
}

// POST /internal/gdpr/shop-redact
// Body: { "shop": "..." }
func (h *GDPRHandler) ShopRedact(c *gin.Context) {
	var req struct {
		Shop string `json:"shop" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Disconnect and remove the WA manager first.
	h.registry.Remove(req.Shop)
	if err := h.db.PurgeShop(req.Shop); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "shop data purged"})
}

// GET /internal/gdpr/customer-data?shop=...&phone=...
func (h *GDPRHandler) CustomerData(c *gin.Context) {
	shop := c.Query("shop")
	phone := c.Query("phone")
	if shop == "" || phone == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "shop and phone required"})
		return
	}
	data := h.db.GetCustomerData(shop, phone)
	c.JSON(http.StatusOK, data)
}
