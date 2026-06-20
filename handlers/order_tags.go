package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/whatpilot/backend/models"
)

// defaultOrderTags are the emoji tags applied when a WA message is sent for each trigger.
var defaultOrderTags = map[models.TriggerType]string{
	models.TriggerOrderCreated:   "⏳ Pending Confirmation",
	models.TriggerOrderFulfilled: "📦 Shipped - WA Notified",
	models.TriggerOrderCancelled: "❌ Cancellation Sent",
	models.TriggerCODOrder:       "💵 COD - Confirmation Sent",
	models.TriggerPaymentPending: "⏳ Payment Pending",
	models.TriggerRefundCreated:  "💙 Refund Notified",
	models.TriggerBankTransfer:   "🏦 Bank Transfer Sent",
	models.TriggerWelcome:        "👋 Welcome Sent",
}

// tagForTrigger resolves the tag for a trigger: per-shop config → global admin config → built-in default.
func (h *ShopifyHandler) tagForTrigger(shop string, trigger models.TriggerType) string {
	key := "order_tag_" + string(trigger)
	// 1. Per-shop override set by the merchant from the Shopify app settings.
	if tag := h.db.GetShopOrderTag(shop, key); tag != "" {
		return tag
	}
	// 2. Global override set by the platform admin.
	if tag := h.db.GetAdminConfigValue(key); tag != "" {
		return tag
	}
	// 3. Built-in default.
	return defaultOrderTags[trigger]
}

// tagOrderAsync adds the trigger's tag to a Shopify order via GraphQL.
// Always called in a goroutine — failures are logged, never fatal.
func (h *ShopifyHandler) tagOrderAsync(shop string, orderID int64, trigger models.TriggerType) {
	tag := h.tagForTrigger(shop, trigger)
	if tag == "" {
		slog.Debug("no tag configured for trigger — skipping", "shop", shop, "order", orderID, "trigger", trigger)
		return
	}
	token := h.db.GetShopToken(shop)
	if token == "" {
		slog.Warn("no access token stored for shop — order tag skipped (merchant must open the app once to register the token)", "shop", shop, "order", orderID, "trigger", trigger)
		return
	}
	slog.Info("tagging order", "shop", shop, "order", orderID, "trigger", trigger, "tag", tag)
	if err := addShopifyOrderTag(shop, token, orderID, tag); err != nil {
		slog.Error("order tag failed", "shop", shop, "order", orderID, "tag", tag, "err", err)
	} else {
		slog.Info("order tagged successfully", "shop", shop, "order", orderID, "tag", tag)
	}
}

// addShopifyOrderTag calls the Shopify GraphQL Admin API tagsAdd mutation.
func addShopifyOrderTag(shop, token string, orderID int64, tag string) error {
	const query = `mutation tagsAdd($id: ID!, $tags: [String!]!) {
		tagsAdd(id: $id, tags: $tags) {
			node { id }
			userErrors { field message }
		}
	}`

	payload, _ := json.Marshal(map[string]interface{}{
		"query": query,
		"variables": map[string]interface{}{
			"id":   fmt.Sprintf("gid://shopify/Order/%d", orderID),
			"tags": []string{tag},
		},
	})

	url := fmt.Sprintf("https://%s/admin/api/2026-01/graphql.json", shop)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("shopify graphql status %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			TagsAdd struct {
				UserErrors []struct{ Message string } `json:"userErrors"`
			} `json:"tagsAdd"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if errs := result.Data.TagsAdd.UserErrors; len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Message
		}
		return fmt.Errorf("graphql errors: %s", strings.Join(msgs, "; "))
	}
	return nil
}
