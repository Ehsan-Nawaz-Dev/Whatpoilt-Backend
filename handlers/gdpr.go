// Package handlers — GDPR compliance endpoints called by the frontend
// after validating the Shopify webhook.
package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/config"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
)

// WebhookEntry handles Shopify's mandatory compliance webhooks delivered to the
// bare /webhooks URL (customers/redact, shop/redact, customers/data_request),
// per shopify.app.toml. HMAC-verified: returns 401 on a bad signature (as Shopify
// requires) and 200 otherwise so Shopify doesn't retry. Without this route these
// webhooks would 404 — a hard App Store failure.
func (h *GDPRHandler) WebhookEntry(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read body"})
		return
	}
	if !verifyShopifyHMAC(body, c.GetHeader("X-Shopify-Hmac-Sha256"), config.App.ShopifyAPISecret) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid HMAC signature"})
		return
	}

	var payload struct {
		ShopDomain string `json:"shop_domain"`
		Customer   struct {
			Phone string `json:"phone"`
		} `json:"customer"`
	}
	_ = json.Unmarshal(body, &payload)

	switch c.GetHeader("X-Shopify-Topic") {
	case "customers/redact":
		_ = h.db.PurgeCustomer(payload.ShopDomain, payload.Customer.Phone)
	case "shop/redact":
		h.registry.Remove(payload.ShopDomain)
		_ = h.db.PurgeShop(payload.ShopDomain)
	case "customers/data_request":
		// Acknowledged — the only PII held is phone + message logs, already visible
		// to the merchant in-app. Nothing further to compile.
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

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
