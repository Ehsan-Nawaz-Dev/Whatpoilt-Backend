package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
)

type KeywordHandler struct{ db *store.DB }

func NewKeywordHandler(db *store.DB) *KeywordHandler { return &KeywordHandler{db: db} }

// GET /api/keywords
func (h *KeywordHandler) List(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	list, err := h.db.ListKeywordReplies(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

// POST /api/keywords
func (h *KeywordHandler) Create(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	var k models.KeywordReply
	if err := c.ShouldBindJSON(&k); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.SaveKeywordReply(shop, k); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"ok": true})
}

// PUT /api/keywords/:id
func (h *KeywordHandler) Update(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	var k models.KeywordReply
	if err := c.ShouldBindJSON(&k); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	k.ID = c.Param("id")
	if err := h.db.SaveKeywordReply(shop, k); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DELETE /api/keywords/:id
func (h *KeywordHandler) Delete(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	if err := h.db.DeleteKeywordReply(shop, c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
