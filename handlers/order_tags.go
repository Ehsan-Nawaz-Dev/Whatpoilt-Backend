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
//
// If the stored token is rejected by Shopify (an expired/invalid token, or a
// deprecated non-expiring token that the Admin API no longer accepts), the shop
// is flagged for re-authorization. There is no backend-only recovery: minting a
// new rotating token requires the merchant to re-open the app so the embedded
// OAuth/token-exchange flow can run with a real session token. The frontend
// surfaces a "Reconnect Shopify" banner for the flagged shop.
func (h *ShopifyHandler) tagOrderAsync(shop string, orderID int64, trigger models.TriggerType) {
	tag := h.tagForTrigger(shop, trigger)
	if tag == "" {
		slog.Debug("no tag configured for trigger — skipping", "shop", shop, "order", orderID, "trigger", trigger)
		return
	}
	token := h.db.GetShopToken(shop)
	if token == "" {
		slog.Warn("no access token for shop — flagging for re-auth", "shop", shop, "order", orderID, "trigger", trigger)
		_ = h.db.FlagShopReauth(shop, reasonNoToken)
		return
	}

	slog.Info("tagging order", "shop", shop, "order", orderID, "trigger", trigger, "tag", tag, "token_prefix", tokenPrefix(token))

	err := addShopifyOrderTag(shop, token, orderID, tag)
	if err == nil {
		slog.Info("order tagged successfully", "shop", shop, "order", orderID, "tag", tag)
		return
	}

	if isReauthRequiredError(err) {
		slog.Warn("shopify rejected access token — flagging shop for re-auth",
			"shop", shop, "order", orderID, "token_prefix", tokenPrefix(token), "err", err)
		h.flagAndForceReexchange(shop, err)
		return
	}

	slog.Error("order tag failed", "shop", shop, "order", orderID, "tag", tag, "token_prefix", tokenPrefix(token), "err", err)
}

// flagAndForceReexchange marks the shop for re-auth and, if Shopify rejected the
// token because the *stored* offline token is the wrong kind (a legacy
// non-expiring token) or is no longer valid, deletes the cached offline session.
//
// This is essential: the embedded app's authenticate.admin() reuses a stored
// offline session as long as it exists and hasn't expired. A non-expiring token
// has no expiry, so it is never seen as stale and is never re-exchanged — the
// merchant can reopen the app forever and keep sending the same dead token.
// Deleting the session forces a fresh token exchange (→ a proper expiring offline
// token) on the next app load, which then mirrors back and clears the flag.
func (h *ShopifyHandler) flagAndForceReexchange(shop string, err error) {
	_ = h.db.FlagShopReauth(shop, reasonInvalidToken)
	if isStaleOfflineToken(err) {
		_ = h.db.DeleteSession("offline_" + shop)
		slog.Info("discarded stale offline session — next app load will re-exchange for a fresh expiring token", "shop", shop)
	}
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

// isStaleOfflineToken reports whether the error means the *stored* offline token
// itself is unusable (a legacy non-expiring token Shopify no longer accepts, or a
// revoked/invalid token) — i.e. the cached offline session must be discarded so
// the app re-exchanges for a fresh one. This is narrower than isReauthRequiredError:
// a generic 403 (e.g. a missing scope) should flag for re-auth but NOT delete the
// session, since re-exchange wouldn't change the outcome.
func isStaleOfflineToken(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Non-expiring access tokens") ||
		strings.Contains(msg, "Invalid API key or access token") ||
		strings.Contains(msg, "unrecognized login")
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
