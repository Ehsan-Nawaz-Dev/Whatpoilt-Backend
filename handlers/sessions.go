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
		IsOnline    bool   `json:"isOnline"`
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
	// Only offline sessions have long-lived, valid tokens for background tasks.
	//
	// NOTE: do NOT migrate the token to an expiring one here. The frontend re-stores
	// its own (original) token on every load, so migrating + revoking it server-side
	// desyncs the two and leaves a revoked token → 401s. Token migration happens
	// lazily on a real 403 ("Non-expiring access tokens") instead.
	if s.AccessToken != "" && !s.IsOnline {
		_ = h.db.SetShopToken(s.Shop, s.AccessToken)
		// A fresh offline token just arrived (new OAuth) — the shop is reconnected,
		// so clear any pending re-auth flag set by a previous failed API call.
		_ = h.db.ClearShopReauth(s.Shop)
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
