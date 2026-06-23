package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/config"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/store"
)

// BillingHandler implements Shopify billing by calling the appSubscriptionCreate
// GraphQL mutation directly (the same Billing API the Shopify libraries use) and
// returning the confirmationUrl. The frontend then breaks out of the embedded
// iframe with window.top.location = confirmationUrl. This avoids the framework's
// embedded billing redirect, which fails with "refused to connect".
type BillingHandler struct{ db *store.DB }

func NewBillingHandler(db *store.DB) *BillingHandler { return &BillingHandler{db: db} }

const billingAPIVersion = "2026-01"

// usageCap is the monthly usage ceiling the merchant approves up front; it covers
// the full Starter→Growth→Professional auto-upgrade climb ($5 + $5) with headroom.
const usageCap = 20.0

const usageTerms = "Automatic plan upgrades when you exceed your monthly message allowance — billed only as used, up to $20/month."

// Create handles POST /api/billing/create. Body: { "plan": "free|starter|growth|professional" }.
func (h *BillingHandler) Create(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	var req struct {
		Plan string `json:"plan"`
	}
	_ = c.ShouldBindJSON(&req)
	planKey := strings.ToLower(strings.TrimSpace(req.Plan))

	// Free plan: no Shopify charge — activate immediately and let the app in.
	if planKey == "" || planKey == "free" {
		if err := h.db.SyncShopPlanWithLineItem(shop, "free", ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"free": true})
		return
	}

	name, price, _ := h.db.PlanInfo(planKey)
	if price <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid plan"})
		return
	}
	token := h.db.GetShopToken(shop)
	if token == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "No Shopify access token. Please reconnect your store."})
		return
	}

	// Recurring base + (for non-top tiers) a capped usage line item that lets the
	// backend auto-upgrade to the next tier without re-approval.
	lineItems := []map[string]interface{}{
		{"plan": map[string]interface{}{
			"appRecurringPricingDetails": map[string]interface{}{
				"price":    map[string]interface{}{"amount": price, "currencyCode": "USD"},
				"interval": "EVERY_30_DAYS",
			},
		}},
	}
	if store.NextPlanKey(planKey) != "" {
		lineItems = append(lineItems, map[string]interface{}{
			"plan": map[string]interface{}{
				"appUsagePricingDetails": map[string]interface{}{
					"cappedAmount": map[string]interface{}{"amount": usageCap, "currencyCode": "USD"},
					"terms":        usageTerms,
				},
			},
		})
	}

	// 3-day free trial on Starter only; Growth and Professional have no trial.
	trialDays := 0
	if planKey == "starter" {
		trialDays = 3
	}

	returnURL := fmt.Sprintf("%s/billing/confirm?shop=%s&plan=%s", config.App.PublicURL, shop, planKey)
	confirmationURL, err := createAppSubscription(shop, token, name+" Plan", returnURL, trialDays, lineItems)

	// Legacy non-expiring token rejected → migrate to an expiring one and retry once.
	if err != nil {
		if newToken, ok := migrateLegacyToken(h.db, shop, token, err); ok {
			confirmationURL, err = createAppSubscription(shop, newToken, name+" Plan", returnURL, trialDays, lineItems)
		}
	}
	if err != nil {
		slog.Error("billing: appSubscriptionCreate failed", "shop", shop, "plan", planKey, "err", err)
		// Token revoked/invalid → the cached offline session holds a dead token and
		// the embedded library keeps reusing it. Delete it so the next app load is
		// forced to mint a fresh token via token exchange, then ask the merchant to
		// reopen the app.
		if isReauthRequiredError(err) {
			_ = h.db.FlagShopReauth(shop, reasonInvalidToken)
			_ = h.db.DeleteSession("offline_" + shop)
			slog.Warn("billing: invalid token — cleared offline session to force re-exchange", "shop", shop)
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "Your Shopify connection was reset. Please close this app and reopen it from your Shopify admin, then choose a plan again.",
			})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "Could not start checkout: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"confirmationUrl": confirmationURL})
}

