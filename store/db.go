package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

		// ── indexes — every query filters by shop_domain ───────────────────
		`CREATE INDEX IF NOT EXISTS idx_templates_shop   ON templates(shop_domain)`,
		`CREATE INDEX IF NOT EXISTS idx_automations_shop ON automations(shop_domain, trigger_type, is_active)`,
		`CREATE INDEX IF NOT EXISTS idx_contacts_shop    ON contacts(shop_domain)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_shop        ON message_logs(shop_domain, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_ready       ON pending_jobs(status, scheduled_at) WHERE status='pending'`,

		// ── idempotent column additions for pre-existing DBs ─────────────────
		`ALTER TABLE templates    ADD COLUMN shop_domain TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE templates    ADD COLUMN message_type TEXT NOT NULL DEFAULT 'text'`,
		`ALTER TABLE templates    ADD COLUMN options TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE templates    ADD COLUMN is_default INTEGER DEFAULT 0`,
		`ALTER TABLE automations  ADD COLUMN shop_domain TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE message_logs ADD COLUMN shop_domain TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE contacts     ADD COLUMN opted_out INTEGER DEFAULT 0`,
		`ALTER TABLE pending_jobs ADD COLUMN message_type TEXT NOT NULL DEFAULT 'text'`,
		`ALTER TABLE pending_jobs ADD COLUMN options TEXT NOT NULL DEFAULT '[]'`,
	}

	for _, s := range stmts {
		db.conn.Exec(s) // ignore "already exists" / "duplicate column" errors
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

// SeedDefaultTemplates inserts the 9 built-in templates for a shop if they
// do not already exist (idempotent — safe to call multiple times).
func (db *DB) SeedDefaultTemplates(shop string) error {
	for _, tmpl := range models.DefaultTemplates {
		optJSON, _ := json.Marshal(tmpl.Options)
		if tmpl.Options == nil {
			optJSON = []byte("[]")
		}
		db.conn.Exec(
			`INSERT OR IGNORE INTO templates
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
		 FROM automations WHERE shop_domain=? AND trigger_type=? AND is_active=1`, shop, tt)
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

// defaultAutomationDefs are the 4 built-in automations seeded for every shop.
var defaultAutomationDefs = []struct {
	Name         string
	TriggerType  models.TriggerType
	TemplateName string
}{
	{"Order Confirmation",        models.TriggerOrderCreated,   "Order Confirmation"},
	{"Shipping Notification",     models.TriggerOrderFulfilled, "Shipping Alert"},
	{"Cancellation Notice",       models.TriggerOrderCancelled, "Order Cancellation"},
	{"Abandoned Cart Recovery",   models.TriggerAbandonedCart,  "Abandoned Cart Recovery"},
}

// SeedAutomations creates the 4 default automations for a shop if they don't exist.
// Templates must already be seeded before calling this.
func (db *DB) SeedAutomations(shop string) error {
	for _, def := range defaultAutomationDefs {
		var count int
		db.conn.QueryRow(
			`SELECT COUNT(*) FROM automations WHERE shop_domain=? AND trigger_type=?`,
			shop, def.TriggerType,
		).Scan(&count)
		if count > 0 {
			continue
		}
		var templateID string
		db.conn.QueryRow(
			`SELECT id FROM templates WHERE shop_domain=? AND name=? AND is_default=1`,
			shop, def.TemplateName,
		).Scan(&templateID)
		db.conn.Exec(
			`INSERT INTO automations
			 (id,shop_domain,name,trigger_type,template_id,is_active,delay_minutes,created_at,updated_at)
			 VALUES(?,?,?,?,?,0,0,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			uuid.NewString(), shop, def.Name, string(def.TriggerType), templateID,
		)
	}
	return nil
}

// DefaultAutomationsSeeded returns true when all 4 trigger types have an automation row.
func (db *DB) DefaultAutomationsSeeded(shop string) bool {
	var count int
	db.conn.QueryRow(
		`SELECT COUNT(DISTINCT trigger_type) FROM automations WHERE shop_domain=?`, shop,
	).Scan(&count)
	return count >= 4
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
	_, err := db.conn.Exec(
		`UPDATE contacts SET opted_out=? WHERE shop_domain=? AND phone=?`, v, shop, phone)
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

func (db *DB) UpdateMessageLogStatus(id string, status models.MessageStatus, errMsg string) error {
	var sentAt interface{}
	if status == models.MessageStatusSent {
		sentAt = time.Now()
	}
	_, err := db.conn.Exec(
		`UPDATE message_logs SET status=?,error=?,sent_at=? WHERE id=?`,
		status, errMsg, sentAt, id)
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

// ─── Pending Jobs (persistent queue) ─────────────────────────────────────────

func (db *DB) EnqueueJob(shop, automationID, templateID, phone, message string,
	msgType models.MessageType, options []string, runAt time.Time) error {
	if msgType == "" {
		msgType = models.MessageTypeText
	}
	if options == nil {
		options = []string{}
	}
	optJSON, _ := json.Marshal(options)
	_, err := db.conn.Exec(
		`INSERT INTO pending_jobs(id,shop_domain,phone,message,message_type,options,automation_id,template_id,scheduled_at,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		uuid.NewString(), shop, phone, message, string(msgType), string(optJSON),
		automationID, templateID, runAt, time.Now(), time.Now())
	return err
}

func (db *DB) GetReadyJobs(limit int) ([]models.PendingJob, error) {
	rows, err := db.conn.Query(
		`SELECT id,shop_domain,phone,message,
		        COALESCE(message_type,'text'), COALESCE(options,'[]'),
		        COALESCE(automation_id,''), COALESCE(template_id,''),
		        attempts, max_attempts
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
			&j.AutomationID, &j.TemplateID, &j.Attempts, &j.MaxAttempts)
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
}

func (db *DB) GetSettings(shop string) (models.Settings, error) {
	var s models.Settings
	var enabled int
	err := db.conn.QueryRow(
		`SELECT typing_simulation_enabled,typing_speed_cpm,
		        min_typing_seconds,max_typing_seconds,
		        read_delay_min_seconds,read_delay_max_seconds
		 FROM settings WHERE shop_domain=?`, shop).
		Scan(&enabled, &s.TypingSpeedCPM,
			&s.MinTypingSeconds, &s.MaxTypingSeconds,
			&s.ReadDelayMinSeconds, &s.ReadDelayMaxSeconds)
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
		  min_typing_seconds,max_typing_seconds,read_delay_min_seconds,read_delay_max_seconds,updated_at)
		 VALUES(?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		 ON CONFLICT(shop_domain) DO UPDATE SET
		  typing_simulation_enabled=excluded.typing_simulation_enabled,
		  typing_speed_cpm=excluded.typing_speed_cpm,
		  min_typing_seconds=excluded.min_typing_seconds,
		  max_typing_seconds=excluded.max_typing_seconds,
		  read_delay_min_seconds=excluded.read_delay_min_seconds,
		  read_delay_max_seconds=excluded.read_delay_max_seconds,
		  updated_at=CURRENT_TIMESTAMP`,
		shop, v, s.TypingSpeedCPM, s.MinTypingSeconds, s.MaxTypingSeconds,
		s.ReadDelayMinSeconds, s.ReadDelayMaxSeconds)
	return err
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
	var s models.DashboardStats
	db.conn.QueryRow(`SELECT COUNT(*) FROM message_logs WHERE shop_domain=? AND status='sent'`, shop).Scan(&s.TotalMessagesSent)
	db.conn.QueryRow(`SELECT COUNT(*) FROM message_logs WHERE shop_domain=? AND status='sent' AND DATE(created_at)=DATE('now')`, shop).Scan(&s.MessagesToday)
	db.conn.QueryRow(`SELECT COUNT(*) FROM automations WHERE shop_domain=? AND is_active=1`, shop).Scan(&s.ActiveAutomations)
	db.conn.QueryRow(`SELECT COUNT(*) FROM contacts WHERE shop_domain=? AND opted_out=0`, shop).Scan(&s.TotalContacts)
	return s, nil
}
