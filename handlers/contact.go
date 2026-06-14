package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
)

type ContactHandler struct{ db *store.DB }

func NewContactHandler(db *store.DB) *ContactHandler { return &ContactHandler{db: db} }

// GET /api/contacts
func (h *ContactHandler) List(c *gin.Context) {
	items, err := h.db.ListContacts(middleware.ShopFrom(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if items == nil {
		items = []models.Contact{}
	}
	c.JSON(http.StatusOK, items)
}

// POST /api/contacts
func (h *ContactHandler) Create(c *gin.Context) {
	var req struct {
		Name  string `json:"name"`
		Phone string `json:"phone" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	item, err := h.db.UpsertContact(middleware.ShopFrom(c), req.Name, req.Phone, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, item)
}

// DELETE /api/contacts/:id
func (h *ContactHandler) Delete(c *gin.Context) {
	if err := h.db.DeleteContact(c.Param("id"), middleware.ShopFrom(c)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
