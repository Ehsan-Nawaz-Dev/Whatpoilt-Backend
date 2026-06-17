package models

import (
	"fmt"
	"time"
)

type TriggerType string

const (
	TriggerOrderCreated   TriggerType = "order_created"
	TriggerOrderFulfilled TriggerType = "order_fulfilled"
	TriggerAbandonedCart  TriggerType = "abandoned_cart"
	TriggerOrderCancelled TriggerType = "order_cancelled"
	TriggerCODOrder       TriggerType = "cod_order"       // cash-on-delivery confirmation poll
	TriggerPaymentPending TriggerType = "payment_pending" // unpaid order payment nudge
	TriggerRefundCreated  TriggerType = "refund_created"  // refund processed notification
	TriggerWelcome        TriggerType = "welcome"         // new contact first opt-in
	TriggerWinBack        TriggerType = "win_back"        // inactive customer re-engagement
)

type MessageStatus string

const (
	MessageStatusPending MessageStatus = "pending"
	MessageStatusSent    MessageStatus = "sent"
	MessageStatusFailed  MessageStatus = "failed"
)

// MessageType controls which WhatsApp interactive format is used.
type MessageType string

const (
	MessageTypeText    MessageType = "text"    // plain text
	MessageTypePoll    MessageType = "poll"    // WhatsApp native poll (voting)
	MessageTypeButtons MessageType = "buttons" // quick-reply buttons (max 3)
)

