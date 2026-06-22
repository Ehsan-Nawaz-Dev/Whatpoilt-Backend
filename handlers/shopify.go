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
	shop := shopFromWebhook(c)

	// Fire COD confirmation automations asynchronously when payment_gateway
	// indicates cash-on-delivery (in addition to the standard order_created flow).
	if isCODOrder(order.PaymentGateway) {
		go h.processExtraOrderTrigger(shop, models.TriggerCODOrder, order)
	}

	// Fire payment-pending nudge when the order is unpaid and NOT a COD order.
	if isPaymentPending(order.FinancialStatus, order.PaymentGateway) {
		go h.processExtraOrderTrigger(shop, models.TriggerPaymentPending, order)
	}

	// Fire bank-transfer instructions when customer chose bank/wire transfer.
	if isBankTransferOrder(order.PaymentGateway) {
		go h.processExtraOrderTrigger(shop, models.TriggerBankTransfer, order)
	}

	h.processOrder(c, shop, models.TriggerOrderCreated, order)
}

// POST /webhooks/refunds/create
func (h *ShopifyHandler) RefundCreated(c *gin.Context) {
	body, ok := h.verifyAndRead(c)
	if !ok {
		return
	}
	shop := shopFromWebhook(c)

	var refund models.ShopifyRefund
	if err := json.Unmarshal(body, &refund); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	// Tag the refund's order immediately — independent of phone or automation status.
	go h.tagOrderAsync(shop, refund.OrderID, models.TriggerRefundCreated)

	// The refund webhook doesn't include customer details — fetch the parent order
	// so we can send a WhatsApp notification. If we can't fetch it, tagging already fired.
	token := h.db.GetShopToken(shop)
	if token == "" {
		slog.Warn("refund webhook: no access token — WA notification skipped, tag already queued", "shop", shop)
		c.JSON(http.StatusOK, gin.H{"skipped": "no access token for WA notification"})
		return
	}
	order, err := fetchShopifyOrder(shop, token, refund.OrderID)
	if err != nil {
		slog.Error("refund webhook: fetch order failed — WA notification skipped", "shop", shop, "order_id", refund.OrderID, "err", err)
		c.JSON(http.StatusOK, gin.H{"skipped": "could not fetch order for WA notification"})
		return
	}

	phone := order.ResolvePhone()
	if phone == "" {
		c.JSON(http.StatusOK, gin.H{"skipped": "no phone on order"})
		return
	}

	autos, _ := h.db.GetAutomationsByTrigger(shop, models.TriggerRefundCreated)
	if len(autos) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "no active automations"})
		return
	}

	name := strings.TrimSpace(fmt.Sprintf("%s %s", order.Customer.FirstName, order.Customer.LastName))
	h.db.UpsertContactNew(shop, name, phone, fmt.Sprint(order.Customer.ID))

	h.enqueueAutomations(shop, autos, phone, models.TriggerRefundCreated, map[string]string{
		"name":          name,
		"order_number":  fmt.Sprint(order.OrderNumber),
		"total":         fmt.Sprintf("%s %s", order.TotalPrice, order.Currency),
		"refund_amount": refund.TotalRefunded(),
	})

	c.JSON(http.StatusOK, gin.H{"message": "queued"})
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

	discountCartURL := checkout.AbandonedCheckoutURL
	if strings.Contains(discountCartURL, "?") {
		discountCartURL += "&discount=SAVE10"
	} else {
		discountCartURL += "?discount=SAVE10"
	}

	name := strings.TrimSpace(fmt.Sprintf("%s %s", checkout.Customer.FirstName, checkout.Customer.LastName))
	h.db.UpsertContactNew(shop, name, phone, fmt.Sprint(checkout.Customer.ID))

	h.enqueueAutomations(shop, automations, phone, models.TriggerAbandonedCart, map[string]string{
		"name":              name,
		"cart_url":          checkout.AbandonedCheckoutURL,
		"discount_cart_url": discountCartURL,
		"total":             checkout.TotalPrice,
	})
	c.JSON(http.StatusOK, gin.H{"message": "queued"})
}

