package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
)

type TemplateHandler struct{ db *store.DB }

func NewTemplateHandler(db *store.DB) *TemplateHandler { return &TemplateHandler{db: db} }

// GET /api/templates
func (h *TemplateHandler) List(c *gin.Context) {
	items, err := h.db.ListTemplates(middleware.ShopFrom(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if items == nil {
		items = []models.Template{}
	}
	c.JSON(http.StatusOK, items)
}

// POST /api/templates
func (h *TemplateHandler) Create(c *gin.Context) {
	var req struct {
		Name        string             `json:"name" binding:"required"`
		Content     string             `json:"content" binding:"required"`
		MessageType models.MessageType `json:"message_type"`
		Options     []string           `json:"options"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	item, err := h.db.CreateTemplate(
		middleware.ShopFrom(c), req.Name, req.Content, req.MessageType, req.Options)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, item)
}

// PUT /api/templates/:id
func (h *TemplateHandler) Update(c *gin.Context) {
	var req struct {
		Name        string             `json:"name" binding:"required"`
		Content     string             `json:"content" binding:"required"`
		MessageType models.MessageType `json:"message_type"`
		Options     []string           `json:"options"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.UpdateTemplate(
		c.Param("id"), middleware.ShopFrom(c),
		req.Name, req.Content, req.MessageType, req.Options); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

// PATCH /api/templates/:id/toggle
func (h *TemplateHandler) Toggle(c *gin.Context) {
	var req struct {
		IsActive bool `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.ToggleTemplate(c.Param("id"), middleware.ShopFrom(c), req.IsActive); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"is_active": req.IsActive})
}

// DELETE /api/templates/:id
func (h *TemplateHandler) Delete(c *gin.Context) {
	if err := h.db.DeleteTemplate(c.Param("id"), middleware.ShopFrom(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// POST /api/templates/seed
// Seeds the 9 default templates for the shop (idempotent).
func (h *TemplateHandler) Seed(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	if err := h.db.SeedDefaultTemplates(shop); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Return the updated list so the frontend can refresh in one round-trip.
	items, _ := h.db.ListTemplates(shop)
	if items == nil {
		items = []models.Template{}
	}
	c.JSON(http.StatusOK, items)
}

// GET /api/templates/seeded — checks whether defaults have been loaded.
func (h *TemplateHandler) IsSeeded(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"seeded": h.db.DefaultTemplatesSeeded(middleware.ShopFrom(c)),
	})
}
