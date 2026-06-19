package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/whatpilot/backend/models"
)

// ─── Support Messages ────────────────────────────────────────────────────────

func (db *DB) CreateSupportMessage(shop, subject, message string) error {
	_, err := db.conn.Exec(`
		INSERT INTO support_messages(id,shop_domain,subject,message,created_at)
		VALUES(?,?,?,?,?)`,
		uuid.NewString(), shop, subject, message, time.Now())
	return err
}

func (db *DB) ListSupportMessages() ([]models.SupportMessage, error) {
	rows, err := db.conn.Query(`
		SELECT id,shop_domain,subject,message,reply,status,replied_at,created_at
		FROM support_messages ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.SupportMessage
	for rows.Next() {
		var sm models.SupportMessage
		rows.Scan(&sm.ID, &sm.Shop, &sm.Subject, &sm.Message,
			&sm.Reply, &sm.Status, &sm.RepliedAt, &sm.CreatedAt)
		if sm.Status == "" {
			sm.Status = "open"
		}
		out = append(out, sm)
	}
	return out, nil
}

func (db *DB) ReplyToTicket(id, reply, status string) error {
	now := time.Now()
	_, err := db.conn.Exec(`
		UPDATE support_messages
		SET reply=?, status=?, replied_at=?
		WHERE id=?`,
		reply, status, now, id)
	return err
}

func (db *DB) DeleteSupportTicket(id string) error {
	_, err := db.conn.Exec(`DELETE FROM support_messages WHERE id=?`, id)
	return err
}

// ─── Support Info (Contact Details) ──────────────────────────────────────────

func (db *DB) GetSupportInfo() models.SupportInfo {
	email := db.GetAdminConfigValue("support_email")
	if email == "" {
		email = "support@whatpilot.com"
	}
	phone := db.GetAdminConfigValue("support_phone")
	if phone == "" {
		phone = "+1 (555) 019-2834"
	}
	address := db.GetAdminConfigValue("support_address")
	if address == "" {
		address = "123 App Street, San Francisco, CA 94103"
	}
	return models.SupportInfo{
		Email:   email,
		Phone:   phone,
		Address: address,
	}
}

func (db *DB) SaveSupportInfo(info models.SupportInfo) error {
	if err := db.SetAdminConfigValue("support_email", info.Email); err != nil {
		return err
	}
	if err := db.SetAdminConfigValue("support_phone", info.Phone); err != nil {
		return err
	}
	return db.SetAdminConfigValue("support_address", info.Address)
}

// ─── FAQs ────────────────────────────────────────────────────────────────────

func (db *DB) ListFAQs() ([]models.FAQ, error) {
	rows, err := db.conn.Query(`
		SELECT id,question,answer,sort_order,created_at
		FROM faqs ORDER BY sort_order ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.FAQ
	for rows.Next() {
		var f models.FAQ
		rows.Scan(&f.ID, &f.Question, &f.Answer, &f.SortOrder, &f.CreatedAt)
		out = append(out, f)
	}
	return out, nil
}

func (db *DB) CreateFAQ(q, a string, order int) (*models.FAQ, error) {
	f := models.FAQ{
		ID:        uuid.NewString(),
		Question:  q,
		Answer:    a,
		SortOrder: order,
		CreatedAt: time.Now(),
	}
	_, err := db.conn.Exec(`
		INSERT INTO faqs(id,question,answer,sort_order,created_at)
		VALUES(?,?,?,?,?)`,
		f.ID, f.Question, f.Answer, f.SortOrder, f.CreatedAt)
	return &f, err
}

func (db *DB) UpdateFAQ(id, q, a string, order int) error {
	_, err := db.conn.Exec(`
		UPDATE faqs SET question=?,answer=?,sort_order=? WHERE id=?`,
		q, a, order, id)
	return err
}

func (db *DB) DeleteFAQ(id string) error {
	_, err := db.conn.Exec(`DELETE FROM faqs WHERE id=?`, id)
	return err
}

var defaultFAQs = []struct {
	Question string
	Answer   string
	Order    int
}{
	{
		Question: "How do I connect my WhatsApp account?",
		Answer:   "Go to the WhatsApp tab, click 'Connect WhatsApp', and scan the QR code using the Link a Device option inside your WhatsApp phone settings.",
		Order:    0,
	},
	{
		Question: "Why are my automated messages not sending?",
		Answer:   "Ensure your WhatsApp status is '🟢 Online & Sending'. If it disconnected due to inactivity or phone logouts, scan the QR code again to reconnect.",
		Order:    1,
	},
	{
		Question: "How does Abandoned Cart recovery work?",
		Answer:   "When a customer leaves checkout, Shopify fires a webhook to WhatPilot. We schedule and encrypt the recovery poll/button message based on the delays configured in your Automations tab.",
		Order:    2,
	},
	{
		Question: "Are there any message or template limits?",
		Answer:   "Limits are applied based on your Shopify billing plan tier. You can view features, pricing, and active usage statistics in the Billing tab.",
		Order:    3,
	},
}

func (db *DB) SeedDefaultFAQs() error {
	var count int
	db.conn.QueryRow(`SELECT COUNT(*) FROM faqs`).Scan(&count)
	if count > 0 {
		return nil
	}
	for _, f := range defaultFAQs {
		_, _ = db.CreateFAQ(f.Question, f.Answer, f.Order)
	}
	return nil
}
