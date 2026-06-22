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

// Conversation-flow lifecycle tags, applied as the customer responds to the
// confirmation poll. Part of the mutually-exclusive status set below. Exported so
// the confirmation flow in main.go applies the exact same values.
const (
	FlowTagConfirmed = "Order Confirmed"
	FlowTagCancelled = "Order Cancel"
)

// lifecycleTriggers are the triggers whose tags represent a mutually-exclusive
// order status. Welcome and attribute tags ("WA Influenced", "No Whatsapp Found")
// are intentionally excluded — those are additive and persist across status changes.
var lifecycleTriggers = []models.TriggerType{
	models.TriggerOrderCreated, models.TriggerOrderFulfilled,
	models.TriggerOrderCancelled, models.TriggerCODOrder,
	models.TriggerPaymentPending, models.TriggerRefundCreated,
	models.TriggerBankTransfer,
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

// lifecycleTagSet returns every status tag this shop can apply to an order — the
// per-trigger tags (shop-configured or default) plus the confirm/cancel flow tags.
// These are mutually exclusive: applying one should replace the others.
func (h *ShopifyHandler) lifecycleTagSet(shop string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, tr := range lifecycleTriggers {
		add(h.tagForTrigger(shop, tr))
	}
	add(FlowTagConfirmed)
	add(FlowTagCancelled)
	return out
}

// SetOrderLifecycleTag moves an order to a single status: it removes every other
// known lifecycle tag, then adds newTag — so the order shows only its current
// state (Pending → Confirmed/Cancelled → Shipped) instead of accumulating every
// stage. Attribute tags (WA Influenced, No Whatsapp Found, Welcome) are untouched.
func (h *ShopifyHandler) SetOrderLifecycleTag(shop string, orderID int64, newTag string) {
	if orderID == 0 || newTag == "" {
		return
	}
	token := h.db.GetShopToken(shop)
	if token == "" {
		_ = h.db.FlagShopReauth(shop, reasonNoToken)
		return
	}

	// Remove the other status tags first so only the new status remains.
	var remove []string
	for _, t := range h.lifecycleTagSet(shop) {
		if t != newTag {
			remove = append(remove, t)
		}
	}
	if len(remove) > 0 {
		if err := removeShopifyOrderTags(shop, token, orderID, remove); err != nil {
			if newToken, ok := migrateLegacyToken(h.db, shop, token, err); ok {
				token = newToken
				_ = removeShopifyOrderTags(shop, token, orderID, remove)
			} else {
				slog.Warn("lifecycle tag remove failed", "shop", shop, "order", orderID, "err", err)
			}
		}
	}

	// Add the new status tag, reusing the legacy-token migration / re-auth handling.
	err := addShopifyOrderTag(shop, token, orderID, newTag)
	if err == nil {
		slog.Info("order lifecycle tag set", "shop", shop, "order", orderID, "tag", newTag)
		return
	}
	if newToken, ok := migrateLegacyToken(h.db, shop, token, err); ok {
		if err2 := addShopifyOrderTag(shop, newToken, orderID, newTag); err2 != nil {
			slog.Error("lifecycle tag add failed after migration", "shop", shop, "order", orderID, "tag", newTag, "err", err2)
		}
		return
	}
	if isReauthRequiredError(err) {
		slog.Warn("shopify rejected token — flagging for re-auth", "shop", shop, "order", orderID, "err", err)
		_ = h.db.FlagShopReauth(shop, reasonInvalidToken)
		return
	}
	slog.Error("lifecycle tag add failed", "shop", shop, "order", orderID, "tag", newTag, "err", err)
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

// addShopifyOrderTag adds a single tag to a Shopify order (tagsAdd).
func addShopifyOrderTag(shop, token string, orderID int64, tag string) error {
	return mutateShopifyOrderTags(shop, token, "tagsAdd", orderID, []string{tag})
}

// removeShopifyOrderTags removes tags from a Shopify order (tagsRemove). Tags that
// aren't present are ignored by Shopify.
func removeShopifyOrderTags(shop, token string, orderID int64, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	return mutateShopifyOrderTags(shop, token, "tagsRemove", orderID, tags)
}

// mutateShopifyOrderTags runs the Shopify GraphQL Admin API tagsAdd or tagsRemove
// mutation. mutation must be "tagsAdd" or "tagsRemove".
func mutateShopifyOrderTags(shop, token, mutation string, orderID int64, tags []string) error {
	query := fmt.Sprintf(`mutation %s($id: ID!, $tags: [String!]!) {
		%s(id: $id, tags: $tags) {
			node { id }
			userErrors { field message }
		}
	}`, mutation, mutation)

	payload, _ := json.Marshal(map[string]interface{}{
		"query": query,
		"variables": map[string]interface{}{
			"id":   fmt.Sprintf("gid://shopify/Order/%d", orderID),
			"tags": tags,
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

	// userErrors live under data.<mutation>.userErrors — key varies by mutation.
	var result struct {
		Data map[string]struct {
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if errs := result.Data[mutation].UserErrors; len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Message
		}
		return fmt.Errorf("graphql errors: %s", strings.Join(msgs, "; "))
	}
	return nil
}