func (h *ShopifyHandler) processOrder(c *gin.Context, shop string, trigger models.TriggerType, order models.ShopifyOrder) {
	slog.Info("webhook order received", "shop", shop, "trigger", trigger, "order", order.OrderNumber)

	// Tag the order immediately — independent of phone number or automation status.
	// Every order that fires a webhook should receive the trigger's Shopify tag.
	go h.tagOrderAsync(shop, order.ID, trigger)

	phone := order.ResolvePhone()
	if phone == "" {
		slog.Warn("skipping WA message — no phone number on order", "shop", shop, "order", order.OrderNumber)
		go h.TagOrderWithLabel(shop, order.ID, "No Whatsapp Found")
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

	// Welcome series: fire TriggerWelcome automations only for brand-new contacts.
	isNew := h.db.UpsertContactNew(shop, name, phone, fmt.Sprint(order.Customer.ID))
	if isNew {
		go h.processExtraOrderTrigger(shop, models.TriggerWelcome, order)
	}

	// Revenue attribution: tag order if we sent a WA message to this customer in
	// the last 24 hours (indicating WA influenced the conversion).
	if h.db.WASentRecently(shop, phone, 24) {
		go h.TagOrderWithLabel(shop, order.ID, "🤝 WA Influenced")
	}

	// Stamp last_order_at so win-back can detect inactivity.
	h.db.UpdateLastOrderAt(shop, phone)

	h.enqueueAutomations(shop, automations, phone, trigger, map[string]string{
		"name":         name,
		"order_number": fmt.Sprint(order.OrderNumber),
		"total":        fmt.Sprintf("%s %s", order.TotalPrice, order.Currency),
		"order_id":     fmt.Sprint(order.ID),
	})

	c.JSON(http.StatusOK, gin.H{"message": "queued"})
}

// TagOrderWithLabel adds a freeform string tag to a Shopify order (revenue attribution etc).
func (h *ShopifyHandler) TagOrderWithLabel(shop string, orderID int64, tag string) {
	token := h.db.GetShopToken(shop)
	if token == "" {
		_ = h.db.FlagShopReauth(shop, reasonNoToken)
		return
	}
	err := addShopifyOrderTag(shop, token, orderID, tag)
	if err == nil {
		return
	}
	if isReauthRequiredError(err) {
		slog.Warn("shopify rejected access token — flagging shop for re-auth",
			"shop", shop, "order", orderID, "err", err)
		_ = h.db.FlagShopReauth(shop, reasonInvalidToken)
		return
	}
	slog.Error("label tag failed", "shop", shop, "order", orderID, "tag", tag, "err", err)
}

func (h *ShopifyHandler) resolveTemplateByName(shop string, name string, vars map[string]string, trigger models.TriggerType) (string, string, []string) {
	tmpl, err := h.db.GetTemplateByName(name, shop)
	if err != nil || !tmpl.IsActive {
		return "", "", nil
	}
	resolved := resolveTemplate(tmpl.Content, vars)
	final := whatsapp.RandomizeMessageForTrigger(resolved, trigger)
	return final, string(tmpl.MessageType), tmpl.Options
}

// enqueueAutomations writes jobs to the persistent pending_jobs table.
// The worker goroutine picks them up and delivers them — surviving restarts.
func (h *ShopifyHandler) enqueueAutomations(shop string, autos []models.Automation,
	phone string, trigger models.TriggerType, vars map[string]string) {

	if h.db.IsOptedOut(shop, phone) {
		slog.Info("skipping opted-out contact", "shop", shop, "phone", phone)
		return
	}

	// A cancelled order voids the Post-Confirmation Reply that was stored when
	// the order was created. Without this cleanup the stale reply would fire
	// the next time the customer sends any message (e.g. responding to the
	// Cancellation Verification poll), making it look like a phantom
	// "Customer Order Confirmation" was sent.
	if trigger == models.TriggerOrderCancelled {
		h.db.DeletePendingConfirmation(shop, phone)
	}

	// Pass 1 — load every active template so we can find positiveOption regardless
	// of the order automations are returned from the DB.
	type loadedAuto struct {
		auto models.Automation
		tmpl models.Template
		msg  string // resolved + randomized content
	}
	items := make([]loadedAuto, 0, len(autos))
	for _, auto := range autos {
		tmpl, err := h.db.GetTemplate(auto.TemplateID, shop)
		if err != nil || !tmpl.IsActive {
			continue
		}
		resolved := resolveTemplate(tmpl.Content, vars)
		final := whatsapp.RandomizeMessageForTrigger(resolved, trigger)
		items = append(items, loadedAuto{auto, *tmpl, final})
	}

	// positiveOption, negativeOption, helpOption = options of the first active poll in this batch.
	var positiveOption, negativeOption, helpOption string
	for _, la := range items {
		if la.tmpl.MessageType == models.MessageTypePoll && len(la.tmpl.Options) > 0 {
			positiveOption = la.tmpl.Options[0]
			if len(la.tmpl.Options) > 1 {
				negativeOption = la.tmpl.Options[1]
			}
			if len(la.tmpl.Options) > 2 {
				helpOption = la.tmpl.Options[2]
			}
			break
		}
	}

	var yesMsg, yesType string
	var yesOpts []string

	var noMsg, noType string
	var noOpts []string

	var helpMsg, helpType string
	var helpOpts []string

	var hasPending bool

	// Pass 2 — enqueue jobs or collect pending replies.
	for _, la := range items {
		if isPostConfirmationReply(la.auto.Name) {
			hasPending = true
			if strings.HasSuffix(la.auto.Name, "Post-Confirmation Reply") {
				yesMsg = la.msg
				yesType = string(la.tmpl.MessageType)
				yesOpts = la.tmpl.Options
			} else if strings.HasSuffix(la.auto.Name, "Cancellation Reply") {
				noMsg = la.msg
				noType = string(la.tmpl.MessageType)
				noOpts = la.tmpl.Options
			} else if strings.HasSuffix(la.auto.Name, "Help Reply") {
				helpMsg = la.msg
				helpType = string(la.tmpl.MessageType)
				helpOpts = la.tmpl.Options
			}
		} else {
			runAt := time.Now().Add(whatsapp.JitterDelay(la.auto.DelayMinutes))
			if err := h.db.EnqueueJob(shop, la.auto.ID, la.tmpl.ID, phone, la.msg,
				la.tmpl.MessageType, la.tmpl.Options, runAt); err != nil {
				slog.Error("enqueue job", "shop", shop, "err", err)
			}
		}
	}

	if hasPending {
		if positiveOption == "" {
			slog.Warn("skipping pending confirmation storing — no active poll found", "shop", shop)
		} else {
			// Resolve templates for the flow dynamically
			var noMsgVal, noTypeVal string
			var noOptsVal []string
			var step2Yes, step2No, step2Help string

			if trigger == models.TriggerOrderCreated {
				noMsgVal, noTypeVal, noOptsVal = h.resolveTemplateByName(shop, "Cancellation Verification", vars, trigger)
				step2Yes, _, _ = h.resolveTemplateByName(shop, "Order Cancellation", vars, trigger)
				step2No = yesMsg // If they choose "No, keep order", send the confirmation reply
				step2Help, _, _ = h.resolveTemplateByName(shop, "Customer Help Reply", vars, trigger)
			} else if trigger == models.TriggerCODOrder {
				noMsgVal, noTypeVal, noOptsVal = h.resolveTemplateByName(shop, "Cancellation Verification", vars, trigger)
				step2Yes, _, _ = h.resolveTemplateByName(shop, "COD Cancellation Reply", vars, trigger)
				step2No = yesMsg // If they choose "No, keep order", send the COD confirmation reply
				step2Help, _, _ = h.resolveTemplateByName(shop, "COD Help Reply", vars, trigger)
			}

			// Fallback: if Cancellation Verification template wasn't resolved, use standard values
			if noMsgVal == "" {
				noMsgVal = noMsg
				noTypeVal = noType
				noOptsVal = noOpts
			}

			var orderID int64
			if idStr, ok := vars["order_id"]; ok {
				fmt.Sscanf(idStr, "%d", &orderID)
			}

			err := h.db.StorePendingConfirmationExtended(
				shop, phone,
				yesMsg, yesType, yesOpts, positiveOption,
				noMsgVal, noTypeVal, noOptsVal, negativeOption,
				helpMsg, helpType, helpOpts, helpOption,
				step2Yes, step2No, step2Help,
				orderID, "Order Confirmed", "",
			)
			if err != nil {
				slog.Error("store pending confirmation extended", "shop", shop, "err", err)
			}
		}
	}

	// Schedule a 24-hour no-reply reminder for order_created polls.
	// The worker will skip it if the customer replies before the 24h window.
	if trigger == models.TriggerOrderCreated {
		var hasPoll bool
		for _, la := range items {
			if la.tmpl.MessageType == models.MessageTypePoll && !isPostConfirmationReply(la.auto.Name) {
				hasPoll = true
				break
			}
		}
		if hasPoll {
			reminderMsg, _, _ := h.resolveTemplateByName(shop, "Order Confirmation Reminder", vars, trigger)
			if reminderMsg != "" {
				now := time.Now()
				if err := h.db.CreateReplyReminder(shop, phone, reminderMsg, "text", nil, now, now.Add(24*time.Hour)); err != nil {
					slog.Error("schedule reply reminder", "shop", shop, "err", err)
				}
			}
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

// processExtraOrderTrigger fires additional automations for a COD or payment-pending
// order without touching the HTTP context (always called in a goroutine).
func (h *ShopifyHandler) processExtraOrderTrigger(shop string, trigger models.TriggerType, order models.ShopifyOrder) {
	// Tag first — independent of phone or automation status.
	h.tagOrderAsync(shop, order.ID, trigger)

	phone := order.ResolvePhone()
	if phone == "" {
		go h.TagOrderWithLabel(shop, order.ID, "No Whatsapp Found")
		return
	}
	autos, _ := h.db.GetAutomationsByTrigger(shop, trigger)
	if len(autos) == 0 {
		return
	}
	name := strings.TrimSpace(fmt.Sprintf("%s %s", order.Customer.FirstName, order.Customer.LastName))
	h.db.UpsertContactNew(shop, name, phone, fmt.Sprint(order.Customer.ID))
	h.enqueueAutomations(shop, autos, phone, trigger, map[string]string{
		"name":         name,
		"order_number": fmt.Sprint(order.OrderNumber),
		"total":        fmt.Sprintf("%s %s", order.TotalPrice, order.Currency),
		"order_id":     fmt.Sprint(order.ID),
	})
}

// isPostConfirmationReply returns true for automations that should be held as a
// pending confirmation rather than sent immediately with the regular job queue.
func isPostConfirmationReply(name string) bool {
	return strings.HasSuffix(name, "Post-Confirmation Reply") ||
		strings.HasSuffix(name, "Cancellation Reply") ||
		strings.HasSuffix(name, "Help Reply")
}

// isBankTransferOrder returns true for bank/wire-transfer payment gateways.
// Excludes "manual" to avoid conflict with isCODOrder which already claims it.
func isBankTransferOrder(gateway string) bool {
	g := strings.ToLower(strings.TrimSpace(gateway))
	return g == "bank_transfer" || g == "bank_deposit" ||
		strings.Contains(g, "bank transfer") || strings.Contains(g, "wire transfer") ||
		strings.Contains(g, "bank_transfer") || strings.Contains(g, "wire_transfer")
}

// isCODOrder returns true for payment gateways that represent cash-on-delivery.
func isCODOrder(gateway string) bool {
	g := strings.ToLower(strings.TrimSpace(gateway))
	return g == "cash_on_delivery" || g == "cod" || g == "manual" ||
		strings.Contains(g, "cash on delivery") || strings.Contains(g, "cash-on-delivery")
}

// isPaymentPending returns true for orders with pending/unpaid status that are NOT COD
// (COD orders are always "pending" until physical delivery, so they use TriggerCODOrder).
func isPaymentPending(financialStatus, gateway string) bool {
	s := strings.ToLower(strings.TrimSpace(financialStatus))
	return (s == "pending" || s == "unpaid") && !isCODOrder(gateway)
}

// fetchShopifyOrder retrieves a single order from the Shopify REST Admin API.
// Used by the refund webhook to obtain customer contact details.
func fetchShopifyOrder(shop, token string, orderID int64) (*models.ShopifyOrder, error) {
	url := fmt.Sprintf("https://%s/admin/api/2026-01/orders/%d.json?fields=id,order_number,total_price,currency,phone,customer,shipping_address,billing_address", shop, orderID)
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
		return nil, fmt.Errorf("shopify API returned %d for order %d", resp.StatusCode, orderID)
	}
	var result struct {
		Order models.ShopifyOrder `json:"order"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result.Order, nil
}

// EnqueueWinBack enqueues win-back automations for a single inactive contact.
// Called from the main.go background goroutine.
func (h *ShopifyHandler) EnqueueWinBack(shop string, autos []models.Automation, contact models.Contact) {
	h.enqueueAutomations(shop, autos, contact.Phone, models.TriggerWinBack, map[string]string{
		"name":  contact.Name,
		"phone": contact.Phone,
	})
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
