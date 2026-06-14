package handlers

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/config"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
)

// AdminHandler serves the /admin/* and /api/public/* route groups.
type AdminHandler struct {
	db        *store.DB
	registry  *whatsapp.Registry
	startTime time.Time
	dbPath    string
}

func NewAdminHandler(db *store.DB, registry *whatsapp.Registry, dbPath string) *AdminHandler {
	h := &AdminHandler{db: db, registry: registry, startTime: time.Now(), dbPath: dbPath}
	// Seed default plans on first boot so the billing page always has data.
	db.SeedDefaultAdminPlans()
	return h
}

// AdminAuth returns a middleware that requires Authorization: Bearer <key>.
// It checks the DB-stored key first (set via /admin/change-password),
// falling back to the env-var key passed at startup.
func AdminAuth(db *store.DB, envKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		active := db.GetAdminKey()
		if active == "" {
			active = envKey
		}
		if active != "" && c.GetHeader("Authorization") != "Bearer "+active {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// ─── Global Stats ─────────────────────────────────────────────────────────────

// GET /admin/stats
func (h *AdminHandler) GlobalStats(c *gin.Context) {
	stats, err := h.db.GetGlobalStats()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, stats)
}

// ─── Shops ────────────────────────────────────────────────────────────────────

// GET /admin/shops
func (h *AdminHandler) ListShops(c *gin.Context) {
	shops, err := h.db.ListShopsWithStats()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	// Enrich with live WA connection status.
	statusMap := h.registry.StatusMap()
	for i := range shops {
		if st, ok := statusMap[shops[i].ShopDomain]; ok {
			shops[i].WAConnected = st == string(whatsapp.StatusConnected)
		}
	}
	if shops == nil {
		shops = []models.ShopStats{}
	}
	c.JSON(200, shops)
}

// DELETE /admin/shops/:shop
func (h *AdminHandler) PurgeShop(c *gin.Context) {
	shop := c.Param("shop")
	if shop == "" {
		c.JSON(400, gin.H{"error": "shop domain required"})
		return
	}
	h.registry.Remove(shop)
	if err := h.db.PurgeShop(shop); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// ─── Plans ────────────────────────────────────────────────────────────────────

// GET /admin/plans
func (h *AdminHandler) ListPlans(c *gin.Context) {
	plans, err := h.db.ListAdminPlans()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if plans == nil {
		plans = []models.AdminPlan{}
	}
	c.JSON(200, plans)
}

// PUT /admin/plans/:key
func (h *AdminHandler) UpdatePlan(c *gin.Context) {
	key := c.Param("key")
	var plan models.AdminPlan
	if err := c.ShouldBindJSON(&plan); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	plan.PlanKey = key
	if err := h.db.UpsertAdminPlan(plan); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, plan)
}

// POST /admin/plans/seed
func (h *AdminHandler) SeedPlans(c *gin.Context) {
	if err := h.db.SeedDefaultAdminPlans(); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	plans, _ := h.db.ListAdminPlans()
	if plans == nil {
		plans = []models.AdminPlan{}
	}
	c.JSON(200, plans)
}

// ─── Announcements ────────────────────────────────────────────────────────────

// GET /admin/announcements
func (h *AdminHandler) ListAnnouncements(c *gin.Context) {
	list, err := h.db.ListAnnouncements()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if list == nil {
		list = []models.Announcement{}
	}
	c.JSON(200, list)
}

// POST /admin/announcements
func (h *AdminHandler) CreateAnnouncement(c *gin.Context) {
	var req struct {
		Title     string  `json:"title" binding:"required"`
		Message   string  `json:"message" binding:"required"`
		Tone      string  `json:"tone"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	var exp *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err == nil {
			exp = &t
		}
	}
	ann, err := h.db.CreateAnnouncement(req.Title, req.Message, req.Tone, exp)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(201, ann)
}

// PUT /admin/announcements/:id
func (h *AdminHandler) UpdateAnnouncement(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Title     string  `json:"title"`
		Message   string  `json:"message"`
		Tone      string  `json:"tone"`
		IsActive  *bool   `json:"is_active"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	var exp *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err == nil {
			exp = &t
		}
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	if err := h.db.UpdateAnnouncement(id, req.Title, req.Message, req.Tone, isActive, exp); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// DELETE /admin/announcements/:id
func (h *AdminHandler) DeleteAnnouncement(c *gin.Context) {
	id := c.Param("id")
	if err := h.db.DeleteAnnouncement(id); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// ─── Profile ──────────────────────────────────────────────────────────────────

// GET /admin/profile
func (h *AdminHandler) GetProfile(c *gin.Context) {
	c.JSON(200, models.AdminProfile{
		Name:      h.db.GetAdminConfigValue("profile_name"),
		AvatarURL: h.db.GetAdminConfigValue("profile_avatar"),
	})
}

// PUT /admin/profile
func (h *AdminHandler) UpdateProfile(c *gin.Context) {
	var req models.AdminProfile
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	h.db.SetAdminConfigValue("profile_name", req.Name)
	h.db.SetAdminConfigValue("profile_avatar", req.AvatarURL)
	c.JSON(200, req)
}

// POST /admin/change-password
func (h *AdminHandler) ChangePassword(c *gin.Context) {
	var req struct {
		CurrentKey string `json:"current_key" binding:"required"`
		NewKey     string `json:"new_key"     binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	// Resolve the currently active key (DB overrides env).
	active := h.db.GetAdminKey()
	if active == "" {
		active = config.App.AdminAPIKey
	}
	if req.CurrentKey != active {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "current key is incorrect"})
		return
	}
	if len(req.NewKey) < 8 {
		c.JSON(400, gin.H{"error": "new key must be at least 8 characters"})
		return
	}
	if err := h.db.SetAdminKey(req.NewKey); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true, "message": "Key updated. Use the new key on your next login."})
}

// ─── Server Status ────────────────────────────────────────────────────────────

// GET /admin/server-status
func (h *AdminHandler) ServerStatus(c *gin.Context) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	statusMap := h.registry.StatusMap()
	connected := 0
	for _, st := range statusMap {
		if st == string(whatsapp.StatusConnected) {
			connected++
		}
	}

	pending, failed := h.db.GetJobStats()

	var dbSizeMB float64
	if info, err := os.Stat(h.dbPath); err == nil {
		dbSizeMB = float64(info.Size()) / (1024 * 1024)
	}

	c.JSON(200, models.ServerStatusData{
		Uptime:        fmtDuration(time.Since(h.startTime)),
		GoVersion:     runtime.Version(),
		MemAllocMB:    float64(ms.Alloc) / (1024 * 1024),
		MemSysMB:      float64(ms.Sys) / (1024 * 1024),
		NumGoroutines: runtime.NumGoroutine(),
		WAConnected:   connected,
		WATotal:       len(statusMap),
		PendingJobs:   pending,
		FailedJobs:    failed,
		DBSizeMB:      math2dp(dbSizeMB),
		Environment:   config.App.Environment,
		Healthy:       true,
	})
}

func fmtDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm %ds", mins, int(d.Seconds())%60)
}

func math2dp(f float64) float64 {
	return float64(int(f*100)) / 100
}

// ─── Public (no auth — called by merchant frontend) ───────────────────────────

// GET /api/public/plans
func (h *AdminHandler) PublicPlans(c *gin.Context) {
	plans, err := h.db.ListAdminPlans()
	if err != nil || len(plans) == 0 {
		// Return hardcoded defaults if DB isn't seeded yet.
		c.JSON(200, models.DefaultAdminPlans())
		return
	}
	c.JSON(200, plans)
}

// GET /api/public/announcements
func (h *AdminHandler) PublicAnnouncements(c *gin.Context) {
	list, err := h.db.GetActiveAnnouncements()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if list == nil {
		list = []models.Announcement{}
	}
	c.JSON(200, list)
}
