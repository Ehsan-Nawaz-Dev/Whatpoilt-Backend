package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/store"
)

func readBody(c *gin.Context) ([]byte, error) {
	return io.ReadAll(c.Request.Body)
}

type SessionHandler struct{ db *store.DB }

func NewSessionHandler(db *store.DB) *SessionHandler { return &SessionHandler{db: db} }

// GET /internal/sessions/:id
func (h *SessionHandler) Load(c *gin.Context) {
	data := h.db.LoadSession(c.Param("id"))
	if data == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.Data(http.StatusOK, "application/json", []byte(data))
}

// POST /internal/sessions
func (h *SessionHandler) Store(c *gin.Context) {
	body, err := readBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var s struct {
		ID          string `json:"id"`
		Shop        string `json:"shop"`
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(body, &s); err != nil || s.ID == "" || s.Shop == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id and shop required"})
		return
	}
	if err := h.db.StoreSession(s.ID, s.Shop, string(body)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Mirror the access token to shop_tokens so webhook handlers (order tagging,
	// refund lookups, etc.) always have a fresh token even without the register-shop call.
	if s.AccessToken != "" {
		_ = h.db.SetShopToken(s.Shop, s.AccessToken)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DELETE /internal/sessions/:id
func (h *SessionHandler) Delete(c *gin.Context) {
	h.db.DeleteSession(c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// POST /internal/sessions/delete-multi
func (h *SessionHandler) DeleteMulti(c *gin.Context) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.db.DeleteSessions(req.IDs)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /internal/sessions/by-shop/:shop
func (h *SessionHandler) ByShop(c *gin.Context) {
	sessions, err := h.db.FindSessionsByShop(c.Param("shop"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Return as JSON array of raw session objects.
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.WriteString("[")
	for i, s := range sessions {
		if i > 0 {
			c.Writer.WriteString(",")
		}
		c.Writer.WriteString(s)
	}
	c.Writer.WriteString("]")
}
