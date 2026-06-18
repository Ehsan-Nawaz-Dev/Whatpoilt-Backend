package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
)

type InboxHandler struct {
	registry *whatsapp.Registry
	db       *store.DB
}

func NewInboxHandler(registry *whatsapp.Registry, db *store.DB) *InboxHandler {
	return &InboxHandler{registry: registry, db: db}
}

// GET /api/whatsapp/chats
func (h *InboxHandler) ListActiveChats(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	sessions, err := h.db.ListActiveChats(shop)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if sessions == nil {
		sessions = []store.ChatSession{}
	}
	c.JSON(http.StatusOK, sessions)
}

// GET /api/whatsapp/chats/:phone
func (h *InboxHandler) GetChatHistory(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	phone := c.Param("phone")
	if phone == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing phone param"})
		return
	}
	history, err := h.db.GetChatHistory(shop, phone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if history == nil {
		history = []models.MessageLog{}
	}
	c.JSON(http.StatusOK, history)
}