// Confirm handles GET /billing/confirm — the returnUrl Shopify sends the merchant
// to after approving (or declining) the charge. Public (hit by the browser, not
// the frontend), so it verifies the real subscription before activating anything.
func (h *BillingHandler) Confirm(c *gin.Context) {
	shop := c.Query("shop")
	plan := c.Query("plan")
	if shop == "" || plan == "" {
		c.String(http.StatusBadRequest, "missing shop or plan")
		return
	}

	token := h.db.GetShopToken(shop)
	if token == "" {
		c.String(http.StatusUnprocessableEntity, "No access token for shop; please reconnect.")
		return
	}

	// Verify there's an ACTIVE subscription and grab its usage line-item id.
	active, usageLineItemID, err := fetchActiveSubscription(shop, token)
	if err != nil {
		slog.Warn("billing: could not verify subscription — activating optimistically", "shop", shop, "err", err)
	}
	if err == nil && !active {
		slog.Info("billing: subscription not active (declined?)", "shop", shop, "plan", plan)
		c.Redirect(http.StatusFound, billingReturnURL(shop))
		return
	}

	if err := h.db.SyncShopPlanWithLineItem(shop, plan, usageLineItemID); err != nil {
		slog.Error("billing: activate plan failed", "shop", shop, "plan", plan, "err", err)
	} else {
		slog.Info("billing: plan activated", "shop", shop, "plan", plan, "usage_line_item", usageLineItemID)
	}
	c.Redirect(http.StatusFound, billingReturnURL(shop))
}

// billingReturnURL re-enters the embedded app in the Shopify admin.
func billingReturnURL(shop string) string {
	shopName := strings.TrimSuffix(shop, ".myshopify.com")
	if config.App.ShopifyAPIKey != "" {
		return fmt.Sprintf("https://admin.shopify.com/store/%s/apps/%s", shopName, config.App.ShopifyAPIKey)
	}
	return config.App.FrontendURL + "/app"
}

// createAppSubscription runs the appSubscriptionCreate mutation and returns the
// confirmationUrl the merchant must visit to approve the charge.
func createAppSubscription(shop, token, name, returnURL string, trialDays int, lineItems []map[string]interface{}) (string, error) {
	const mutation = `mutation appSubscriptionCreate($name: String!, $returnUrl: URL!, $test: Boolean, $trialDays: Int, $lineItems: [AppSubscriptionLineItemInput!]!) {
		appSubscriptionCreate(name: $name, returnUrl: $returnUrl, test: $test, trialDays: $trialDays, lineItems: $lineItems) {
			confirmationUrl
			appSubscription { id status }
			userErrors { field message }
		}
	}`

	vars := map[string]interface{}{
		"name":      name,
		"returnUrl": returnURL,
		"test":      config.App.Environment != "production",
		"trialDays": trialDays,
		"lineItems": lineItems,
	}
	body, err := shopifyGraphQL(shop, token, mutation, vars)
	if err != nil {
		return "", err
	}

	var result struct {
		Data struct {
			AppSubscriptionCreate struct {
				ConfirmationURL string `json:"confirmationUrl"`
				UserErrors      []struct{ Message string } `json:"userErrors"`
			} `json:"appSubscriptionCreate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if errs := result.Data.AppSubscriptionCreate.UserErrors; len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Message
		}
		return "", fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	if result.Data.AppSubscriptionCreate.ConfirmationURL == "" {
		return "", fmt.Errorf("no confirmationUrl returned: %s", string(body))
	}
	return result.Data.AppSubscriptionCreate.ConfirmationURL, nil
}

// fetchActiveSubscription returns whether the shop has an ACTIVE app subscription
// and the id of its usage line item (empty if none).
func fetchActiveSubscription(shop, token string) (active bool, usageLineItemID string, err error) {
	const query = `query {
		currentAppInstallation {
			activeSubscriptions {
				id
				status
				lineItems {
					id
					plan { pricingDetails { __typename } }
				}
			}
		}
	}`
	body, err := shopifyGraphQL(shop, token, query, nil)
	if err != nil {
		return false, "", err
	}

	var result struct {
		Data struct {
			CurrentAppInstallation struct {
				ActiveSubscriptions []struct {
					Status    string `json:"status"`
					LineItems []struct {
						ID   string `json:"id"`
						Plan struct {
							PricingDetails struct {
								TypeName string `json:"__typename"`
							} `json:"pricingDetails"`
						} `json:"plan"`
					} `json:"lineItems"`
				} `json:"activeSubscriptions"`
			} `json:"currentAppInstallation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, "", err
	}
	for _, sub := range result.Data.CurrentAppInstallation.ActiveSubscriptions {
		if sub.Status != "ACTIVE" {
			continue
		}
		active = true
		for _, li := range sub.LineItems {
			if li.Plan.PricingDetails.TypeName == "AppUsagePricing" {
				usageLineItemID = li.ID
			}
		}
	}
	return active, usageLineItemID, nil
}

// shopifyGraphQL posts a GraphQL request to the shop's Admin API.
func shopifyGraphQL(shop, token, query string, variables map[string]interface{}) ([]byte, error) {
	payload, _ := json.Marshal(map[string]interface{}{"query": query, "variables": variables})
	url := fmt.Sprintf("https://%s/admin/api/%s/graphql.json", shop, billingAPIVersion)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graphql status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
