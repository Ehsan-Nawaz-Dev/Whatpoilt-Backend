package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
)

type AutomationHandler struct{ db *store.DB }

func NewAutomationHandler(db *store.DB) *AutomationHandler { return &AutomationHandler{db: db} }

// GET /api/automations
func (h *AutomationHandler) List(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	// Auto-seed the 4 default automations on first load (idempotent).
	if !h.db.DefaultAutomationsSeeded(shop) {
		if !h.db.DefaultTemplatesSeeded(shop) {
			h.db.SeedDefaultTemplates(shop)
		}
		h.db.SeedAutomations(shop)
	}
	items, err := h.db.ListAutomations(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if items == nil {
		items = []models.Automation{}
	}
	c.JSON(http.StatusOK, items)
}

// POST /api/automations
func (h *AutomationHandler) Create(c *gin.Context) {
	var req struct {
		Name         string             `json:"name" binding:"required"`
		TriggerType  models.TriggerType `json:"trigger_type" binding:"required"`
		TemplateID   string             `json:"template_id" binding:"required"`
		DelayMinutes int                `json:"delay_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	item, err := h.db.CreateAutomation(middleware.ShopFrom(c), req.Name, req.TriggerType, req.TemplateID, req.DelayMinutes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, item)
}

// PUT /api/automations/:id
func (h *AutomationHandler) Update(c *gin.Context) {
	var req struct {
		Name         string `json:"name" binding:"required"`
		IsActive     bool   `json:"is_active"`
		TemplateID   string `json:"template_id"`
		DelayMinutes int    `json:"delay_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.UpdateAutomation(c.Param("id"), middleware.ShopFrom(c), req.Name, req.IsActive, req.DelayMinutes, req.TemplateID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// DELETE /api/automations/:id
func (h *AutomationHandler) Delete(c *gin.Context) {
	if err := h.db.DeleteAutomation(c.Param("id"), middleware.ShopFrom(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// PATCH /api/automations/:id/toggle
func (h *AutomationHandler) Toggle(c *gin.Context) {
	if err := h.db.ToggleAutomation(c.Param("id"), middleware.ShopFrom(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// PATCH /api/automations/:id/template
func (h *AutomationHandler) UpdateTemplate(c *gin.Context) {
	var req struct {
		TemplateID string `json:"template_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.UpdateAutomationTemplate(c.Param("id"), middleware.ShopFrom(c), req.TemplateID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
