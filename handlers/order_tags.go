package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
)

// defaultOrderTags are the emoji tags applied when a WA message is sent for each
// trigger. These mirror the frontend Settings defaults (app.settings.tsx) so the
// tag a merchant sees in the UI matches what's actually applied to the order.
var defaultOrderTags = map[models.TriggerType]string{
	models.TriggerOrderCreated:   "⏳ Pending Order Confirmation",
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
	if tag := h.db.GetShopOrderTag(shop, key); tag != "" {
		return tag
	}
	if tag := h.db.GetAdminConfigValue(key); tag != "" {
		return tag
	}
	return defaultOrderTags[trigger]
}

// migrateLegacyToken handles the "Non-expiring access tokens are no longer accepted"
// rejection by exchanging the legacy token for an expiring one (+ refresh token) via
// store.MigrateToExpiringToken. Returns the new token and true on success; (("",false)
// when the error isn't a legacy-token rejection or the migration itself fails (the
// caller then falls through to flagging for re-auth).
func migrateLegacyToken(db *store.DB, shop, oldToken string, err error) (string, bool) {
	if !isLegacyNonExpiringToken(err) {
		return "", false
	}
	slog.Warn("legacy non-expiring token rejected — migrating to expiring token", "shop", shop, "token_prefix", tokenPrefix(oldToken))
	newToken, mErr := db.MigrateToExpiringToken(shop, oldToken)
	if mErr != nil {
		slog.Error("token migration failed", "shop", shop, "err", mErr)
		return "", false
	}
	slog.Info("token migrated to expiring offline token", "shop", shop, "new_token_prefix", tokenPrefix(newToken))
	return newToken, true
}

// reauth reasons surfaced to the merchant via /api/settings/auth-status.
const (
	reasonInvalidToken = "Shopify rejected the stored access token. Please reconnect your store."
	reasonNoToken      = "No Shopify access token on file. Please reconnect your store."
)

// isReauthRequiredError reports whether a Shopify Admin API error means the
// stored token is no longer usable and the merchant must re-authorize. This
// covers expired/invalid tokens (HTTP 401/403) and the legacy non-expiring
// token deprecation message.
func isReauthRequiredError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Non-expiring access tokens") ||
		strings.Contains(msg, "status 401") ||
		strings.Contains(msg, "status 403") ||
		strings.Contains(msg, "Invalid API key or access token") ||
		strings.Contains(msg, "unrecognized login")
}

// isLegacyNonExpiringToken reports whether Shopify rejected the call specifically
// because the stored token is a legacy non-expiring offline token, which can be
// migrated in place to an expiring one (see migrateLegacyToken). This is narrower
// than isReauthRequiredError: a 401/403 from a revoked token or missing scope is
// NOT migratable and must fall through to flagging for re-auth.
func isLegacyNonExpiringToken(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Non-expiring access tokens")
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