// Template is a WhatsApp message template.
// Variables use <<variable_name>> syntax:
//
//	<<name>>         Customer full name
//	<<order_number>> Shopify order number
//	<<total>>        Order total with currency
//	<<cart_url>>     Abandoned-cart recovery URL
//	<<tracking_url>> Shipment tracking link
type Template struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Content     string      `json:"content"`      // Text body (all types) / poll question (poll)
	MessageType MessageType `json:"message_type"` // "text" | "poll" | "buttons"
	Options     []string    `json:"options"`      // Poll choices OR button labels
	IsActive    bool        `json:"is_active"`
	IsDefault   bool        `json:"is_default"` // true for the 9 built-in templates
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// DefaultTemplates are the 9 pre-built templates seeded for every new shop.
var DefaultTemplates = []Template{
	{
		Name:        "Order Confirmation",
		MessageType: MessageTypePoll,
		Content:     "Hi <<name>>! 🛍️ Your order #<<order_number>> has been confirmed (<<total>>). Did you place this order?",
		Options:     []string{"✅ Yes, that's me!", "❌ No, I didn't place this", "❓ I need help"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Post-Confirmation Reply",
		MessageType: MessageTypeText,
		Content:     "Thank you for confirming, <<name>>! 🎉\n\nYour order #<<order_number>> is being prepared with care. We'll notify you once it ships!\n\nFor any questions, just reply to this message anytime.",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Order Cancellation",
		MessageType: MessageTypeText,
		Content:     "Hi <<name>> 😔\n\nYour order #<<order_number>> (<<total>>) has been cancelled as requested.\n\nIf this was a mistake or you have questions, please contact our support team immediately.\n\nWe hope to serve you again soon! 💙",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Admin Order Alert",
		MessageType: MessageTypeText,
		Content:     "🔔 *NEW ORDER*\n\n📋 Order #<<order_number>>\n👤 Customer: <<name>>\n💰 Total: <<total>>\n\nPlease review and process this order.",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Admin Order Confirmed Alert",
		MessageType: MessageTypeText,
		Content:     "✅ *ORDER CONFIRMED*\n\nOrder #<<order_number>> has been confirmed by the customer.\n👤 <<name>>\n💰 <<total>>\n\nReady to fulfill!",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Abandoned Cart Recovery",
		MessageType: MessageTypePoll,
		Content:     "Hi <<name>>! 👋 You left items worth <<total>> in your cart. Can we help you complete your purchase?\n\n🛒 Complete here: <<cart_url>>",
		Options:     []string{"🛒 Yes, I'll buy now!", "💡 I need help deciding", "❌ Not interested"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Shipping Alert",
		MessageType: MessageTypeButtons,
		Content:     "📦 Great news, <<name>>!\n\nYour order #<<order_number>> has been shipped and is on its way!\n\nExpected delivery in 3-5 business days. 🚚",
		Options:     []string{"Track My Order", "Contact Support"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Delivery Alert",
		MessageType: MessageTypePoll,
		Content:     "🎉 Your order #<<order_number>> has been delivered, <<name>>!\n\nWe hope you love your purchase! How was your delivery experience?",
		Options:     []string{"⭐⭐⭐⭐⭐ Excellent!", "⭐⭐⭐⭐ Good", "⭐⭐⭐ Average", "😕 Had issues"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Cancellation Verification",
		MessageType: MessageTypePoll,
		Content:     "Hi <<name>>, we received a cancellation request for order #<<order_number>> (<<total>>). Are you sure you want to cancel?",
		Options:     []string{"✅ Yes, cancel my order", "❌ No, keep my order", "📞 I need to speak to someone"},
		IsActive:    true,
		IsDefault:   true,
	},
	// ── 9 additional built-in templates (total = 18) ──────────────────────────
	{
		Name:        "Order Processing",
		MessageType: MessageTypeText,
		Content:     "Hi <<name>>! ⚙️ Your order #<<order_number>> (<<total>>) is now being processed.\n\nWe'll send you a shipping notification the moment it's on its way. Thank you for choosing us! 💙",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Quick Order Thanks",
		MessageType: MessageTypeText,
		Content:     "Thank you for your order, <<name>>! 🎉\n\nOrder #<<order_number>> worth <<total>> has been received. We're packing it up right now and will notify you once it ships!",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Shipping Confirmation",
		MessageType: MessageTypeText,
		Content:     "Hi <<name>>! 🚚 Your order #<<order_number>> has left our warehouse and is heading your way.\n\nTrack your shipment here: <<tracking_url>>\n\nExpected arrival: 3–5 business days.",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Post-Purchase Review",
		MessageType: MessageTypePoll,
		Content:     "Hi <<name>>! 🌟 We hope you're loving your order #<<order_number>>.\n\nHow would you rate your overall shopping experience with us?",
		Options:     []string{"⭐⭐⭐⭐⭐ Excellent!", "⭐⭐⭐⭐ Great", "⭐⭐⭐ Average", "😕 Disappointing"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Refund Initiated",
		MessageType: MessageTypeText,
		Content:     "Hi <<name>> 💙\n\nWe've initiated the refund for your cancelled order #<<order_number>> (<<total>>).\n\nRefunds typically appear within 5–7 business days depending on your bank. We hope to serve you again soon!",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Win-Back Offer",
		MessageType: MessageTypeButtons,
		Content:     "Hi <<name>>! 😊 We noticed your order #<<order_number>> was cancelled.\n\nWe'd love to have you back! Here's a special offer just for you.",
		Options:     []string{"🛍️ Shop Again", "💬 Talk to Support"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Cart Save Reminder",
		MessageType: MessageTypeText,
		Content:     "Hey <<name>>! 👋 Just a friendly reminder — you left items worth <<total>> in your cart.\n\nYour items are reserved for a limited time. Complete your purchase here: <<cart_url>> 🛒",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Cart Discount Offer",
		MessageType: MessageTypeButtons,
		Content:     "Hi <<name>>! 🏷️ Still thinking about your cart worth <<total>>?\n\nComplete your purchase today and enjoy an exclusive discount. Don't let your items sell out!\n\n🛒 <<cart_url>>",
		Options:     []string{"🛍️ Complete Purchase", "❓ I Need Help"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Admin Cancellation Alert",
		MessageType: MessageTypeText,
		Content:     "⚠️ *ORDER CANCELLED*\n\n📋 Order #<<order_number>>\n👤 Customer: <<name>>\n💰 Amount: <<total>>\n\nPlease process the refund and update inventory accordingly.",
		IsActive:    true,
		IsDefault:   true,
	},
	// ── COD Confirmation (2) ──────────────────────────────────────────────────
	{
		Name:        "COD Confirmation",
		MessageType: MessageTypePoll,
		Content:     "Hi <<name>>! 💵 Your cash-on-delivery order #<<order_number>> (<<total>>) is ready to dispatch.\n\nPlease confirm you'll be available to accept the delivery.",
		Options:     []string{"✅ Yes, I'll accept delivery", "❌ No, please cancel my order", "📞 Call me first"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "COD Confirmation Reply",
		MessageType: MessageTypeText,
		Content:     "Great news, <<name>>! 🚚 Your COD order #<<order_number>> is confirmed for dispatch.\n\nOur delivery team will contact you before arriving. Please keep <<total>> ready.\n\nThank you for shopping with us!",
		IsActive:    true,
		IsDefault:   true,
	},
	// ── Payment Pending (1) ───────────────────────────────────────────────────
	{
		Name:        "Payment Reminder",
		MessageType: MessageTypeText,
		Content:     "Hi <<name>>! 💳 Your order #<<order_number>> worth <<total>> is awaiting payment.\n\nComplete your payment to confirm the order and we'll start processing it right away.\n\nIf you have any issues, just reply here and we'll help!",
		IsActive:    true,
		IsDefault:   true,
	},
	// ── Refund Created (1) ────────────────────────────────────────────────────
	{
		Name:        "Refund Status Update",
		MessageType: MessageTypeText,
		Content:     "Hi <<name>>! 💙 Your refund of <<refund_amount>> for order #<<order_number>> has been successfully processed.\n\nPlease allow 5–7 business days for the amount to appear in your account, depending on your bank.\n\nWe hope to serve you again soon! 🙏",
		IsActive:    true,
		IsDefault:   true,
	},
	// ── Upsell After Purchase (1) — uses TriggerOrderCreated with delay ────────
	{
		Name:        "Upsell Offer",
		MessageType: MessageTypeButtons,
		Content:     "Hi <<name>>! 🛍️ We hope you're excited about order #<<order_number>>!\n\nCustomers who bought this also loved these items. Would you like to explore?",
		Options:     []string{"🛒 Shop More", "💬 Ask a Question"},
		IsActive:    true,
		IsDefault:   true,
	},
	// ── Review Request (1) — uses TriggerOrderFulfilled with 3-day delay ────────
	{
		Name:        "Review Request",
		MessageType: MessageTypePoll,
		Content:     "Hi <<name>>! 🌟 Your order #<<order_number>> should have arrived by now.\n\nWe'd love to hear how your experience was. Would you mind sharing a quick rating?",
		Options:     []string{"⭐⭐⭐⭐⭐ Excellent!", "⭐⭐⭐⭐ Great", "⭐⭐⭐ Average", "😕 Needs improvement"},
		IsActive:    true,
		IsDefault:   true,
	},
	// ── Delivery Follow-Up (1) — uses TriggerOrderFulfilled with 5-day delay ────
	{
		Name:        "Delivery Follow-Up",
		MessageType: MessageTypePoll,
		Content:     "Hi <<name>>! 📦 We wanted to check in on order #<<order_number>>.\n\nDid your delivery arrive safely?",
		Options:     []string{"✅ Yes, received!", "⏳ Still waiting", "❌ Not received", "📞 I need help"},
		IsActive:    true,
		IsDefault:   true,
	},
	// ── Welcome Series (1) — fires on TriggerWelcome ──────────────────────────
	{
		Name:        "Welcome Message",
		MessageType: MessageTypeText,
		Content:     "👋 Welcome, <<name>>!\n\nThank you for connecting with us on WhatsApp. You'll receive order updates, exclusive offers, and support right here.\n\nReply *STOP* anytime to unsubscribe. 💙",
		IsActive:    true,
		IsDefault:   true,
	},
	// ── Win-Back Campaign (1) — fires on TriggerWinBack ──────────────────────
	{
		Name:        "Win-Back Message",
		MessageType: MessageTypeButtons,
		Content:     "Hi <<name>>! 😊 We miss you!\n\nIt's been a while since your last order and we'd love to have you back. Here's something special just for you.",
		Options:     []string{"🛍️ Shop Now", "💬 Chat with Us"},
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "Customer Help Reply",
		MessageType: MessageTypeText,
		Content:     "Hello <<name>>! 💬 We have received your request for help regarding order #<<order_number>>. A support agent will contact you shortly.",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "COD Cancellation Reply",
		MessageType: MessageTypeText,
		Content:     "We're sorry to hear that, <<name>>. 😔 We have received your request to cancel COD order #<<order_number>>. It has been flagged for cancellation.",
		IsActive:    true,
		IsDefault:   true,
	},
	{
		Name:        "COD Help Reply",
		MessageType: MessageTypeText,
		Content:     "Hello <<name>>! 💬 We have received your help request regarding COD order #<<order_number>>. A support agent will contact you shortly.",
		IsActive:    true,
		IsDefault:   true,
	},
}

// Automation defines a rule that triggers WhatsApp messages based on Shopify events.
type Automation struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	TriggerType  TriggerType `json:"trigger_type"`
	TemplateID   string      `json:"template_id"`
	IsActive     bool        `json:"is_active"`
	DelayMinutes int         `json:"delay_minutes"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// Contact stores customer WhatsApp contact information.
type Contact struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Phone             string    `json:"phone"`
	ShopifyCustomerID string    `json:"shopify_customer_id,omitempty"`
	OptedOut          bool      `json:"opted_out"`
	CreatedAt         time.Time `json:"created_at"`
}

// PendingJob is a row in the persistent job queue. Survives server restarts.
type PendingJob struct {
	ID          string      `json:"id"`
	ShopDomain  string      `json:"shop_domain"`
	Phone       string      `json:"phone"`
	Message     string      `json:"message"`
	MessageType MessageType `json:"message_type"`
	Options     []string    `json:"options"`
	AutomationID string     `json:"automation_id"`
	TemplateID  string      `json:"template_id"`
	Attempts    int         `json:"attempts"`
	MaxAttempts int         `json:"max_attempts"`
}

// MessageLog records every WhatsApp message attempt.
type MessageLog struct {
	ID           string        `json:"id"`
	AutomationID string        `json:"automation_id,omitempty"`
	ContactPhone string        `json:"contact_phone"`
	TemplateID   string        `json:"template_id,omitempty"`
	Content      string        `json:"content"`
	Status       MessageStatus `json:"status"`
	Error        string        `json:"error,omitempty"`
	SentAt       *time.Time    `json:"sent_at,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
}

// ShopifyOrder is the subset of Shopify order data we care about.
type ShopifyOrder struct {
	ID              int64             `json:"id"`
	OrderNumber     int               `json:"order_number"`
	TotalPrice      string            `json:"total_price"`
	Currency        string            `json:"currency"`
	Phone           string            `json:"phone"` // top-level order phone
	PaymentGateway  string            `json:"payment_gateway"`  // e.g. "cash_on_delivery", "manual", "shopify_payments"
	FinancialStatus string            `json:"financial_status"` // "pending", "paid", "unpaid", "refunded"
	Customer        ShopifyCustomer   `json:"customer"`
	ShippingAddress ShopifyAddress    `json:"shipping_address"`
	BillingAddress  ShopifyAddress    `json:"billing_address"`
	LineItems       []ShopifyLineItem `json:"line_items"`
}

// ShopifyRefund is the payload Shopify sends on the refunds/create webhook.
type ShopifyRefund struct {
	ID           int64                  `json:"id"`
	OrderID      int64                  `json:"order_id"`
	Note         string                 `json:"note"`
	Transactions []ShopifyTransaction   `json:"transactions"`
}

type ShopifyTransaction struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Status   string `json:"status"` // "success", "pending", "failure"
}

// TotalRefunded returns the sum of successful transaction amounts as a formatted string.
func (r *ShopifyRefund) TotalRefunded() string {
	var total float64
	currency := ""
	for _, t := range r.Transactions {
		if t.Status == "success" || t.Status == "" {
			var amt float64
			fmt.Sscanf(t.Amount, "%f", &amt)
			total += amt
			if currency == "" {
				currency = t.Currency
			}
		}
	}
	if currency != "" {
		return fmt.Sprintf("%.2f %s", total, currency)
	}
	return fmt.Sprintf("%.2f", total)
}

// ResolvePhone returns the first non-empty phone from all possible locations.
func (o *ShopifyOrder) ResolvePhone() string {
	for _, p := range []string{
		o.Customer.Phone,
		o.Phone,
		o.ShippingAddress.Phone,
		o.BillingAddress.Phone,
	} {
		if p != "" {
			return p
		}
	}
	return ""
}

type ShopifyCustomer struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
}

type ShopifyAddress struct {
	Phone string `json:"phone"`
}

type ShopifyLineItem struct {
	Title    string `json:"title"`
	Quantity int    `json:"quantity"`
}

// ShopifyCheckout represents an abandoned cart / checkout.
type ShopifyCheckout struct {
	ID                   int64           `json:"id"` // Shopify sends as integer
	TotalPrice           string          `json:"total_price"`
	AbandonedCheckoutURL string          `json:"abandoned_checkout_url"`
	Customer             ShopifyCustomer `json:"customer"`
	BillingAddress       ShopifyAddress  `json:"billing_address"`
}

// SendMessageRequest is the request body for the manual send endpoint.
type SendMessageRequest struct {
	Phone   string `json:"phone" binding:"required"`
	Message string `json:"message" binding:"required"`
}

// DashboardStats aggregates key metrics for the dashboard.
type DashboardStats struct {
	TotalMessagesSent int    `json:"total_messages_sent"`
	MessagesToday     int    `json:"messages_today"`
	ActiveAutomations int    `json:"active_automations"`
	TotalContacts     int    `json:"total_contacts"`
	WAStatus          string `json:"wa_status"`
}

// ─── Analytics ────────────────────────────────────────────────────────────────

// DailyStats holds message counts broken down by status for a single calendar day.
type DailyStats struct {
	Date    string `json:"date"`    // "2024-06-13"
	Sent    int    `json:"sent"`
	Failed  int    `json:"failed"`
	Pending int    `json:"pending"`
	Total   int    `json:"total"`
}

// HourlyStats holds sent-message counts per hour of the day (0–23).
type HourlyStats struct {
	Hour  int `json:"hour"`
	Count int `json:"count"`
}

// TriggerStats shows how many messages each automation trigger type produced.
type TriggerStats struct {
	Trigger string `json:"trigger"`
	Count   int    `json:"count"`
}

// AnalyticsData is the single response returned by GET /api/analytics.
type AnalyticsData struct {
	// Lifetime totals
	TotalSent    int     `json:"total_sent"`
	TotalFailed  int     `json:"total_failed"`
	TotalPending int     `json:"total_pending"`
	PendingJobs  int     `json:"pending_jobs"`
	SuccessRate  float64 `json:"success_rate"` // 0.0–100.0

	// Period totals (for the selected window)
	PeriodSent   int `json:"period_sent"`
	PeriodFailed int `json:"period_failed"`

	// Time-series data
	Daily  []DailyStats  `json:"daily"`
	Hourly []HourlyStats `json:"hourly"`

	// Breakdown by trigger
	ByTrigger []TriggerStats `json:"by_trigger"`
}

// AdminProfile is the operator's display name, avatar and login username.
type AdminProfile struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Username  string `json:"username"`
}

// ServerStatusData is the payload for GET /admin/server-status.
type ServerStatusData struct {
	Uptime        string  `json:"uptime"`
	GoVersion     string  `json:"go_version"`
	MemAllocMB    float64 `json:"mem_alloc_mb"`
	MemSysMB      float64 `json:"mem_sys_mb"`
	NumGoroutines int     `json:"num_goroutines"`
	WAConnected   int     `json:"wa_connected"`
	WATotal       int     `json:"wa_total"`
	PendingJobs   int     `json:"pending_jobs"`
	FailedJobs    int     `json:"failed_jobs"`
	DBSizeMB      float64 `json:"db_size_mb"`
	Environment   string  `json:"environment"`
	Healthy       bool    `json:"healthy"`
}

// AdminPlan holds the editable plan configuration for the SaaS operator.
type AdminPlan struct {
	PlanKey         string    `json:"plan_key"`         // "starter" | "pro" | "business"
	DisplayName     string    `json:"display_name"`
	Price           float64   `json:"price"`
	Features        []string  `json:"features"`
	MessageLimit    int       `json:"message_limit"`    // -1 = unlimited
	AutomationLimit int       `json:"automation_limit"` // -1 = unlimited
	TemplateLimit   int       `json:"template_limit"`   // -1 = unlimited
	IsActive        bool      `json:"is_active"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Announcement is an in-app banner shown to all merchants.
type Announcement struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Message   string     `json:"message"`
	Tone      string     `json:"tone"` // "info" | "warning" | "success" | "critical"
	IsActive  bool       `json:"is_active"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ShopStats aggregates stats for a single merchant (admin view).
type ShopStats struct {
	ShopDomain        string `json:"shop_domain"`
	TotalMessages     int    `json:"total_messages"`
	MessagesSent      int    `json:"messages_sent"`
	ActiveContacts    int    `json:"active_contacts"`
	ActiveAutomations int    `json:"active_automations"`
	WAConnected       bool   `json:"wa_connected"`
}

// DefaultAdminPlans returns the hardcoded plan set used as fallback before the DB is seeded.
func DefaultAdminPlans() []AdminPlan {
	return []AdminPlan{
		{PlanKey: "starter", DisplayName: "Starter", Price: 9.99,
			Features:        []string{"500 WhatsApp messages/month", "5 automation rules", "3 message templates", "Email support"},
			MessageLimit: 500, AutomationLimit: 5, TemplateLimit: 3, IsActive: true},
		{PlanKey: "pro", DisplayName: "Pro", Price: 29.99,
			Features:        []string{"2,000 WhatsApp messages/month", "Unlimited automations", "Unlimited templates", "Priority support", "Typing simulation"},
			MessageLimit: 2000, AutomationLimit: -1, TemplateLimit: -1, IsActive: true},
		{PlanKey: "business", DisplayName: "Business", Price: 79.99,
			Features:        []string{"Unlimited messages", "Unlimited automations", "Unlimited templates", "Dedicated support", "Custom integrations"},
			MessageLimit: -1, AutomationLimit: -1, TemplateLimit: -1, IsActive: true},
	}
}

// GlobalStats is the cross-shop overview for the admin dashboard.
type GlobalStats struct {
	TotalShops          int `json:"total_shops"`
	TotalMessages       int `json:"total_messages"`
	MessagesToday       int `json:"messages_today"`
	TotalContacts       int `json:"total_contacts"`
	ActiveAnnouncements int `json:"active_announcements"`
}

// Settings holds the typing-simulation and delivery behaviour config.
type Settings struct {
	TypingSimulationEnabled bool `json:"typing_simulation_enabled"`
	TypingSpeedCPM          int  `json:"typing_speed_cpm"`
	MinTypingSeconds        int  `json:"min_typing_seconds"`
	MaxTypingSeconds        int  `json:"max_typing_seconds"`
	ReadDelayMinSeconds     int  `json:"read_delay_min_seconds"`
	ReadDelayMaxSeconds     int  `json:"read_delay_max_seconds"`
	// Sending controls
	FrequencyCapPerDay int `json:"frequency_cap_per_day"` // max messages/day per customer (0 = unlimited)
	SendingWindowStart int `json:"sending_window_start"`  // hour 0-23; -1 = no restriction
	SendingWindowEnd   int `json:"sending_window_end"`    // hour 0-23; -1 = no restriction
	// Win-back
	WinBackInactiveDays int `json:"win_back_inactive_days"` // days of inactivity before win-back fires (0 = disabled)
}

// KeywordReply maps an incoming text keyword to an auto-reply message.
type KeywordReply struct {
	ID           string    `json:"id"`
	Keyword      string    `json:"keyword"`      // case-insensitive match
	ReplyMessage string    `json:"reply_message"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
}

// BroadcastRequest is the body for the broadcast/campaign endpoint.
type BroadcastRequest struct {
	TemplateID    string `json:"template_id" binding:"required"`
	DelayMinutes  int    `json:"delay_minutes"`   // stagger: each contact offset by this
	FilterOptedIn bool   `json:"filter_opted_in"` // only send to explicitly opted-in contacts
}

// OptOutStat is one day's opt-out count for the trends chart.
type OptOutStat struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}
