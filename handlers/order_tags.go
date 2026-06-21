package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/whatpilot/backend/config"
	"github.com/whatpilot/backend/models"
)

// defaultOrderTags are the emoji tags applied when a WA message is sent for each trigger.
var defaultOrderTags = map[models.TriggerType]string{
	models.TriggerOrderCreated:   "Pending Order Confirmation",
	models.TriggerOrderFulfilled: "📦 Shipped - WA Notified",
	models.TriggerOrderCancelled: "❌ Cancellation Sent",
	models.TriggerCODOrder:       "Pending Order Confirmation",
	models.TriggerPaymentPending: "⏳ Payment Pending",
	models.TriggerRefundCreated:  "💙 Refund Notified",
	models.TriggerBankTransfer:   "🏦 Bank Transfer Sent",
	models.TriggerWelcome:        "👋 Welcome Sent",
}

// tagForTrigger resolves the tag for a trigger: per-shop config → global admin config → built-in default.
func (h *ShopifyHandler) tagForTrigger(shop string, trigger models.TriggerType) string {
	key := "order_tag_" + string(trigger)
	if tag := h.db.GetShopOrderTag(shop, key); tag != "" {
		return tag
	}
	if tag := h.db.GetAdminConfigValue(key); tag != "" {
		return tag
	}
	return defaultOrderTags[trigger]
}

// tagOrderAsync adds the trigger's tag to a Shopify order via GraphQL.
// If the stored token is a deprecated non-expiring token (403), it automatically
// exchanges it for a new rotating offline token and retries.
func (h *ShopifyHandler) tagOrderAsync(shop string, orderID int64, trigger models.TriggerType) {
	tag := h.tagForTrigger(shop, trigger)
	if tag == "" {
		slog.Debug("no tag configured for trigger — skipping", "shop", shop, "order", orderID, "trigger", trigger)
		return
	}
	token := h.db.GetShopToken(shop)
	if token == "" {
		slog.Warn("no access token for shop — order tag skipped", "shop", shop, "order", orderID, "trigger", trigger)
		return
	}

	// Proactively exchange if token is the old permanent format — avoids a failed API round-trip.
	if isDeprecatedTokenByPrefix(token) {
		slog.Warn("deprecated non-expiring token detected before tagging — exchanging proactively", "shop", shop, "token_prefix", tokenPrefix(token))
		newToken, exchErr := exchangeForRotatingToken(shop, token)
		if exchErr != nil {
			slog.Error("proactive token exchange failed", "shop", shop, "err", exchErr)
			return
		}
		_ = h.db.SetShopToken(shop, newToken)
		slog.Info("token exchanged proactively", "shop", shop, "new_token_prefix", tokenPrefix(newToken))
		token = newToken
	}

	slog.Info("tagging order", "shop", shop, "order", orderID, "trigger", trigger, "tag", tag, "token_prefix", tokenPrefix(token))

	err := addShopifyOrderTag(shop, token, orderID, tag)
	if err == nil {
		slog.Info("order tagged successfully", "shop", shop, "order", orderID, "tag", tag)
		return
	}

	// Fallback: if error message confirms a deprecated token (e.g. token wasn't prefixed shpat_ but still permanent).
	if isDeprecatedTokenError(err) {
		slog.Warn("deprecated non-expiring token detected — exchanging for rotating token", "shop", shop, "token_prefix", tokenPrefix(token))
		newToken, exchErr := exchangeForRotatingToken(shop, token)
		if exchErr != nil {
			slog.Error("token exchange failed", "shop", shop, "err", exchErr)
			return
		}
		if err2 := h.db.SetShopToken(shop, newToken); err2 != nil {
			slog.Error("failed to store rotated token", "shop", shop, "err", err2)
		}
		slog.Info("token exchanged — retrying order tag", "shop", shop, "order", orderID, "new_token_prefix", tokenPrefix(newToken))
		if err3 := addShopifyOrderTag(shop, newToken, orderID, tag); err3 != nil {
			slog.Error("order tag failed after token exchange", "shop", shop, "order", orderID, "tag", tag, "err", err3)
		} else {
			slog.Info("order tagged successfully after token exchange", "shop", shop, "order", orderID, "tag", tag)
		}
		return
	}

	slog.Error("order tag failed", "shop", shop, "order", orderID, "tag", tag, "token_prefix", tokenPrefix(token), "err", err)
}

// exchangeForRotatingToken exchanges a deprecated permanent offline token for a new
// rotating offline token using Shopify's OAuth token exchange grant.
func exchangeForRotatingToken(shop, oldToken string) (string, error) {
	payload, _ := json.Marshal(map[string]string{
		"client_id":            config.App.ShopifyAPIKey,
		"client_secret":        config.App.ShopifyAPISecret,
		"grant_type":           "urn:ietf:params:oauth:grant-type:token-exchange",
		"subject_token":        oldToken,
		"subject_token_type":   "urn:shopify:params:oauth:token-type:offline-access-token",
		"requested_token_type": "urn:shopify:params:oauth:token-type:offline-access-token",
	})

	url := fmt.Sprintf("https://%s/admin/oauth/access_token", shop)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("token exchange: parse response: %w", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("token exchange: empty access_token in response: %s", string(body))
	}
	return result.AccessToken, nil
}

func isDeprecatedTokenError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Non-expiring access tokens")
}

// isDeprecatedTokenByPrefix returns true for old permanent offline tokens (shpat_ prefix).
// New rotating offline tokens use the shpoa_ prefix.
func isDeprecatedTokenByPrefix(token string) bool {
	return strings.HasPrefix(token, "shpat_")
}

func tokenPrefix(token string) string {
	if len(token) > 12 {
		return token[:12] + "…"
	}
	return token
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

	url := fmt.Sprintf("https://%s/admin/api/2024-01/graphql.json", shop)
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
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("shopify graphql status %d: %s", resp.StatusCode, string(bodyBytes))
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
