package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/config"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
)

type ShopifyHandler struct {
	registry *whatsapp.Registry
	db       *store.DB
}

func NewShopifyHandler(registry *whatsapp.Registry, db *store.DB) *ShopifyHandler {
	return &ShopifyHandler{registry: registry, db: db}
}

// shopFromWebhook reads the shop domain Shopify injects into every webhook.
func shopFromWebhook(c *gin.Context) string {
	return c.GetHeader("X-Shopify-Shop-Domain")
}

// POST /webhooks/orders/created
func (h *ShopifyHandler) OrderCreated(c *gin.Context) {
	body, ok := h.verifyAndRead(c)
	if !ok {
		return
	}
	var order models.ShopifyOrder
	if err := json.Unmarshal(body, &order); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	h.processOrder(c, shopFromWebhook(c), models.TriggerOrderCreated, order)
}

// POST /webhooks/orders/fulfilled
func (h *ShopifyHandler) OrderFulfilled(c *gin.Context) {
	body, ok := h.verifyAndRead(c)
	if !ok {
		return
	}
	var order models.ShopifyOrder
	if err := json.Unmarshal(body, &order); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	h.processOrder(c, shopFromWebhook(c), models.TriggerOrderFulfilled, order)
}

// POST /webhooks/orders/cancelled
func (h *ShopifyHandler) OrderCancelled(c *gin.Context) {
	body, ok := h.verifyAndRead(c)
	if !ok {
		return
	}
	var order models.ShopifyOrder
	if err := json.Unmarshal(body, &order); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	h.processOrder(c, shopFromWebhook(c), models.TriggerOrderCancelled, order)
}

// POST /webhooks/checkouts/create  (abandoned cart)
func (h *ShopifyHandler) AbandonedCart(c *gin.Context) {
	body, ok := h.verifyAndRead(c)
	if !ok {
		return
	}
	shop := shopFromWebhook(c)

	var checkout models.ShopifyCheckout
	if err := json.Unmarshal(body, &checkout); err != nil {
		slog.Warn("abandoned cart: invalid payload", "shop", shop, "err", err)
		c.JSON(http.StatusOK, gin.H{"skipped": "invalid payload"}) // return 200 so Shopify stops retrying
		return
	}
	// Resolve phone from customer or billing address
	phone := checkout.Customer.Phone
	if phone == "" {
		phone = checkout.BillingAddress.Phone
	}
	if phone == "" {
		c.JSON(http.StatusOK, gin.H{"skipped": "no phone number"})
		return
	}

	automations, _ := h.db.GetAutomationsByTrigger(shop, models.TriggerAbandonedCart)
	if len(automations) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "no active automations"})
		return
	}

	name := strings.TrimSpace(fmt.Sprintf("%s %s", checkout.Customer.FirstName, checkout.Customer.LastName))
	h.db.UpsertContact(shop, name, phone, fmt.Sprint(checkout.Customer.ID))

	h.enqueueAutomations(shop, automations, phone, models.TriggerAbandonedCart, map[string]string{
		"name":     name,
		"cart_url": checkout.AbandonedCheckoutURL,
		"total":    checkout.TotalPrice,
	})
	c.JSON(http.StatusOK, gin.H{"message": "queued"})
}

func (h *ShopifyHandler) processOrder(c *gin.Context, shop string, trigger models.TriggerType, order models.ShopifyOrder) {
	phone := order.ResolvePhone()
	slog.Info("webhook order received", "shop", shop, "trigger", trigger, "order", order.OrderNumber, "has_phone", phone != "")
	if phone == "" {
		slog.Warn("skipping order — no phone number on customer", "shop", shop, "order", order.OrderNumber)
		c.JSON(http.StatusOK, gin.H{"skipped": "no phone on order"})
		return
	}

	automations, _ := h.db.GetAutomationsByTrigger(shop, trigger)
	slog.Info("active automations for trigger", "shop", shop, "trigger", trigger, "count", len(automations))
	if len(automations) == 0 {
		slog.Warn("no active automations for trigger", "shop", shop, "trigger", trigger)
		c.JSON(http.StatusOK, gin.H{"message": "no active automations"})
		return
	}

	name := strings.TrimSpace(fmt.Sprintf("%s %s", order.Customer.FirstName, order.Customer.LastName))
	h.db.UpsertContact(shop, name, phone, fmt.Sprint(order.Customer.ID))

	h.enqueueAutomations(shop, automations, phone, trigger, map[string]string{
		"name":         name,
		"order_number": fmt.Sprint(order.OrderNumber),
		"total":        fmt.Sprintf("%s %s", order.TotalPrice, order.Currency),
	})

	// Tag the order in Shopify asynchronously — never blocks the webhook response.
	go h.tagOrderAsync(shop, order.ID, trigger)

	c.JSON(http.StatusOK, gin.H{"message": "queued"})
}

// enqueueAutomations writes jobs to the persistent pending_jobs table.
// The worker goroutine picks them up and delivers them — surviving restarts.
func (h *ShopifyHandler) enqueueAutomations(shop string, autos []models.Automation,
	phone string, trigger models.TriggerType, vars map[string]string) {

	if h.db.IsOptedOut(shop, phone) {
		slog.Info("skipping opted-out contact", "shop", shop, "phone", phone)
		return
	}

	for _, auto := range autos {
		tmpl, err := h.db.GetTemplate(auto.TemplateID, shop)
		if err != nil || !tmpl.IsActive {
			continue
		}

		resolved := resolveTemplate(tmpl.Content, vars)
		final := whatsapp.RandomizeMessageForTrigger(resolved, trigger)

		// "Post-Confirmation Reply" is held back until the customer confirms.
		// Store it as a pending confirmation instead of queuing immediately.
		if auto.Name == "Post-Confirmation Reply" {
			if err := h.db.StorePendingConfirmation(shop, phone, final,
				string(tmpl.MessageType), tmpl.Options); err != nil {
				slog.Error("store pending confirmation", "shop", shop, "err", err)
			}
			continue
		}

		runAt := time.Now().Add(whatsapp.JitterDelay(auto.DelayMinutes))
		if err := h.db.EnqueueJob(shop, auto.ID, tmpl.ID, phone, final,
			tmpl.MessageType, tmpl.Options, runAt); err != nil {
			slog.Error("enqueue job", "shop", shop, "err", err)
		}
	}
}

func (h *ShopifyHandler) verifyAndRead(c *gin.Context) ([]byte, bool) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read body"})
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	if !verifyShopifyHMAC(body, c.GetHeader("X-Shopify-Hmac-Sha256"), config.App.ShopifyAPISecret) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid HMAC signature"})
		return nil, false
	}
	return body, true
}

func verifyShopifyHMAC(body []byte, sig, secret string) bool {
	// In production the secret MUST be set (enforced in main.go startup check).
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal([]byte(base64.StdEncoding.EncodeToString(mac.Sum(nil))), []byte(sig))
}
