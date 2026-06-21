package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/store"
)

type RetagHandler struct {
	db *store.DB
}

func NewRetagHandler(db *store.DB) *RetagHandler {
	return &RetagHandler{db: db}
}

type shopifyOrderSummary struct {
	ID                int64  `json:"id"`
	FinancialStatus   string `json:"financial_status"`
	FulfillmentStatus string `json:"fulfillment_status"`
	PaymentGateway    string `json:"payment_gateway"`
	CancelledAt       string `json:"cancelled_at"`
}

// POST /api/retag-orders
// Fetches recent Shopify orders and applies status tags to all of them.
func (h *RetagHandler) Retag(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	token := h.db.GetShopToken(shop)
	if token == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "No access token for this shop. Open the WhatPilot app in Shopify admin once to register your token, then try again.",
		})
		return
	}

	// Auto-exchange deprecated non-expiring token (shpat_ prefix) before bulk tagging.
	if isDeprecatedTokenByPrefix(token) {
		slog.Warn("retag: deprecated non-expiring token detected — exchanging", "shop", shop, "token_prefix", tokenPrefix(token))
		newToken, exchErr := exchangeForRotatingToken(shop, token)
		if exchErr != nil {
			slog.Error("retag: token exchange failed", "shop", shop, "err", exchErr)
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Token exchange failed: " + exchErr.Error()})
			return
		}
		_ = h.db.SetShopToken(shop, newToken)
		slog.Info("retag: token exchanged successfully", "shop", shop, "new_token_prefix", tokenPrefix(newToken))
		token = newToken
	}

	orders, err := fetchRecentOrders(shop, token, 250)
	if err != nil {
		slog.Error("retag: fetch orders failed", "shop", shop, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to fetch orders from Shopify: " + err.Error()})
		return
	}

	tagged, failed := 0, 0
	for _, order := range orders {
		tag := retagForOrder(order)
		if tag == "" {
			continue
		}
		if err := addShopifyOrderTag(shop, token, order.ID, tag); err != nil {
			slog.Error("retag: tag failed", "shop", shop, "order", order.ID, "err", err)
			failed++
		} else {
			tagged++
		}
	}

	slog.Info("retag complete", "shop", shop, "tagged", tagged, "failed", failed, "total", len(orders))
	c.JSON(http.StatusOK, gin.H{
		"message": "Retagging complete",
		"tagged":  tagged,
		"failed":  failed,
		"total":   len(orders),
	})
}

func retagForOrder(o shopifyOrderSummary) string {
	if o.CancelledAt != "" {
		return "❌ Cancellation Sent"
	}
	if o.FinancialStatus == "refunded" || o.FinancialStatus == "partially_refunded" {
		return "💙 Refund Notified"
	}
	if o.FulfillmentStatus == "fulfilled" {
		return "📦 Shipped - WA Notified"
	}
	if isCODOrder(o.PaymentGateway) {
		return "💵 COD - Confirmation Sent"
	}
	if isBankTransferOrder(o.PaymentGateway) {
		return "🏦 Bank Transfer Sent"
	}
	if isPaymentPending(o.FinancialStatus, o.PaymentGateway) {
		return "⏳ Payment Pending"
	}
	return "⏳ Pending Confirmation"
}

func fetchRecentOrders(shop, token string, limit int) ([]shopifyOrderSummary, error) {
	url := fmt.Sprintf(
		"https://%s/admin/api/2026-01/orders.json?limit=%d&status=any&fields=id,financial_status,fulfillment_status,payment_gateway,cancelled_at",
		shop, limit,
	)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("shopify returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		Orders []shopifyOrderSummary `json:"orders"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Orders, nil
}
