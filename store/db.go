package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/whatpilot/backend/models"
	_ "github.com/mattn/go-sqlite3"
)

type DB struct{ conn *sql.DB }

func New(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL", path))
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1) // SQLite WAL: one writer at a time
	db := &DB{conn: conn}
	return db, db.migrate()
}

func (db *DB) Close() error { return db.conn.Close() }

func (db *DB) migrate() error {
	stmts := []string{
		// ── core tables ──────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS templates (
			id TEXT PRIMARY KEY, shop_domain TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL, content TEXT NOT NULL,
			message_type TEXT NOT NULL DEFAULT 'text',
			options TEXT NOT NULL DEFAULT '[]',
			is_active INTEGER DEFAULT 1,
			is_default INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		`CREATE TABLE IF NOT EXISTS automations (
			id TEXT PRIMARY KEY, shop_domain TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL, trigger_type TEXT NOT NULL,
			template_id TEXT, is_active INTEGER DEFAULT 1,
			delay_minutes INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		`CREATE TABLE IF NOT EXISTS contacts (
			id TEXT PRIMARY KEY, shop_domain TEXT NOT NULL DEFAULT '',
			name TEXT, phone TEXT NOT NULL,
			shopify_customer_id TEXT,
			opted_out INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(shop_domain, phone))`,

		`CREATE TABLE IF NOT EXISTS message_logs (
			id TEXT PRIMARY KEY, shop_domain TEXT NOT NULL DEFAULT '',
			automation_id TEXT, contact_phone TEXT NOT NULL,
			template_id TEXT, content TEXT NOT NULL,
			status TEXT DEFAULT 'pending', error TEXT,
			sent_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		`CREATE TABLE IF NOT EXISTS settings (
			shop_domain TEXT PRIMARY KEY,
			typing_simulation_enabled INTEGER DEFAULT 1,
			typing_speed_cpm INTEGER DEFAULT 220,
			min_typing_seconds INTEGER DEFAULT 2,
			max_typing_seconds INTEGER DEFAULT 14,
			read_delay_min_seconds INTEGER DEFAULT 1,
			read_delay_max_seconds INTEGER DEFAULT 5,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		// ── persistent job queue ──────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS pending_jobs (
			id TEXT PRIMARY KEY, shop_domain TEXT NOT NULL,
			phone TEXT NOT NULL, message TEXT NOT NULL,
			message_type TEXT NOT NULL DEFAULT 'text',
			options TEXT NOT NULL DEFAULT '[]',
			automation_id TEXT DEFAULT '', template_id TEXT DEFAULT '',
			scheduled_at DATETIME NOT NULL,
			status TEXT DEFAULT 'pending',
			attempts INTEGER DEFAULT 0, max_attempts INTEGER DEFAULT 3,
			error TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		// ── 24-hour reply reminders ──────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS reply_reminders (
			id TEXT PRIMARY KEY,
			shop_domain TEXT NOT NULL,
			phone TEXT NOT NULL,
			message TEXT NOT NULL,
			message_type TEXT NOT NULL DEFAULT 'text',
			options TEXT NOT NULL DEFAULT '[]',
			original_sent_at DATETIME NOT NULL,
			send_at DATETIME NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		`CREATE INDEX IF NOT EXISTS idx_reply_reminders_shop_status ON reply_reminders(shop_domain, status, send_at)`,

		// ── Shopify OAuth sessions (replaces Prisma/PostgreSQL) ─────────────
		`CREATE TABLE IF NOT EXISTS shopify_sessions (
			id         TEXT PRIMARY KEY,
			shop       TEXT NOT NULL,
			data       TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		`CREATE INDEX IF NOT EXISTS idx_sessions_shop ON shopify_sessions(shop)`,

		// ── shop access tokens (for Shopify API calls e.g. order tagging) ──
		`CREATE TABLE IF NOT EXISTS shop_tokens (
			shop_domain  TEXT PRIMARY KEY,
			access_token TEXT NOT NULL,
			updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		// ── admin config (key-value store for profile + overrideable key) ───
		`CREATE TABLE IF NOT EXISTS admin_config (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '')`,

		// ── admin tables ─────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS admin_plans (
			plan_key TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			price REAL NOT NULL DEFAULT 0,
			features TEXT NOT NULL DEFAULT '[]',
			message_limit INTEGER NOT NULL DEFAULT -1,
			automation_limit INTEGER NOT NULL DEFAULT -1,
			template_limit INTEGER NOT NULL DEFAULT -1,
			is_active INTEGER NOT NULL DEFAULT 1,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		`CREATE TABLE IF NOT EXISTS announcements (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			message TEXT NOT NULL,
			tone TEXT NOT NULL DEFAULT 'info',
			is_active INTEGER NOT NULL DEFAULT 1,
			expires_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		// ── pending confirmation replies (sent after customer confirms poll) ─
		`CREATE TABLE IF NOT EXISTS pending_confirmations (
			id TEXT PRIMARY KEY,
			shop_domain TEXT NOT NULL,
			phone TEXT NOT NULL,
			reply_message TEXT NOT NULL,
			reply_type TEXT NOT NULL DEFAULT 'text',
			reply_options TEXT NOT NULL DEFAULT '[]',
			reply_no_message TEXT NOT NULL DEFAULT '',
			reply_no_type TEXT NOT NULL DEFAULT 'text',
			reply_no_options TEXT NOT NULL DEFAULT '[]',
			reply_help_message TEXT NOT NULL DEFAULT '',
			reply_help_type TEXT NOT NULL DEFAULT 'text',
			reply_help_options TEXT NOT NULL DEFAULT '[]',
			trigger_option TEXT NOT NULL DEFAULT '',
			no_option TEXT NOT NULL DEFAULT '',
			help_option TEXT NOT NULL DEFAULT '',
			step2_yes_message TEXT NOT NULL DEFAULT '',
			step2_no_message TEXT NOT NULL DEFAULT '',
			step2_help_message TEXT NOT NULL DEFAULT '',
			expires_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(shop_domain, phone))`,

		// ── keyword auto-reply rules ─────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS keyword_replies (
			id TEXT PRIMARY KEY,
			shop_domain TEXT NOT NULL,
			keyword TEXT NOT NULL,
			reply_message TEXT NOT NULL,
			is_active INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(shop_domain, keyword))`,

		`CREATE TABLE IF NOT EXISTS support_messages (
			id TEXT PRIMARY KEY,
			shop_domain TEXT NOT NULL DEFAULT '',
			subject TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		`CREATE TABLE IF NOT EXISTS faqs (
			id TEXT PRIMARY KEY,
			question TEXT NOT NULL,
			answer TEXT NOT NULL,
			sort_order INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,

		// ── indexes — every query filters by shop_domain ───────────────────
		`CREATE INDEX IF NOT EXISTS idx_templates_shop   ON templates(shop_domain)`,
		`CREATE INDEX IF NOT EXISTS idx_automations_shop ON automations(shop_domain, trigger_type, is_active)`,
		`CREATE INDEX IF NOT EXISTS idx_contacts_shop    ON contacts(shop_domain)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_shop        ON message_logs(shop_domain, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_ready       ON pending_jobs(status, scheduled_at) WHERE status='pending'`,
		`CREATE INDEX IF NOT EXISTS idx_keywords_shop    ON keyword_replies(shop_domain)`,

		// ── idempotent column additions for pre-existing DBs ─────────────────
		`ALTER TABLE templates    ADD COLUMN shop_domain TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE templates    ADD COLUMN message_type TEXT NOT NULL DEFAULT 'text'`,
		`ALTER TABLE templates    ADD COLUMN options TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE templates    ADD COLUMN is_default INTEGER DEFAULT 0`,
		`ALTER TABLE automations  ADD COLUMN shop_domain TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE message_logs ADD COLUMN shop_domain TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE contacts     ADD COLUMN opted_out INTEGER DEFAULT 0`,
		`ALTER TABLE contacts     ADD COLUMN last_order_at DATETIME`,
		`ALTER TABLE contacts     ADD COLUMN opted_out_at DATETIME`,
		`ALTER TABLE pending_jobs ADD COLUMN message_type TEXT NOT NULL DEFAULT 'text'`,
		`ALTER TABLE pending_jobs ADD COLUMN options TEXT NOT NULL DEFAULT '[]'`,
		// order_id + tag_on_send let the worker apply the trigger's Shopify tag
		// only once the WhatsApp message is actually sent (not on webhook receipt).
		`ALTER TABLE pending_jobs ADD COLUMN order_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE pending_jobs ADD COLUMN tag_on_send TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN trigger_option TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN reply_no_message TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN reply_no_type TEXT NOT NULL DEFAULT 'text'`,
		`ALTER TABLE pending_confirmations ADD COLUMN reply_no_options TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE pending_confirmations ADD COLUMN reply_help_message TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN reply_help_type TEXT NOT NULL DEFAULT 'text'`,
		`ALTER TABLE pending_confirmations ADD COLUMN reply_help_options TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE pending_confirmations ADD COLUMN no_option TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN help_option TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN step2_yes_message TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN step2_no_message TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN step2_help_message TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE settings ADD COLUMN frequency_cap_per_day INTEGER DEFAULT 0`,
		`ALTER TABLE settings ADD COLUMN sending_window_start INTEGER DEFAULT -1`,
		`ALTER TABLE settings ADD COLUMN sending_window_end INTEGER DEFAULT -1`,
		`ALTER TABLE settings ADD COLUMN win_back_inactive_days INTEGER DEFAULT 0`,
		`ALTER TABLE settings ADD COLUMN plan_key TEXT DEFAULT 'free'`,
		`ALTER TABLE settings ADD COLUMN plan_name TEXT DEFAULT 'Free'`,
		`ALTER TABLE settings ADD COLUMN message_limit INTEGER DEFAULT 150`,
		`ALTER TABLE settings ADD COLUMN messages_sent_this_month INTEGER DEFAULT 0`,
		// plan_selected: 1 once the merchant has explicitly picked a plan (free or
		// paid) after install — gates entry to the app until a plan is chosen.
		`ALTER TABLE settings ADD COLUMN plan_selected INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE settings ADD COLUMN limit_reset_at DATETIME`,
		`ALTER TABLE settings ADD COLUMN subscription_line_item_id TEXT DEFAULT ''`,
		`ALTER TABLE support_messages ADD COLUMN reply TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE support_messages ADD COLUMN status TEXT NOT NULL DEFAULT 'open'`,
		`ALTER TABLE support_messages ADD COLUMN replied_at DATETIME`,
		`ALTER TABLE pending_confirmations ADD COLUMN order_id INTEGER DEFAULT 0`,
		`ALTER TABLE pending_confirmations ADD COLUMN tag_on_yes TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pending_confirmations ADD COLUMN tag_on_no TEXT NOT NULL DEFAULT ''`,

		// ── per-shop Shopify re-authorization state (multi-tenant SaaS) ──────
		// When a shop's access token is rejected by Shopify (legacy non-expiring
		// token or 401), the backend flags it here so the frontend can show a
		// "Reconnect Shopify" banner for that merchant only.
		`ALTER TABLE shop_tokens ADD COLUMN needs_reauth INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE shop_tokens ADD COLUMN reauth_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE shop_tokens ADD COLUMN reauth_detected_at DATETIME`,
	}

	for _, s := range stmts {
		db.conn.Exec(s) // ignore "already exists" / "duplicate column" errors
	}

	// ── Delete Order Cancellation Reply template and automation if they exist
	db.conn.Exec(`DELETE FROM templates WHERE name = 'Order Cancellation Reply'`)
	db.conn.Exec(`DELETE FROM automations WHERE name = 'Order Cancellation Reply'`)

	// ── Healing: Auto-resolve automations missing templates or pointing to dead IDs
	badAutoRows, err := db.conn.Query(`
		SELECT a.id, a.shop_domain, a.name 
		FROM automations a 
		LEFT JOIN templates t ON a.template_id = t.id 
		WHERE a.template_id IS NULL OR a.template_id = '' OR t.id IS NULL
	`)
	if err == nil {
		type badAuto struct {
			ID   string
			Shop string
			Name string
		}
		var badAutos []badAuto
		for badAutoRows.Next() {
			var ba badAuto
			if err := badAutoRows.Scan(&ba.ID, &ba.Shop, &ba.Name); err == nil {
				badAutos = append(badAutos, ba)
			}
		}
		badAutoRows.Close()

		for _, ba := range badAutos {
			var templateName string
			for _, def := range defaultAutomationDefs {
				if def.Name == ba.Name {
					templateName = def.TemplateName
					break
				}
			}
			if templateName != "" {
				var templateID string
				db.conn.QueryRow(
					`SELECT id FROM templates WHERE shop_domain=? AND name=? AND is_default=1`,
					ba.Shop, templateName,
				).Scan(&templateID)
				if templateID != "" {
					db.conn.Exec(`UPDATE automations SET template_id = ? WHERE id = ?`, templateID, ba.ID)
				}
			}
		}
	}

	// ── Deduplicate templates ──────────────────────────────────────────────
	type tempKey struct {
		Shop string
		Name string
	}
	type tempRow struct {
		ID        string
		IsDefault int
		CreatedAt string
	}

	rows, err := db.conn.Query(`SELECT id, shop_domain, name, is_default, created_at FROM templates`)
	if err == nil {
		templatesByKey := make(map[tempKey][]tempRow)
		for rows.Next() {
			var r tempRow
			var shop, name string
			if err := rows.Scan(&r.ID, &shop, &name, &r.IsDefault, &r.CreatedAt); err == nil {
				k := tempKey{Shop: shop, Name: name}
				templatesByKey[k] = append(templatesByKey[k], r)
			}
		}
		rows.Close()

		for _, list := range templatesByKey {
			if len(list) > 1 {
				// Sort to find the best master template
				// Priority: is_default=1 first, then oldest created_at, then ID
				sort.Slice(list, func(i, j int) bool {
					if list[i].IsDefault != list[j].IsDefault {
						return list[i].IsDefault > list[j].IsDefault
					}
					if list[i].CreatedAt != list[j].CreatedAt {
						return list[i].CreatedAt < list[j].CreatedAt
					}
					return list[i].ID < list[j].ID
				})

				masterID := list[0].ID
				for i := 1; i < len(list); i++ {
					dupID := list[i].ID
					// Update automations referencing this duplicate
					db.conn.Exec(`UPDATE automations SET template_id = ? WHERE template_id = ?`, masterID, dupID)
					// Delete duplicate template
					db.conn.Exec(`DELETE FROM templates WHERE id = ?`, dupID)
				}
			}
		}
	}

	// ── Deduplicate automations ────────────────────────────────────────────
	type autoKey struct {
		Shop string
		Name string
	}
	type autoRow struct {
		ID        string
		CreatedAt string
	}

	arows, err := db.conn.Query(`SELECT id, shop_domain, name, created_at FROM automations`)
	if err == nil {
		automationsByKey := make(map[autoKey][]autoRow)
		for arows.Next() {
			var r autoRow
			var shop, name string
			if err := arows.Scan(&r.ID, &shop, &name, &r.CreatedAt); err == nil {
				k := autoKey{Shop: shop, Name: name}
				automationsByKey[k] = append(automationsByKey[k], r)
			}
		}
		arows.Close()

		for _, list := range automationsByKey {
			if len(list) > 1 {
				// Sort by oldest created_at first, then ID
				sort.Slice(list, func(i, j int) bool {
					if list[i].CreatedAt != list[j].CreatedAt {
						return list[i].CreatedAt < list[j].CreatedAt
					}
					return list[i].ID < list[j].ID
				})

				for i := 1; i < len(list); i++ {
					db.conn.Exec(`DELETE FROM automations WHERE id = ?`, list[i].ID)
				}
			}
		}
	}

	return nil
}

// ─── Templates ───────────────────────────────────────────────────────────────

func scanTemplate(row interface{ Scan(...any) error }) (models.Template, error) {
	var t models.Template
	var active, isDefault int
	var optJSON string
	err := row.Scan(&t.ID, &t.Name, &t.Content, &optJSON,
		&active, &isDefault, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	t.IsActive = active == 1
	t.IsDefault = isDefault == 1
	// message_type is stored in content prefix notation — use the DB column
	// (see CreateTemplate for the separate column)
	json.Unmarshal([]byte(optJSON), &t.Options)
	if t.Options == nil {
		t.Options = []string{}
	}
	return t, nil
}

func scanTemplateWithType(row interface{ Scan(...any) error }) (models.Template, error) {
	var t models.Template
	var active, isDefault int
	var optJSON, msgType string
	err := row.Scan(&t.ID, &t.Name, &t.Content, &msgType, &optJSON,
		&active, &isDefault, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	t.IsActive = active == 1
	t.IsDefault = isDefault == 1
	t.MessageType = models.MessageType(msgType)
	json.Unmarshal([]byte(optJSON), &t.Options)
	if t.Options == nil {
		t.Options = []string{}
	}
	return t, nil
}

func (db *DB) ListTemplates(shop string) ([]models.Template, error) {
	rows, err := db.conn.Query(
		`SELECT id,name,content,message_type,options,is_active,is_default,created_at,updated_at
		 FROM templates WHERE shop_domain=? ORDER BY is_default DESC, created_at DESC`, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Template
	for rows.Next() {
		t, err := scanTemplateWithType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (db *DB) GetTemplate(id, shop string) (*models.Template, error) {
	row := db.conn.QueryRow(
		`SELECT id,name,content,message_type,options,is_active,is_default,created_at,updated_at
		 FROM templates WHERE id=? AND shop_domain=?`, id, shop)
	t, err := scanTemplateWithType(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (db *DB) GetTemplateByName(name, shop string) (*models.Template, error) {
	row := db.conn.QueryRow(
		`SELECT id,name,content,message_type,options,is_active,is_default,created_at,updated_at
		 FROM templates WHERE name=? AND shop_domain=?`, name, shop)
	t, err := scanTemplateWithType(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (db *DB) CreateTemplate(shop, name, content string, msgType models.MessageType, options []string) (*models.Template, error) {
	if msgType == "" {
		msgType = models.MessageTypeText
	}
	if options == nil {
		options = []string{}
	}
	optJSON, _ := json.Marshal(options)
	t := models.Template{
		ID: uuid.NewString(), Name: name, Content: content,
		MessageType: msgType, Options: options,
		IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	_, err := db.conn.Exec(
		`INSERT INTO templates(id,shop_domain,name,content,message_type,options,is_active,is_default,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,1,0,?,?)`,
		t.ID, shop, t.Name, t.Content, string(t.MessageType), string(optJSON), t.CreatedAt, t.UpdatedAt)
	return &t, err
}

func (db *DB) UpdateTemplate(id, shop, name, content string, msgType models.MessageType, options []string) error {
	if options == nil {
		options = []string{}
	}
	optJSON, _ := json.Marshal(options)
	_, err := db.conn.Exec(
		`UPDATE templates SET name=?,content=?,message_type=?,options=?,updated_at=?
		 WHERE id=? AND shop_domain=?`,
		name, content, string(msgType), string(optJSON), time.Now(), id, shop)
	return err
}

func (db *DB) ToggleTemplate(id, shop string, isActive bool) error {
	v := 0
	if isActive {
		v = 1
	}
	_, err := db.conn.Exec(
		`UPDATE templates SET is_active=?,updated_at=? WHERE id=? AND shop_domain=?`,
		v, time.Now(), id, shop)
	return err
}

func (db *DB) DeleteTemplate(id, shop string) error {
	_, err := db.conn.Exec(`DELETE FROM templates WHERE id=? AND shop_domain=?`, id, shop)
	return err
}

// SeedDefaultTemplates inserts the built-in templates for a shop if they
// do not already exist (idempotent — safe to call multiple times).
func (db *DB) SeedDefaultTemplates(shop string) error {
	for _, tmpl := range models.DefaultTemplates {
		var count int
		db.conn.QueryRow(
			`SELECT COUNT(*) FROM templates WHERE shop_domain=? AND name=? AND is_default=1`,
			shop, tmpl.Name,
		).Scan(&count)
		if count > 0 {
			continue // already seeded
		}
		optJSON, _ := json.Marshal(tmpl.Options)
		if tmpl.Options == nil {
			optJSON = []byte("[]")
		}
		db.conn.Exec(
			`INSERT INTO templates
			 (id,shop_domain,name,content,message_type,options,is_active,is_default,created_at,updated_at)
			 VALUES(?,?,?,?,?,?,1,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			uuid.NewString(), shop, tmpl.Name, tmpl.Content,
			string(tmpl.MessageType), string(optJSON),
		)
	}
	return nil
}

// DefaultTemplatesSeeded returns true if the shop already has default templates.
func (db *DB) DefaultTemplatesSeeded(shop string) bool {
	var count int
	db.conn.QueryRow(
		`SELECT COUNT(*) FROM templates WHERE shop_domain=? AND is_default=1`, shop,
	).Scan(&count)
	return count > 0
}

// ─── Automations ─────────────────────────────────────────────────────────────

func (db *DB) ListAutomations(shop string) ([]models.Automation, error) {
	rows, err := db.conn.Query(
		`SELECT id,name,trigger_type,COALESCE(template_id,''),is_active,delay_minutes,created_at,updated_at
		 FROM automations WHERE shop_domain=? ORDER BY created_at DESC`, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Automation
	for rows.Next() {
		var a models.Automation
		var active int
		rows.Scan(&a.ID, &a.Name, &a.TriggerType, &a.TemplateID, &active,
			&a.DelayMinutes, &a.CreatedAt, &a.UpdatedAt)
		a.IsActive = active == 1
		out = append(out, a)
	}
	return out, nil
}

func (db *DB) GetAutomationsByTrigger(shop string, tt models.TriggerType) ([]models.Automation, error) {
	rows, err := db.conn.Query(
		`SELECT id,name,trigger_type,COALESCE(template_id,''),is_active,delay_minutes,created_at,updated_at
		 FROM automations WHERE shop_domain=? AND trigger_type=? AND is_active=1
		 ORDER BY created_at ASC`, shop, tt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Automation
	for rows.Next() {
		var a models.Automation
		var active int
		rows.Scan(&a.ID, &a.Name, &a.TriggerType, &a.TemplateID, &active,
			&a.DelayMinutes, &a.CreatedAt, &a.UpdatedAt)
		a.IsActive = active == 1
		out = append(out, a)
	}
	return out, nil
}

func (db *DB) CreateAutomation(shop, name string, tt models.TriggerType, templateID string, delay int) (*models.Automation, error) {
	a := models.Automation{ID: uuid.NewString(), Name: name, TriggerType: tt,
		TemplateID: templateID, IsActive: true, DelayMinutes: delay,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_, err := db.conn.Exec(
		`INSERT INTO automations(id,shop_domain,name,trigger_type,template_id,is_active,delay_minutes,created_at,updated_at)
		 VALUES(?,?,?,?,?,1,?,?,?)`,
		a.ID, shop, a.Name, a.TriggerType, a.TemplateID, a.DelayMinutes, a.CreatedAt, a.UpdatedAt)
	return &a, err
}

func (db *DB) UpdateAutomation(id, shop, name string, isActive bool, delay int, templateID string) error {
	v := 0
	if isActive {
		v = 1
	}
	_, err := db.conn.Exec(
		`UPDATE automations SET name=?,is_active=?,delay_minutes=?,template_id=?,updated_at=?
		 WHERE id=? AND shop_domain=?`, name, v, delay, templateID, time.Now(), id, shop)
	return err
}

func (db *DB) DeleteAutomation(id, shop string) error {
	_, err := db.conn.Exec(`DELETE FROM automations WHERE id=? AND shop_domain=?`, id, shop)
	return err
}

func (db *DB) ToggleAutomation(id, shop string) error {
	_, err := db.conn.Exec(
		`UPDATE automations SET is_active = CASE WHEN is_active=1 THEN 0 ELSE 1 END, updated_at=?
		 WHERE id=? AND shop_domain=?`, time.Now(), id, shop)
	return err
}

func (db *DB) UpdateAutomationTemplate(id, shop, templateID string) error {
	_, err := db.conn.Exec(
		`UPDATE automations SET template_id=?, updated_at=? WHERE id=? AND shop_domain=?`,
		templateID, time.Now(), id, shop)
	return err
}

// defaultAutomationDefs are the built-in automations seeded for every shop.
// DelayMinutes is the default delay; merchants can change it via the dashboard.
var defaultAutomationDefs = []struct {
	Name         string
	TriggerType  models.TriggerType
	TemplateName string
	DelayMinutes int
}{
	// Order Created (6)
	{"Customer Order Confirmation", models.TriggerOrderCreated,   "Order Confirmation",  0},
	{"Order Processing Update",     models.TriggerOrderCreated,   "Order Processing",    0},
	{"Quick Order Thanks",          models.TriggerOrderCreated,   "Quick Order Thanks",  0},
	{"Post-Confirmation Reply",     models.TriggerOrderCreated,   "Post-Confirmation Reply", 0},
	{"Customer Help Reply",         models.TriggerOrderCreated,   "Customer Help Reply", 0},
	{"Admin New Order Alert",       models.TriggerOrderCreated,   "Admin Order Alert",   0},
	{"Upsell After Purchase",       models.TriggerOrderCreated,   "Upsell Offer",        1440}, // 24 h
	// Order Fulfilled (7)
	{"Shipping Notification",       models.TriggerOrderFulfilled, "Shipping Alert",               0},
	{"Shipping Confirmation",       models.TriggerOrderFulfilled, "Shipping Confirmation",        0},
	{"Delivery Confirmation",       models.TriggerOrderFulfilled, "Delivery Alert",               0},
	{"Post-Purchase Review",        models.TriggerOrderFulfilled, "Post-Purchase Review",         0},
	{"Admin Order Confirmed Alert", models.TriggerOrderFulfilled, "Admin Order Confirmed Alert",  0},
	{"Review Request",              models.TriggerOrderFulfilled, "Review Request",               4320}, // 3 days
	{"Delivery Follow-Up",         models.TriggerOrderFulfilled, "Delivery Follow-Up",           7200}, // 5 days
	// Order Cancelled (5)
	{"Cancellation Verification",   models.TriggerOrderCancelled, "Cancellation Verification", 0},
	{"Cancellation Notice",         models.TriggerOrderCancelled, "Order Cancellation",        0},
	{"Refund Initiated",            models.TriggerOrderCancelled, "Refund Initiated",          0},
	{"Win-Back Offer",              models.TriggerOrderCancelled, "Win-Back Offer",            0},
	{"Admin Cancellation Alert",    models.TriggerOrderCancelled, "Admin Cancellation Alert",  0},
	// Abandoned Cart (3)
	{"Abandoned Cart Recovery",     models.TriggerAbandonedCart,  "Abandoned Cart Recovery", 0},
	{"Cart Save Reminder",          models.TriggerAbandonedCart,  "Cart Save Reminder",      0},
	{"Cart Discount Offer",         models.TriggerAbandonedCart,  "Cart Discount Offer",     0},
	// COD Order (4)
	{"COD Delivery Confirmation",   models.TriggerCODOrder,       "COD Confirmation",       0},
	{"COD Post-Confirmation Reply", models.TriggerCODOrder,       "COD Confirmation Reply", 0},
	{"COD Cancellation Reply",      models.TriggerCODOrder,       "COD Cancellation Reply", 0},
	{"COD Help Reply",              models.TriggerCODOrder,       "COD Help Reply",         0},
	// Payment Pending (1)
	{"Payment Reminder",            models.TriggerPaymentPending, "Payment Reminder",        0},
	// Refund Created (1)
	{"Refund Status Update",        models.TriggerRefundCreated,  "Refund Status Update",    0},
	// Welcome (1)
	{"Welcome Series",              models.TriggerWelcome,        "Welcome Message",         0},
	// Win-Back (1)
	{"Win-Back Campaign",           models.TriggerWinBack,        "Win-Back Message",        0},
	// Bank Transfer (1)
	{"Bank Transfer Instructions",  models.TriggerBankTransfer,   "Bank Transfer Instructions", 0},
	// Order Reminder (1)
	{"Order Confirmation Reminder", models.TriggerOrderReminder,  "Order Confirmation Reminder", 0},
}

// SeedAutomations creates default automations for a shop if they don't exist.
// Templates must already be seeded before calling this.
func (db *DB) SeedAutomations(shop string) error {
	for _, def := range defaultAutomationDefs {
		var count int
		db.conn.QueryRow(
			`SELECT COUNT(*) FROM automations WHERE shop_domain=? AND name=?`,
			shop, def.Name,
		).Scan(&count)
		if count > 0 {
			continue
		}
		var templateID string
		db.conn.QueryRow(
			`SELECT id FROM templates WHERE shop_domain=? AND name=? AND is_default=1`,
			shop, def.TemplateName,
		).Scan(&templateID)
		if templateID == "" {
			// Seed default templates to make sure it's created
			db.SeedDefaultTemplates(shop)
			db.conn.QueryRow(
				`SELECT id FROM templates WHERE shop_domain=? AND name=? AND is_default=1`,
				shop, def.TemplateName,
			).Scan(&templateID)
		}
		db.conn.Exec(
			`INSERT INTO automations
			 (id,shop_domain,name,trigger_type,template_id,is_active,delay_minutes,created_at,updated_at)
			 VALUES(?,?,?,?,?,0,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			uuid.NewString(), shop, def.Name, string(def.TriggerType), templateID, def.DelayMinutes,
		)
	}
	return nil
}

// DefaultAutomationsSeeded returns true when the shop has all default automation rows.
func (db *DB) DefaultAutomationsSeeded(shop string) bool {
	var count int
	db.conn.QueryRow(
		`SELECT COUNT(*) FROM automations WHERE shop_domain=?`, shop,
	).Scan(&count)
	return count >= len(defaultAutomationDefs)
}

// ─── Contacts ────────────────────────────────────────────────────────────────

func (db *DB) ListContacts(shop string) ([]models.Contact, error) {
	rows, err := db.conn.Query(
		`SELECT id,COALESCE(name,''),phone,COALESCE(shopify_customer_id,''),opted_out,created_at
		 FROM contacts WHERE shop_domain=? ORDER BY created_at DESC`, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Contact
	for rows.Next() {
		var c models.Contact
		var optedOut int
		rows.Scan(&c.ID, &c.Name, &c.Phone, &c.ShopifyCustomerID, &optedOut, &c.CreatedAt)
		c.OptedOut = optedOut == 1
		out = append(out, c)
	}
	return out, nil
}

func (db *DB) UpsertContact(shop, name, phone, shopifyID string) (*models.Contact, error) {
	id := uuid.NewString()
	_, err := db.conn.Exec(
		`INSERT INTO contacts(id,shop_domain,name,phone,shopify_customer_id,created_at)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(shop_domain,phone)
		 DO UPDATE SET name=excluded.name, shopify_customer_id=excluded.shopify_customer_id`,
		id, shop, name, phone, shopifyID, time.Now())
	if err != nil {
		return nil, err
	}
	var c models.Contact
	var optedOut int
	db.conn.QueryRow(
		`SELECT id,COALESCE(name,''),phone,COALESCE(shopify_customer_id,''),opted_out,created_at
		 FROM contacts WHERE shop_domain=? AND phone=?`, shop, phone).
		Scan(&c.ID, &c.Name, &c.Phone, &c.ShopifyCustomerID, &optedOut, &c.CreatedAt)
	c.OptedOut = optedOut == 1
	return &c, nil
}

func (db *DB) SetContactOptOut(shop, phone string, out bool) error {
	v := 0
	if out {
		v = 1
	}
	var err error
	if out {
		_, err = db.conn.Exec(
			`UPDATE contacts SET opted_out=?,opted_out_at=CURRENT_TIMESTAMP WHERE shop_domain=? AND phone=?`,
			v, shop, phone)
	} else {
		_, err = db.conn.Exec(
			`UPDATE contacts SET opted_out=?,opted_out_at=NULL WHERE shop_domain=? AND phone=?`,
			v, shop, phone)
	}
	return err
}

func (db *DB) IsOptedOut(shop, phone string) bool {
	var v int
	db.conn.QueryRow(`SELECT opted_out FROM contacts WHERE shop_domain=? AND phone=?`, shop, phone).Scan(&v)
	return v == 1
}

func (db *DB) DeleteContact(id, shop string) error {
	_, err := db.conn.Exec(`DELETE FROM contacts WHERE id=? AND shop_domain=?`, id, shop)
	return err
}

// ─── Message Logs ─────────────────────────────────────────────────────────────

func (db *DB) CreateMessageLog(shop, automationID, phone, templateID, content string) (*models.MessageLog, error) {
	l := models.MessageLog{ID: uuid.NewString(), AutomationID: automationID,
		ContactPhone: phone, TemplateID: templateID, Content: content,
		Status: models.MessageStatusPending, CreatedAt: time.Now()}
	_, err := db.conn.Exec(
		`INSERT INTO message_logs(id,shop_domain,automation_id,contact_phone,template_id,content,status,created_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		l.ID, shop, l.AutomationID, l.ContactPhone, l.TemplateID, l.Content, l.Status, l.CreatedAt)
	return &l, err
}

// LogOutboundMessage records a successfully-sent outbound message that doesn't go
// through the worker/automation pipeline — poll-vote replies, text-confirmation
// replies, Step 2 cancel replies, keyword auto-replies — in message_logs and
// increments the monthly usage counter. Without this, those messages are sent but
// never counted, so the dashboard under-reports usage.
func (db *DB) LogOutboundMessage(shop, phone, content string) {
	logEntry, err := db.CreateMessageLog(shop, "", phone, "", content)
	if err != nil {
		return
	}
	db.UpdateMessageLogStatus(logEntry.ID, models.MessageStatusSent, "")
}

func (db *DB) UpdateMessageLogStatus(id string, status models.MessageStatus, errMsg string) error {
	var sentAt interface{}
	if status == models.MessageStatusSent {
		sentAt = time.Now()
	}
	_, err := db.conn.Exec(
		`UPDATE message_logs SET status=?,error=?,sent_at=? WHERE id=?`,
		status, errMsg, sentAt, id)
	if err == nil && status == models.MessageStatusSent {
		var shop string
		db.conn.QueryRow(`SELECT shop_domain FROM message_logs WHERE id=?`, id).Scan(&shop)
		if shop != "" {
			db.IncrementSentMessages(shop)
		}
	}
	return err
}

func (db *DB) ListMessageLogs(shop string, limit int) ([]models.MessageLog, error) {
	rows, err := db.conn.Query(
		`SELECT id,COALESCE(automation_id,''),contact_phone,COALESCE(template_id,''),
		        content,status,COALESCE(error,''),sent_at,created_at
		 FROM message_logs WHERE shop_domain=? ORDER BY created_at DESC LIMIT ?`, shop, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.MessageLog
	for rows.Next() {
		var l models.MessageLog
		rows.Scan(&l.ID, &l.AutomationID, &l.ContactPhone, &l.TemplateID,
			&l.Content, &l.Status, &l.Error, &l.SentAt, &l.CreatedAt)
		out = append(out, l)
	}
	return out, nil
}

func (db *DB) CreateIncomingMessageLog(shop, phone, content string) (*models.MessageLog, error) {
	phone = normalizePhone(phone)
	l := models.MessageLog{
		ID:           uuid.NewString(),
		AutomationID: "",
		ContactPhone: phone,
		TemplateID:   "",
		Content:      content,
		Status:       "received",
		CreatedAt:    time.Now(),
	}
	sentAt := time.Now()
	_, err := db.conn.Exec(
		`INSERT INTO message_logs(id,shop_domain,automation_id,contact_phone,template_id,content,status,sent_at,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		l.ID, shop, l.AutomationID, l.ContactPhone, l.TemplateID, l.Content, l.Status, sentAt, l.CreatedAt)
	return &l, err
}

type ChatSession struct {
	Phone       string    `json:"phone"`
	ContactName string    `json:"contact_name"`
	LastMessage string    `json:"last_message"`
	LastStatus  string    `json:"last_status"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (db *DB) ListActiveChats(shop string) ([]ChatSession, error) {
	// Subquery finds the most recent message per phone without window functions
	// (compatible with SQLite < 3.25). LEFT JOIN ensures contacts who only sent
	// incoming messages (no Shopify order, not in contacts table) still appear.
	query := `
		SELECT
			m.contact_phone,
			COALESCE(c.name, m.contact_phone) AS contact_name,
			m.content AS last_message,
			m.status  AS last_status,
			m.created_at AS updated_at
		FROM message_logs m
		LEFT JOIN contacts c
			ON c.phone = m.contact_phone AND c.shop_domain = m.shop_domain
		WHERE m.shop_domain = ?
		  AND m.created_at = (
			SELECT MAX(m2.created_at)
			FROM message_logs m2
			WHERE m2.shop_domain = m.shop_domain
			  AND m2.contact_phone = m.contact_phone
		  )
		GROUP BY m.contact_phone
		ORDER BY m.created_at DESC
	`
	rows, err := db.conn.Query(query, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []ChatSession
	seen := make(map[string]bool)
	for rows.Next() {
		var cs ChatSession
		if err := rows.Scan(&cs.Phone, &cs.ContactName, &cs.LastMessage, &cs.LastStatus, &cs.UpdatedAt); err == nil {
			if !seen[cs.Phone] {
				seen[cs.Phone] = true
				if cs.ContactName == "" {
					cs.ContactName = cs.Phone
				}
				sessions = append(sessions, cs)
			}
		}
	}
	return sessions, nil
}

func (db *DB) GetChatHistory(shop, phone string) ([]models.MessageLog, error) {
	phone = normalizePhone(phone)
	rows, err := db.conn.Query(
		`SELECT id, COALESCE(automation_id,''), contact_phone, COALESCE(template_id,''),
		        content, status, COALESCE(error,''), sent_at, created_at
		 FROM message_logs
		 WHERE shop_domain = ? AND contact_phone = ?
		 ORDER BY created_at ASC`, shop, phone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.MessageLog
	for rows.Next() {
		var l models.MessageLog
		rows.Scan(&l.ID, &l.AutomationID, &l.ContactPhone, &l.TemplateID,
			&l.Content, &l.Status, &l.Error, &l.SentAt, &l.CreatedAt)
		out = append(out, l)
	}
	return out, nil
}

// ─── Reply Reminders ─────────────────────────────────────────────────────────

func (db *DB) CreateReplyReminder(shop, phone, message, msgType string, options []string, originalSentAt, sendAt time.Time) error {
	if options == nil {
		options = []string{}
	}
	optJSON, _ := json.Marshal(options)
	_, err := db.conn.Exec(
		`INSERT INTO reply_reminders(id,shop_domain,phone,message,message_type,options,original_sent_at,send_at,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		uuid.NewString(), shop, phone, message, msgType, string(optJSON), originalSentAt, sendAt, time.Now())
	return err
}

func (db *DB) GetPendingReminders() ([]models.ReplyReminder, error) {
	rows, err := db.conn.Query(
		`SELECT id,shop_domain,phone,message,message_type,options,original_sent_at,send_at
		 FROM reply_reminders
		 WHERE status='pending' AND send_at<=?
		 ORDER BY send_at ASC LIMIT 50`, time.Now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ReplyReminder
	for rows.Next() {
		var r models.ReplyReminder
		var optJSON string
		rows.Scan(&r.ID, &r.ShopDomain, &r.Phone, &r.Message, &r.MessageType, &optJSON, &r.OriginalSentAt, &r.SendAt)
		json.Unmarshal([]byte(optJSON), &r.Options)
		out = append(out, r)
	}
	return out, nil
}

// HasRepliedSince returns true if the customer sent a WhatsApp message to this shop after `since`.
func (db *DB) HasRepliedSince(shop, phone string, since time.Time) bool {
	phone = normalizePhone(phone)
	var count int
	db.conn.QueryRow(
		`SELECT COUNT(*) FROM message_logs
		 WHERE shop_domain=? AND contact_phone=? AND status='received' AND created_at>?`,
		shop, phone, since).Scan(&count)
	return count > 0
}

func (db *DB) CompleteReminder(id, status string) error {
	_, err := db.conn.Exec(`UPDATE reply_reminders SET status=? WHERE id=?`, status, id)
	return err
}

// ─── Pending Jobs (persistent queue) ─────────────────────────────────────────

func (db *DB) EnqueueJob(shop, automationID, templateID, phone, message string,
	msgType models.MessageType, options []string, runAt time.Time,
	orderID int64, tagOnSend string) error {
	if msgType == "" {
		msgType = models.MessageTypeText
	}
	if options == nil {
		options = []string{}
	}
	optJSON, _ := json.Marshal(options)
	_, err := db.conn.Exec(
		`INSERT INTO pending_jobs(id,shop_domain,phone,message,message_type,options,automation_id,template_id,scheduled_at,created_at,updated_at,order_id,tag_on_send)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		uuid.NewString(), shop, phone, message, string(msgType), string(optJSON),
		automationID, templateID, runAt, time.Now(), time.Now(), orderID, tagOnSend)
	return err
}

func (db *DB) GetReadyJobs(limit int) ([]models.PendingJob, error) {
	rows, err := db.conn.Query(
		`SELECT id,shop_domain,phone,message,
		        COALESCE(message_type,'text'), COALESCE(options,'[]'),
		        COALESCE(automation_id,''), COALESCE(template_id,''),
		        attempts, max_attempts,
		        COALESCE(order_id,0), COALESCE(tag_on_send,'')
		 FROM pending_jobs
		 WHERE status='pending' AND scheduled_at<=? AND attempts<max_attempts
		 ORDER BY scheduled_at ASC LIMIT ?`, time.Now(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PendingJob
	for rows.Next() {
		var j models.PendingJob
		var msgType, optJSON string
		rows.Scan(&j.ID, &j.ShopDomain, &j.Phone, &j.Message,
			&msgType, &optJSON,
			&j.AutomationID, &j.TemplateID, &j.Attempts, &j.MaxAttempts,
			&j.OrderID, &j.TagOnSend)
		j.MessageType = models.MessageType(msgType)
		json.Unmarshal([]byte(optJSON), &j.Options)
		if j.Options == nil {
			j.Options = []string{}
		}
		out = append(out, j)
	}
	return out, nil
}

// ClaimJob atomically sets a job to 'processing'. Returns false if another
// worker already claimed it.
func (db *DB) ClaimJob(id string) bool {
	res, err := db.conn.Exec(
		`UPDATE pending_jobs SET status='processing',attempts=attempts+1,updated_at=?
		 WHERE id=? AND status='pending'`, time.Now(), id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (db *DB) CompleteJob(id string) {
	db.conn.Exec(`UPDATE pending_jobs SET status='sent',updated_at=? WHERE id=?`, time.Now(), id)
}

func (db *DB) FailJob(id, errMsg string) {
	db.conn.Exec(
		`UPDATE pending_jobs
		 SET status=CASE WHEN attempts>=max_attempts THEN 'failed' ELSE 'pending' END,
		     error=?, updated_at=?
		 WHERE id=?`, errMsg, time.Now(), id)
}

// ─── Settings ─────────────────────────────────────────────────────────────────

var defaultSettings = models.Settings{
	TypingSimulationEnabled: true, TypingSpeedCPM: 220,
	MinTypingSeconds: 2, MaxTypingSeconds: 14,
	ReadDelayMinSeconds: 1, ReadDelayMaxSeconds: 5,
	FrequencyCapPerDay: 0, SendingWindowStart: -1, SendingWindowEnd: -1,
	WinBackInactiveDays: 0,
	PlanKey: "free", PlanName: "Free", MessageLimit: 150, MessagesSentThisMonth: 0,
}

func (db *DB) CheckAndResetPlanLimits(shop string) {
	var limitResetAt *time.Time
	var messagesSentThisMonth int
	err := db.conn.QueryRow(`SELECT limit_reset_at, messages_sent_this_month FROM settings WHERE shop_domain=?`, shop).Scan(&limitResetAt, &messagesSentThisMonth)
	if err != nil {
		return
	}
	if limitResetAt != nil && time.Now().After(*limitResetAt) {
		nextReset := *limitResetAt
		for !nextReset.After(time.Now()) {
			nextReset = nextReset.AddDate(0, 1, 0)
		}
		db.conn.Exec(`UPDATE settings SET messages_sent_this_month = 0, limit_reset_at = ? WHERE shop_domain = ?`, nextReset, shop)
	}
}

func (db *DB) ChargeUsageMessage(shop string, planKey string, lineItemId string) (bool, error) {
	if lineItemId == "" {
		slog.Warn("ChargeUsageMessage failed: no subscription_line_item_id stored", "shop", shop)
		return false, fmt.Errorf("no subscription_line_item_id stored for shop")
	}

	token := db.GetShopToken(shop)
	if token == "" {
		slog.Warn("ChargeUsageMessage failed: no access token stored", "shop", shop)
		return false, fmt.Errorf("no access token stored for shop")
	}

	var price float64 = 0.02
	if planKey == "pro" {
		price = 0.01
	}

	const query = `mutation appUsageRecordCreate($input: AppUsageRecordCreateInput!) {
		appUsageRecordCreate(input: $input) {
			appUsageRecord { id }
			userErrors { field message }
		}
	}`

	idempotencyKey := uuid.New().String()
	payload, _ := json.Marshal(map[string]interface{}{
		"query": query,
		"variables": map[string]interface{}{
			"input": map[string]interface{}{
				"subscriptionLineItemId": lineItemId,
				"description":            "Charge for extra WhatsApp message above limit",
				"price": map[string]interface{}{
					"amount":       price,
					"currencyCode": "USD",
				},
				"idempotencyKey": idempotencyKey,
			},
		},
	})

	url := fmt.Sprintf("https://%s/admin/api/2026-01/graphql.json", shop)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("shopify usage charge graphql status %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			AppUsageRecordCreate struct {
				AppUsageRecord struct{ ID string } `json:"appUsageRecord"`
				UserErrors     []struct{ Message string } `json:"userErrors"`
			} `json:"appUsageRecordCreate"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	if len(result.Data.AppUsageRecordCreate.UserErrors) > 0 {
		var errMsgs []string
		for _, e := range result.Data.AppUsageRecordCreate.UserErrors {
			errMsgs = append(errMsgs, e.Message)
		}
		errMsg := strings.Join(errMsgs, "; ")
		slog.Error("Shopify appUsageRecordCreate mutation returned userErrors", "shop", shop, "errors", errMsg)
		return false, fmt.Errorf("graphql errors: %s", errMsg)
	}

	slog.Info("Successfully charged usage fee for extra message", "shop", shop, "price", price, "id", result.Data.AppUsageRecordCreate.AppUsageRecord.ID)
	return true, nil
}

func (db *DB) CanSendWhatsAppMessage(shop string) (bool, error) {
	db.CheckAndResetPlanLimits(shop)
	var limit int
	var sent int
	var planKey string
	var lineItemId string
	err := db.conn.QueryRow(`SELECT message_limit, messages_sent_this_month, COALESCE(plan_key,'free'), COALESCE(subscription_line_item_id,'') FROM settings WHERE shop_domain=?`, shop).Scan(&limit, &sent, &planKey, &lineItemId)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if limit == -1 {
		return true, nil
	}
	if sent < limit {
		return true, nil
	}

	// Over limit: pause sending. The merchant must approve the higher charge in
	// Shopify before sending resumes, so the app surfaces a one-click upgrade
	// prompt to the next tier (the frontend derives "over limit" + next plan from
	// message_limit / messages_sent_this_month in GET /api/settings).
	_ = lineItemId // retained for signature compatibility; usage-charging removed
	_ = planKey
	return false, nil
}

func (db *DB) IncrementSentMessages(shop string) {
	db.CheckAndResetPlanLimits(shop)
	db.conn.Exec(`UPDATE settings SET messages_sent_this_month = messages_sent_this_month + 1 WHERE shop_domain = ?`, shop)
}

func (db *DB) SyncShopPlan(shop string, planName string) error {
	return db.SyncShopPlanWithLineItem(shop, planName, "")
}

func (db *DB) SyncShopPlanWithLineItem(shop string, planName string, lineItemId string) error {
	planKey := strings.ToLower(planName)
	var displayName string = planName
	var messageLimit int = 150

	err := db.conn.QueryRow(`SELECT plan_key, display_name, message_limit FROM admin_plans WHERE LOWER(display_name) = ? OR plan_key = ?`, planKey, planKey).Scan(&planKey, &displayName, &messageLimit)
	if err != nil {
		switch {
		case strings.Contains(planKey, "professional"):
			planKey, displayName, messageLimit = "professional", "Professional", 4800
		case strings.Contains(planKey, "growth"):
			planKey, displayName, messageLimit = "growth", "Growth", 2800
		case strings.Contains(planKey, "starter"):
			planKey, displayName, messageLimit = "starter", "Starter", 1700
		default:
			planKey, displayName, messageLimit = "free", "Free", 150
		}
	}

	var currentPlanKey string
	var limitResetAt *time.Time
	err = db.conn.QueryRow(`SELECT plan_key, limit_reset_at FROM settings WHERE shop_domain = ?`, shop).Scan(&currentPlanKey, &limitResetAt)
	
	now := time.Now()
	nextReset := now.AddDate(0, 1, 0)

	// Choosing/confirming a plan counts as the merchant having selected one.
	if err == sql.ErrNoRows {
		_, err = db.conn.Exec(
			`INSERT INTO settings(shop_domain, plan_key, plan_name, message_limit, messages_sent_this_month, limit_reset_at, subscription_line_item_id, plan_selected)
			 VALUES(?, ?, ?, ?, 0, ?, ?, 1)`,
			shop, planKey, displayName, messageLimit, nextReset, lineItemId)
		return err
	} else if err == nil {
		if currentPlanKey != planKey || limitResetAt == nil {
			_, err = db.conn.Exec(
				`UPDATE settings SET plan_key = ?, plan_name = ?, message_limit = ?, messages_sent_this_month = 0, limit_reset_at = ?, subscription_line_item_id = ?, plan_selected = 1
				 WHERE shop_domain = ?`,
				planKey, displayName, messageLimit, nextReset, lineItemId, shop)
		} else {
			// Update subscription line item ID even if plan didn't change (e.g. renewal or reinstallation)
			_, err = db.conn.Exec(
				`UPDATE settings SET plan_key = ?, plan_name = ?, message_limit = ?, subscription_line_item_id = ?, plan_selected = 1
				 WHERE shop_domain = ?`,
				planKey, displayName, messageLimit, lineItemId, shop)
		}
		return err
	}
	return err
}

// IsPlanSelected reports whether the shop has explicitly chosen a plan (free or
// paid) since install. Used to gate app entry until a plan is picked.
func (db *DB) IsPlanSelected(shop string) bool {
	var selected int
	db.conn.QueryRow(`SELECT COALESCE(plan_selected,0) FROM settings WHERE shop_domain=?`, shop).Scan(&selected)
	return selected == 1
}

func (db *DB) GetSettings(shop string) (models.Settings, error) {
	db.CheckAndResetPlanLimits(shop)
	var s models.Settings
	var enabled int
	err := db.conn.QueryRow(
		`SELECT typing_simulation_enabled,typing_speed_cpm,
		        min_typing_seconds,max_typing_seconds,
		        read_delay_min_seconds,read_delay_max_seconds,
		        COALESCE(frequency_cap_per_day,0),
		        COALESCE(sending_window_start,-1),
		        COALESCE(sending_window_end,-1),
		        COALESCE(win_back_inactive_days,0),
		        COALESCE(plan_key,'free'),
		        COALESCE(plan_name,'Free'),
		        COALESCE(message_limit,150),
		        COALESCE(messages_sent_this_month,0),
		        limit_reset_at,
		        COALESCE(subscription_line_item_id,'')
		 FROM settings WHERE shop_domain=?`, shop).
		Scan(&enabled, &s.TypingSpeedCPM,
			&s.MinTypingSeconds, &s.MaxTypingSeconds,
			&s.ReadDelayMinSeconds, &s.ReadDelayMaxSeconds,
			&s.FrequencyCapPerDay, &s.SendingWindowStart, &s.SendingWindowEnd,
			&s.WinBackInactiveDays, &s.PlanKey, &s.PlanName, &s.MessageLimit,
			&s.MessagesSentThisMonth, &s.LimitResetAt, &s.SubscriptionLineItemId)
	if err == sql.ErrNoRows {
		return defaultSettings, nil
	}
	s.TypingSimulationEnabled = enabled == 1
	return s, err
}

func (db *DB) SaveSettings(shop string, s models.Settings) error {
	v := 0
	if s.TypingSimulationEnabled {
		v = 1
	}
	_, err := db.conn.Exec(
		`INSERT INTO settings(shop_domain,typing_simulation_enabled,typing_speed_cpm,
		  min_typing_seconds,max_typing_seconds,read_delay_min_seconds,read_delay_max_seconds,
		  frequency_cap_per_day,sending_window_start,sending_window_end,win_back_inactive_days,
		  updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		 ON CONFLICT(shop_domain) DO UPDATE SET
		  typing_simulation_enabled=excluded.typing_simulation_enabled,
		  typing_speed_cpm=excluded.typing_speed_cpm,
		  min_typing_seconds=excluded.min_typing_seconds,
		  max_typing_seconds=excluded.max_typing_seconds,
		  read_delay_min_seconds=excluded.read_delay_min_seconds,
		  read_delay_max_seconds=excluded.read_delay_max_seconds,
		  frequency_cap_per_day=excluded.frequency_cap_per_day,
		  sending_window_start=excluded.sending_window_start,
		  sending_window_end=excluded.sending_window_end,
		  win_back_inactive_days=excluded.win_back_inactive_days,
		  updated_at=CURRENT_TIMESTAMP`,
		shop, v, s.TypingSpeedCPM, s.MinTypingSeconds, s.MaxTypingSeconds,
		s.ReadDelayMinSeconds, s.ReadDelayMaxSeconds,
		s.FrequencyCapPerDay, s.SendingWindowStart, s.SendingWindowEnd,
		s.WinBackInactiveDays)
	return err
}

// ─── Frequency cap ────────────────────────────────────────────────────────────

// MessageCountToday returns how many messages were successfully sent to a phone today.
func (db *DB) MessageCountToday(shop, phone string) int {
	var n int
	db.conn.QueryRow(
		`SELECT COUNT(*) FROM message_logs
		 WHERE shop_domain=? AND contact_phone=? AND status='sent'
		 AND DATE(created_at)=DATE('now')`, shop, phone).Scan(&n)
	return n
}

// RescheduleJob defers a job to a specific time (used for time-of-day windowing).
func (db *DB) RescheduleJob(id string, at time.Time) {
	db.conn.Exec(
		`UPDATE pending_jobs SET scheduled_at=?,status='pending',updated_at=? WHERE id=?`,
		at, time.Now(), id)
}

// ─── Keyword auto-replies ─────────────────────────────────────────────────────

func (db *DB) ListKeywordReplies(shop string) ([]models.KeywordReply, error) {
	rows, err := db.conn.Query(
		`SELECT id,keyword,reply_message,is_active,created_at
		 FROM keyword_replies WHERE shop_domain=? ORDER BY keyword ASC`, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.KeywordReply
	for rows.Next() {
		var k models.KeywordReply
		var active int
		if err := rows.Scan(&k.ID, &k.Keyword, &k.ReplyMessage, &active, &k.CreatedAt); err != nil {
			continue
		}
		k.IsActive = active == 1
		out = append(out, k)
	}
	return out, nil
}

func (db *DB) GetKeywordReply(shop, text string) string {
	var reply string
	db.conn.QueryRow(
		`SELECT reply_message FROM keyword_replies
		 WHERE shop_domain=? AND LOWER(keyword)=LOWER(?) AND is_active=1
		 LIMIT 1`, shop, strings.TrimSpace(text)).Scan(&reply)
	return reply
}

func (db *DB) SaveKeywordReply(shop string, k models.KeywordReply) error {
	active := 0
	if k.IsActive {
		active = 1
	}
	id := k.ID
	if id == "" {
		id = uuid.NewString()
	}
	_, err := db.conn.Exec(
		`INSERT INTO keyword_replies(id,shop_domain,keyword,reply_message,is_active,created_at)
		 VALUES(?,?,?,?,?,CURRENT_TIMESTAMP)
		 ON CONFLICT(id) DO UPDATE SET
		  keyword=excluded.keyword,
		  reply_message=excluded.reply_message,
		  is_active=excluded.is_active`,
		id, shop, k.Keyword, k.ReplyMessage, active)
	return err
}

func (db *DB) DeleteKeywordReply(shop, id string) error {
	_, err := db.conn.Exec(
		`DELETE FROM keyword_replies WHERE id=? AND shop_domain=?`, id, shop)
	return err
}

// ─── Win-back ─────────────────────────────────────────────────────────────────

// WinBackCandidates returns non-opted-out contacts whose last order was more than
// inactiveDays ago AND who have never had a win-back job enqueued today.
func (db *DB) WinBackCandidates(shop string, inactiveDays int) []models.Contact {
	rows, err := db.conn.Query(
		`SELECT c.id,COALESCE(c.name,''),c.phone,COALESCE(c.shopify_customer_id,''),c.opted_out,c.created_at
		 FROM contacts c
		 WHERE c.shop_domain=? AND c.opted_out=0
		   AND c.last_order_at IS NOT NULL
		   AND c.last_order_at < datetime('now',?)
		   AND NOT EXISTS (
		     SELECT 1 FROM pending_jobs j
		     WHERE j.shop_domain=? AND j.phone=c.phone
		       AND j.automation_id IN (
		         SELECT id FROM automations WHERE shop_domain=? AND trigger_type='win_back'
		       )
		       AND DATE(j.created_at)=DATE('now')
		   )
		 LIMIT 100`,
		shop, fmt.Sprintf("-%d days", inactiveDays), shop, shop)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []models.Contact
	for rows.Next() {
		var c models.Contact
		var optedOut int
		rows.Scan(&c.ID, &c.Name, &c.Phone, &c.ShopifyCustomerID, &optedOut, &c.CreatedAt)
		c.OptedOut = optedOut == 1
		out = append(out, c)
	}
	return out
}

// UpdateLastOrderAt stamps the contact's last_order_at when an order is placed.
func (db *DB) UpdateLastOrderAt(shop, phone string) {
	db.conn.Exec(
		`UPDATE contacts SET last_order_at=CURRENT_TIMESTAMP WHERE shop_domain=? AND phone=?`,
		shop, normalizePhone(phone))
}

// ─── Broadcast ────────────────────────────────────────────────────────────────

// AllActiveContacts returns every non-opted-out contact for a shop.
func (db *DB) AllActiveContacts(shop string) ([]models.Contact, error) {
	rows, err := db.conn.Query(
		`SELECT id,COALESCE(name,''),phone,COALESCE(shopify_customer_id,''),opted_out,created_at
		 FROM contacts WHERE shop_domain=? AND opted_out=0 ORDER BY created_at ASC`, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Contact
	for rows.Next() {
		var c models.Contact
		var optedOut int
		rows.Scan(&c.ID, &c.Name, &c.Phone, &c.ShopifyCustomerID, &optedOut, &c.CreatedAt)
		c.OptedOut = optedOut == 1
		out = append(out, c)
	}
	return out, nil
}

// ─── Revenue attribution ──────────────────────────────────────────────────────

// WASentRecently returns true if a WhatsApp message was successfully sent to phone
// within the given number of hours.
func (db *DB) WASentRecently(shop, phone string, hours int) bool {
	phone = normalizePhone(phone)
	var count int
	db.conn.QueryRow(
		`SELECT COUNT(*) FROM message_logs
		 WHERE shop_domain=? AND contact_phone=? AND status='sent'
		 AND created_at >= datetime('now',?)`,
		shop, phone, fmt.Sprintf("-%d hours", hours)).Scan(&count)
	return count > 0
}

// ─── Opt-out trends ───────────────────────────────────────────────────────────

// OptOutTrends returns daily opt-out counts over the last `days` days.
func (db *DB) OptOutTrends(shop string, days int) []models.OptOutStat {
	rows, err := db.conn.Query(
		`SELECT DATE(opted_out_at) as d, COUNT(*) as n
		 FROM contacts
		 WHERE shop_domain=? AND opted_out=1
		   AND opted_out_at >= datetime('now',?)
		 GROUP BY d ORDER BY d ASC`,
		shop, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []models.OptOutStat
	for rows.Next() {
		var s models.OptOutStat
		rows.Scan(&s.Date, &s.Count)
		out = append(out, s)
	}
	return out
}

// AllShops returns the list of distinct shop_domain values that have settings rows.
func (db *DB) AllShops() []string {
	rows, _ := db.conn.Query(`SELECT shop_domain FROM settings`)
	if rows == nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// UpsertContactNew is like UpsertContact but returns true when the contact is brand new.
func (db *DB) UpsertContactNew(shop, name, phone, shopifyID string) bool {
	phone = normalizePhone(phone)
	var count int
	db.conn.QueryRow(`SELECT COUNT(*) FROM contacts WHERE shop_domain=? AND phone=?`, shop, phone).Scan(&count)
	db.conn.Exec(
		`INSERT INTO contacts(id,shop_domain,name,phone,shopify_customer_id,opted_out,created_at)
		 VALUES(?,?,?,?,?,0,CURRENT_TIMESTAMP)
		 ON CONFLICT(shop_domain,phone) DO UPDATE SET
		  name=COALESCE(NULLIF(excluded.name,''),name),
		  shopify_customer_id=COALESCE(NULLIF(excluded.shopify_customer_id,''),shopify_customer_id)`,
		uuid.NewString(), shop, name, phone, shopifyID)
	return count == 0
}

// ─── GDPR / Uninstall ─────────────────────────────────────────────────────────

// PurgeShop deletes ALL data for a shop. Called on APP_UNINSTALLED.
func (db *DB) PurgeShop(shop string) error {
	tables := []string{"message_logs", "pending_jobs", "contacts", "automations", "templates", "settings"}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE shop_domain=?`, shop); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// PurgeCustomer deletes contact + message log data for one customer (GDPR redact).
func (db *DB) PurgeCustomer(shop, phone string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	tx.Exec(`DELETE FROM contacts WHERE shop_domain=? AND phone=?`, shop, phone)
	tx.Exec(`DELETE FROM message_logs WHERE shop_domain=? AND contact_phone=?`, shop, phone)
	tx.Exec(`DELETE FROM pending_jobs WHERE shop_domain=? AND phone=?`, shop, phone)
	return tx.Commit()
}

// GetCustomerData returns all data held for a customer (GDPR data request).
func (db *DB) GetCustomerData(shop, phone string) map[string]interface{} {
	var contact models.Contact
	var optedOut int
	db.conn.QueryRow(
		`SELECT id,COALESCE(name,''),phone,COALESCE(shopify_customer_id,''),opted_out,created_at
		 FROM contacts WHERE shop_domain=? AND phone=?`, shop, phone).
		Scan(&contact.ID, &contact.Name, &contact.Phone,
			&contact.ShopifyCustomerID, &optedOut, &contact.CreatedAt)
	contact.OptedOut = optedOut == 1

	logs, _ := db.ListMessageLogs(shop, 1000)
	var customerLogs []models.MessageLog
	for _, l := range logs {
		if l.ContactPhone == phone {
			customerLogs = append(customerLogs, l)
		}
	}
	return map[string]interface{}{"contact": contact, "messages": customerLogs}
}

// ─── Pending Confirmations ────────────────────────────────────────────────────

type PendingConfirmation struct {
	ShopDomain         string
	Phone              string
	ReplyMessage       string
	ReplyType          string
	ReplyOptions       []string
	TriggerOption      string // SHA256 of this option must be in votedHashes to fire

	ReplyNoMessage     string
	ReplyNoType        string
	ReplyNoOptions     []string
	NoOption           string

	ReplyHelpMessage    string
	ReplyHelpType       string
	ReplyHelpOptions    []string
	HelpOption          string

	Step2YesMessage     string
	Step2NoMessage      string
	Step2HelpMessage    string

	VotedBranch         string // "yes" | "no" | "help"
	OrderID             int64
	TagOnYes            string
	TagOnNo             string
}

// normalizePhone strips formatting so Shopify phones (e.g. "+1 415-555-2671")
// match the digit-only format WhatsApp JIDs use ("14155552671").
func normalizePhone(phone string) string {
	phone = strings.TrimPrefix(phone, "+")
	for _, r := range []string{" ", "-", "(", ")", "."} {
		phone = strings.ReplaceAll(phone, r, "")
	}
	return phone
}

// optionVoted returns true when the SHA256 hash of option appears in votedHashes.
func optionVoted(option string, votedHashes [][]byte) bool {
	h := sha256.Sum256([]byte(option))
	for _, v := range votedHashes {
		if bytes.Equal(h[:], v) {
			return true
		}
	}
	return false
}

// StorePendingConfirmationExtended saves positive, negative, and help replies, plus step 2 follow-ups that are sent depending on which option the customer votes for.
func (db *DB) StorePendingConfirmationExtended(
	shop, phone string,
	yesMsg, yesType string, yesOpts []string, yesOption string,
	noMsg, noType string, noOpts []string, noOption string,
	helpMsg, helpType string, helpOpts []string, helpOption string,
	step2Yes, step2No, step2Help string,
	orderID int64, tagOnYes, tagOnNo string,
) error {
	phone = normalizePhone(phone)
	slog.Info("storing pending confirmation extended",
		"shop", shop, "phone", phone,
		"yes_option", yesOption,
		"no_option", noOption,
		"help_option", helpOption,
		"has_step2_yes", step2Yes != "",
		"order_id", orderID)
	
	yesOptJSON, _ := json.Marshal(yesOpts)
	noOptJSON, _ := json.Marshal(noOpts)
	helpOptJSON, _ := json.Marshal(helpOpts)

	_, err := db.conn.Exec(
		`INSERT INTO pending_confirmations
		 (id, shop_domain, phone, 
		  reply_message, reply_type, reply_options, trigger_option,
		  reply_no_message, reply_no_type, reply_no_options, no_option,
		  reply_help_message, reply_help_type, reply_help_options, help_option,
		  step2_yes_message, step2_no_message, step2_help_message,
		  order_id, tag_on_yes, tag_on_no,
		  expires_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, datetime('now','+24 hours'))
		 ON CONFLICT(shop_domain,phone) DO UPDATE SET
		   reply_message=excluded.reply_message,
		   reply_type=excluded.reply_type,
		   reply_options=excluded.reply_options,
		   trigger_option=excluded.trigger_option,
		   reply_no_message=excluded.reply_no_message,
		   reply_no_type=excluded.reply_no_type,
		   reply_no_options=excluded.reply_no_options,
		   no_option=excluded.no_option,
		   reply_help_message=excluded.reply_help_message,
		   reply_help_type=excluded.reply_help_type,
		   reply_help_options=excluded.reply_help_options,
		   help_option=excluded.help_option,
		   step2_yes_message=excluded.step2_yes_message,
		   step2_no_message=excluded.step2_no_message,
		   step2_help_message=excluded.step2_help_message,
		   order_id=excluded.order_id,
		   tag_on_yes=excluded.tag_on_yes,
		   tag_on_no=excluded.tag_on_no,
		   expires_at=excluded.expires_at`,
		uuid.NewString(), shop, phone,
		yesMsg, yesType, string(yesOptJSON), yesOption,
		noMsg, noType, string(noOptJSON), noOption,
		helpMsg, helpType, string(helpOptJSON), helpOption,
		step2Yes, step2No, step2Help,
		orderID, tagOnYes, tagOnNo,
	)
	return err
}

// StorePendingConfirmation is kept for legacy compatibility (maps to yes/positive branch only).
func (db *DB) StorePendingConfirmation(shop, phone, message, msgType string, options []string, triggerOption string) error {
	return db.StorePendingConfirmationExtended(
		shop, phone,
		message, msgType, options, triggerOption,
		"", "text", []string{}, "",
		"", "text", []string{}, "",
		"", "", "",
		0, "", "",
	)
}

// PopPendingConfirmation retrieves and deletes the stored reply for a customer.
// votedHashes is the slice of SHA256 option hashes returned by DecryptPollVote;
// pass nil for plain-text messages.
func (db *DB) PopPendingConfirmation(shop, phone string, votedHashes [][]byte) *PendingConfirmation {
	phone = normalizePhone(phone)
	var pc PendingConfirmation
	var yesOptsJSON, noOptsJSON, helpOptsJSON string
	err := db.conn.QueryRow(
		`SELECT shop_domain, phone, 
		        reply_message, reply_type, reply_options, COALESCE(trigger_option,''),
		        reply_no_message, reply_no_type, reply_no_options, COALESCE(no_option,''),
		        reply_help_message, reply_help_type, reply_help_options, COALESCE(help_option,''),
		        COALESCE(step2_yes_message,''), COALESCE(step2_no_message,''), COALESCE(step2_help_message,''),
		        COALESCE(order_id, 0), COALESCE(tag_on_yes, ''), COALESCE(tag_on_no, '')
		 FROM pending_confirmations
		 WHERE shop_domain=? AND phone=? AND expires_at > datetime('now')`,
		shop, phone,
	).Scan(
		&pc.ShopDomain, &pc.Phone, 
		&pc.ReplyMessage, &pc.ReplyType, &yesOptsJSON, &pc.TriggerOption,
		&pc.ReplyNoMessage, &pc.ReplyNoType, &noOptsJSON, &pc.NoOption,
		&pc.ReplyHelpMessage, &pc.ReplyHelpType, &helpOptsJSON, &pc.HelpOption,
		&pc.Step2YesMessage, &pc.Step2NoMessage, &pc.Step2HelpMessage,
		&pc.OrderID, &pc.TagOnYes, &pc.TagOnNo,
	)
	if err != nil {
		slog.Info("pop pending confirmation: no entry found (or expired)", "shop", shop, "phone", phone)
		return nil
	}
	json.Unmarshal([]byte(yesOptsJSON), &pc.ReplyOptions)
	json.Unmarshal([]byte(noOptsJSON), &pc.ReplyNoOptions)
	json.Unmarshal([]byte(helpOptsJSON), &pc.ReplyHelpOptions)

	slog.Info("pop pending confirmation: found entry",
		"shop", shop, "phone", phone,
		"trigger_option", pc.TriggerOption,
		"no_option", pc.NoOption,
		"help_option", pc.HelpOption,
		"via_poll_vote", votedHashes != nil,
		"voted_hash_count", len(votedHashes),
		"order_id", pc.OrderID)

	if votedHashes == nil {
		// Text-message path: entries that require a specific poll vote must NOT fire here.
		if pc.TriggerOption != "" {
			slog.Info("pop pending confirmation: text message blocked by poll trigger_option guard",
				"shop", shop, "phone", phone, "trigger_option", pc.TriggerOption)
			return nil
		}
		pc.VotedBranch = "yes"
	} else {
		// Poll-vote path: check which option was voted.
		if optionVoted(pc.TriggerOption, votedHashes) {
			slog.Info("pop pending confirmation: positive option matched", "shop", shop, "phone", phone)
			pc.VotedBranch = "yes"
		} else if pc.NoOption != "" && optionVoted(pc.NoOption, votedHashes) {
			slog.Info("pop pending confirmation: negative option matched", "shop", shop, "phone", phone)
			pc.VotedBranch = "no"
		} else if pc.HelpOption != "" && optionVoted(pc.HelpOption, votedHashes) {
			slog.Info("pop pending confirmation: help option matched", "shop", shop, "phone", phone)
			pc.VotedBranch = "help"
		} else {
			slog.Info("pop pending confirmation: voted option hash did not match any trigger options", "shop", shop, "phone", phone)
			return nil
		}
	}

	db.conn.Exec(`DELETE FROM pending_confirmations WHERE shop_domain=? AND phone=?`, shop, phone)
	return &pc
}

// DeletePendingConfirmation removes any stored pending reply for a customer.
// Called when an order is cancelled so a stale Post-Confirmation Reply is not
// mistakenly sent when the customer later replies to a Cancellation Verification poll.
func (db *DB) DeletePendingConfirmation(shop, phone string) {
	phone = normalizePhone(phone)
	db.conn.Exec(`DELETE FROM pending_confirmations WHERE shop_domain=? AND phone=?`, shop, phone)
}

// ─── Analytics ────────────────────────────────────────────────────────────────

// GetAnalytics returns the full analytics payload for the given shop and day window.
func (db *DB) GetAnalytics(shop string, days int) (models.AnalyticsData, error) {
	var a models.AnalyticsData

	// ── Lifetime status totals ────────────────────────────────────────────────
	rows, err := db.conn.Query(
		`SELECT status, COUNT(*) FROM message_logs WHERE shop_domain=? GROUP BY status`, shop)
	if err != nil {
		return a, err
	}
	for rows.Next() {
		var status string
		var count int
		rows.Scan(&status, &count)
		switch status {
		case "sent":
			a.TotalSent = count
		case "failed":
			a.TotalFailed = count
		case "pending":
			a.TotalPending = count
		}
	}
	rows.Close()

	total := a.TotalSent + a.TotalFailed
	if total > 0 {
		a.SuccessRate = float64(a.TotalSent) / float64(total) * 100
	}

	// ── Pending jobs in queue ─────────────────────────────────────────────────
	db.conn.QueryRow(
		`SELECT COUNT(*) FROM pending_jobs WHERE shop_domain=? AND status='pending'`, shop,
	).Scan(&a.PendingJobs)

	// ── Daily breakdown for last N days ──────────────────────────────────────
	// Build a map of date→DailyStats first, then fill in the zeroes.
	drows, err := db.conn.Query(`
		SELECT DATE(created_at) AS d, status, COUNT(*) AS n
		FROM message_logs
		WHERE shop_domain = ?
		  AND created_at >= DATE('now', ? || ' days')
		GROUP BY d, status
		ORDER BY d ASC`,
		shop, fmt.Sprintf("-%d", days))
	if err != nil {
		return a, err
	}
	dailyMap := make(map[string]*models.DailyStats)
	for drows.Next() {
		var d, status string
		var n int
		drows.Scan(&d, &status, &n)
		if _, ok := dailyMap[d]; !ok {
			dailyMap[d] = &models.DailyStats{Date: d}
		}
		switch status {
		case "sent":
			dailyMap[d].Sent = n
			a.PeriodSent += n
		case "failed":
			dailyMap[d].Failed = n
			a.PeriodFailed += n
		case "pending":
			dailyMap[d].Pending = n
		}
	}
	drows.Close()

	// Fill every calendar day in the window (even zero days).
	for i := days - 1; i >= 0; i-- {
		date := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		if s, ok := dailyMap[date]; ok {
			s.Total = s.Sent + s.Failed + s.Pending
			a.Daily = append(a.Daily, *s)
		} else {
			a.Daily = append(a.Daily, models.DailyStats{Date: date})
		}
	}

	// ── Hourly distribution of sent messages ──────────────────────────────────
	hrows, err := db.conn.Query(`
		SELECT CAST(strftime('%H', created_at) AS INTEGER) AS hr, COUNT(*) AS n
		FROM message_logs
		WHERE shop_domain=? AND status='sent'
		GROUP BY hr ORDER BY hr`, shop)
	if err != nil {
		return a, err
	}
	hourMap := make(map[int]int)
	for hrows.Next() {
		var hr, n int
		hrows.Scan(&hr, &n)
		hourMap[hr] = n
	}
	hrows.Close()
	for hr := 0; hr < 24; hr++ {
		a.Hourly = append(a.Hourly, models.HourlyStats{Hour: hr, Count: hourMap[hr]})
	}

	// ── Messages by trigger type ──────────────────────────────────────────────
	trows, err := db.conn.Query(`
		SELECT a.trigger_type, COUNT(*) AS n
		FROM message_logs ml
		JOIN automations a ON ml.automation_id = a.id
		WHERE ml.shop_domain=? AND ml.status='sent'
		GROUP BY a.trigger_type
		ORDER BY n DESC`, shop)
	if err == nil {
		for trows.Next() {
			var ts models.TriggerStats
			trows.Scan(&ts.Trigger, &ts.Count)
			a.ByTrigger = append(a.ByTrigger, ts)
		}
		trows.Close()
	}

	return a, nil
}

// ─── Stats ────────────────────────────────────────────────────────────────────

func (db *DB) GetStats(shop string) (models.DashboardStats, error) {
	db.CheckAndResetPlanLimits(shop)
	var s models.DashboardStats
	db.conn.QueryRow(`SELECT COUNT(*) FROM message_logs WHERE shop_domain=? AND status='sent'`, shop).Scan(&s.TotalMessagesSent)
	db.conn.QueryRow(`SELECT COUNT(*) FROM message_logs WHERE shop_domain=? AND status='sent' AND DATE(created_at)=DATE('now')`, shop).Scan(&s.MessagesToday)
	db.conn.QueryRow(`SELECT COUNT(*) FROM automations WHERE shop_domain=? AND is_active=1`, shop).Scan(&s.ActiveAutomations)
	db.conn.QueryRow(`SELECT COUNT(*) FROM contacts WHERE shop_domain=? AND opted_out=0`, shop).Scan(&s.TotalContacts)

	db.conn.QueryRow(
		`SELECT COALESCE(plan_name,'Free'), COALESCE(message_limit,150), COALESCE(messages_sent_this_month,0), limit_reset_at
		 FROM settings WHERE shop_domain=?`, shop).Scan(&s.PlanName, &s.MessageLimit, &s.MessagesSentThisMonth, &s.LimitResetAt)

	return s, nil
}
