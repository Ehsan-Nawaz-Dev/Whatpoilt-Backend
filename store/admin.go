package store

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/whatpilot/backend/models"
)

// ─── Admin: Config (key-value store) ─────────────────────────────────────────

func (db *DB) GetAdminConfigValue(key string) string {
	var v string
	db.conn.QueryRow(`SELECT value FROM admin_config WHERE key=?`, key).Scan(&v)
	return v
}

func (db *DB) SetAdminConfigValue(key, value string) error {
	_, err := db.conn.Exec(`
		INSERT INTO admin_config(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetAdminKey returns the key stored in DB, or "" if not overridden yet.
func (db *DB) GetAdminKey() string {
	return db.GetAdminConfigValue("admin_key")
}

// SetAdminKey persists a new admin key to the DB (overrides the env var).
func (db *DB) SetAdminKey(key string) error {
	return db.SetAdminConfigValue("admin_key", key)
}

// ─── Admin: Job Stats ────────────────────────────────────────────────────────

// GetJobStats returns the count of pending and failed jobs across all shops.
func (db *DB) GetJobStats() (pending, failed int) {
	db.conn.QueryRow(`SELECT COUNT(*) FROM pending_jobs WHERE status='pending'`).Scan(&pending)
	db.conn.QueryRow(`SELECT COUNT(*) FROM pending_jobs WHERE status='failed'`).Scan(&failed)
	return
}

// ─── Admin: Global Stats ──────────────────────────────────────────────────────

func (db *DB) GetGlobalStats() (models.GlobalStats, error) {
	var s models.GlobalStats

	db.conn.QueryRow(`
		SELECT COUNT(DISTINCT shop_domain) FROM (
			SELECT shop_domain FROM message_logs
			UNION SELECT shop_domain FROM contacts
			UNION SELECT shop_domain FROM automations
		)`).Scan(&s.TotalShops)

	db.conn.QueryRow(`SELECT COUNT(*) FROM message_logs`).Scan(&s.TotalMessages)
	db.conn.QueryRow(`SELECT COUNT(*) FROM message_logs WHERE DATE(created_at)=DATE('now')`).Scan(&s.MessagesToday)
	db.conn.QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&s.TotalContacts)
	db.conn.QueryRow(`
		SELECT COUNT(*) FROM announcements
		WHERE is_active=1 AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`,
	).Scan(&s.ActiveAnnouncements)

	return s, nil
}

// ─── Admin: Shops ─────────────────────────────────────────────────────────────

func (db *DB) ListShopsWithStats() ([]models.ShopStats, error) {
	rows, err := db.conn.Query(`
		SELECT
			s.shop_domain,
			COALESCE(ml.total, 0),
			COALESCE(ml.sent, 0),
			COALESCE(c.active_contacts, 0),
			COALESCE(a.active_automations, 0)
		FROM (
			SELECT shop_domain FROM message_logs
			UNION SELECT shop_domain FROM contacts
			UNION SELECT shop_domain FROM automations
		) s
		LEFT JOIN (
			SELECT shop_domain,
				COUNT(*) as total,
				SUM(CASE WHEN status='sent' THEN 1 ELSE 0 END) as sent
			FROM message_logs GROUP BY shop_domain
		) ml ON s.shop_domain = ml.shop_domain
		LEFT JOIN (
			SELECT shop_domain, COUNT(*) as active_contacts
			FROM contacts WHERE opted_out=0 GROUP BY shop_domain
		) c ON s.shop_domain = c.shop_domain
		LEFT JOIN (
			SELECT shop_domain, COUNT(*) as active_automations
			FROM automations WHERE is_active=1 GROUP BY shop_domain
		) a ON s.shop_domain = a.shop_domain
		ORDER BY COALESCE(ml.total, 0) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.ShopStats
	for rows.Next() {
		var s models.ShopStats
		rows.Scan(&s.ShopDomain, &s.TotalMessages, &s.MessagesSent,
			&s.ActiveContacts, &s.ActiveAutomations)
		out = append(out, s)
	}
	return out, nil
}

// ─── Admin: Plans ─────────────────────────────────────────────────────────────

var defaultAdminPlans = []models.AdminPlan{
	{
		PlanKey: "starter", DisplayName: "Starter", Price: 9.99,
		Features:        []string{"500 WhatsApp messages/month", "5 automation rules", "3 message templates", "Email support"},
		MessageLimit:    500, AutomationLimit: 5, TemplateLimit: 3, IsActive: true,
	},
	{
		PlanKey: "pro", DisplayName: "Pro", Price: 29.99,
		Features:        []string{"2,000 WhatsApp messages/month", "Unlimited automations", "Unlimited templates", "Priority support", "Typing simulation"},
		MessageLimit:    2000, AutomationLimit: -1, TemplateLimit: -1, IsActive: true,
	},
	{
		PlanKey: "business", DisplayName: "Business", Price: 79.99,
		Features:        []string{"Unlimited messages", "Unlimited automations", "Unlimited templates", "Dedicated support", "Custom integrations"},
		MessageLimit:    -1, AutomationLimit: -1, TemplateLimit: -1, IsActive: true,
	},
}

func (db *DB) SeedDefaultAdminPlans() error {
	for _, p := range defaultAdminPlans {
		featJSON, _ := json.Marshal(p.Features)
		db.conn.Exec(`
			INSERT OR IGNORE INTO admin_plans
				(plan_key,display_name,price,features,message_limit,automation_limit,template_limit,is_active,updated_at)
			VALUES (?,?,?,?,?,?,?,1,CURRENT_TIMESTAMP)`,
			p.PlanKey, p.DisplayName, p.Price, string(featJSON),
			p.MessageLimit, p.AutomationLimit, p.TemplateLimit)
	}
	return nil
}

func (db *DB) ListAdminPlans() ([]models.AdminPlan, error) {
	rows, err := db.conn.Query(`
		SELECT plan_key,display_name,price,features,message_limit,automation_limit,template_limit,is_active,updated_at
		FROM admin_plans ORDER BY
		CASE plan_key WHEN 'starter' THEN 1 WHEN 'pro' THEN 2 WHEN 'business' THEN 3 ELSE 4 END`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.AdminPlan
	for rows.Next() {
		var p models.AdminPlan
		var active int
		var featJSON string
		rows.Scan(&p.PlanKey, &p.DisplayName, &p.Price, &featJSON,
			&p.MessageLimit, &p.AutomationLimit, &p.TemplateLimit, &active, &p.UpdatedAt)
		p.IsActive = active == 1
		json.Unmarshal([]byte(featJSON), &p.Features)
		if p.Features == nil {
			p.Features = []string{}
		}
		out = append(out, p)
	}
	return out, nil
}

func (db *DB) UpsertAdminPlan(p models.AdminPlan) error {
	if p.Features == nil {
		p.Features = []string{}
	}
	featJSON, _ := json.Marshal(p.Features)
	active := 0
	if p.IsActive {
		active = 1
	}
	_, err := db.conn.Exec(`
		INSERT INTO admin_plans
			(plan_key,display_name,price,features,message_limit,automation_limit,template_limit,is_active,updated_at)
		VALUES (?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(plan_key) DO UPDATE SET
			display_name=excluded.display_name,
			price=excluded.price,
			features=excluded.features,
			message_limit=excluded.message_limit,
			automation_limit=excluded.automation_limit,
			template_limit=excluded.template_limit,
			is_active=excluded.is_active,
			updated_at=CURRENT_TIMESTAMP`,
		p.PlanKey, p.DisplayName, p.Price, string(featJSON),
		p.MessageLimit, p.AutomationLimit, p.TemplateLimit, active)
	return err
}

func (db *DB) DeleteAdminPlan(key string) error {
	_, err := db.conn.Exec(`DELETE FROM admin_plans WHERE plan_key=?`, key)
	return err
}

// ─── Admin: Announcements ─────────────────────────────────────────────────────

func (db *DB) ListAnnouncements() ([]models.Announcement, error) {
	rows, err := db.conn.Query(`
		SELECT id,title,message,tone,is_active,expires_at,created_at,updated_at
		FROM announcements ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnouncements(rows)
}

func (db *DB) GetActiveAnnouncements() ([]models.Announcement, error) {
	rows, err := db.conn.Query(`
		SELECT id,title,message,tone,is_active,expires_at,created_at,updated_at
		FROM announcements
		WHERE is_active=1 AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnnouncements(rows)
}

func scanAnnouncements(rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}) ([]models.Announcement, error) {
	var out []models.Announcement
	for rows.Next() {
		var a models.Announcement
		var active int
		rows.Scan(&a.ID, &a.Title, &a.Message, &a.Tone, &active, &a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt)
		a.IsActive = active == 1
		out = append(out, a)
	}
	return out, nil
}

func (db *DB) CreateAnnouncement(title, message, tone string, expiresAt *time.Time) (*models.Announcement, error) {
	if tone == "" {
		tone = "info"
	}
	a := models.Announcement{
		ID: uuid.NewString(), Title: title, Message: message, Tone: tone,
		IsActive: true, ExpiresAt: expiresAt,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	_, err := db.conn.Exec(`
		INSERT INTO announcements(id,title,message,tone,is_active,expires_at,created_at,updated_at)
		VALUES (?,?,?,?,1,?,?,?)`,
		a.ID, a.Title, a.Message, a.Tone, a.ExpiresAt, a.CreatedAt, a.UpdatedAt)
	return &a, err
}

func (db *DB) UpdateAnnouncement(id, title, message, tone string, isActive bool, expiresAt *time.Time) error {
	if tone == "" {
		tone = "info"
	}
	active := 0
	if isActive {
		active = 1
	}
	_, err := db.conn.Exec(`
		UPDATE announcements
		SET title=?,message=?,tone=?,is_active=?,expires_at=?,updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		title, message, tone, active, expiresAt, id)
	return err
}

func (db *DB) DeleteAnnouncement(id string) error {
	_, err := db.conn.Exec(`DELETE FROM announcements WHERE id=?`, id)
	return err
}

// ─── Per-shop Order Tags ──────────────────────────────────────────────────────
// Stored in admin_config with key = "shop:{domain}:{tagKey}" so no new table is needed.

func (db *DB) GetShopOrderTag(shop, tagKey string) string {
	return db.GetAdminConfigValue("shop:" + shop + ":" + tagKey)
}

func (db *DB) SetShopOrderTag(shop, tagKey, value string) error {
	return db.SetAdminConfigValue("shop:"+shop+":"+tagKey, value)
}
