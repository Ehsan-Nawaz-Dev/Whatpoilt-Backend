package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/whatpilot/backend/middleware"
	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
)

type SupportHandler struct {
	db *store.DB
}

func NewSupportHandler(db *store.DB) *SupportHandler {
	return &SupportHandler{db: db}
}

// POST /api/support
func (h *SupportHandler) SubmitTicket(c *gin.Context) {
	shop := middleware.ShopFrom(c)
	var req struct {
		Subject string `json:"subject" binding:"required"`
		Message string `json:"message" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.CreateSupportMessage(shop, req.Subject, req.Message); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /api/public/support-info
func (h *SupportHandler) GetSupportInfo(c *gin.Context) {
	info := h.db.GetSupportInfo()
	c.JSON(http.StatusOK, info)
}

// GET /api/public/faqs
func (h *SupportHandler) ListPublicFAQs(c *gin.Context) {
	list, err := h.db.ListFAQs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if list == nil {
		list = []models.FAQ{}
	}
	c.JSON(http.StatusOK, list)
}

// ─── Admin Endpoints ─────────────────────────────────────────────────────────

// GET /admin/support
func (h *SupportHandler) ListTickets(c *gin.Context) {
	list, err := h.db.ListSupportMessages()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if list == nil {
		list = []models.SupportMessage{}
	}
	c.JSON(http.StatusOK, list)
}

// PUT /admin/support-info
func (h *SupportHandler) UpdateSupportInfo(c *gin.Context) {
	var info models.SupportInfo
	if err := c.ShouldBindJSON(&info); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.SaveSupportInfo(info); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, info)
}

// GET /admin/faqs
func (h *SupportHandler) ListAdminFAQs(c *gin.Context) {
	list, err := h.db.ListFAQs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if list == nil {
		list = []models.FAQ{}
	}
	c.JSON(http.StatusOK, list)
}

// POST /admin/faqs
func (h *SupportHandler) CreateFAQ(c *gin.Context) {
	var req struct {
		Question  string `json:"question" binding:"required"`
		Answer    string `json:"answer" binding:"required"`
		SortOrder int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	faq, err := h.db.CreateFAQ(req.Question, req.Answer, req.SortOrder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, faq)
}

// PUT /admin/faqs/:id
func (h *SupportHandler) UpdateFAQ(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Question  string `json:"question" binding:"required"`
		Answer    string `json:"answer" binding:"required"`
		SortOrder int    `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.UpdateFAQ(id, req.Question, req.Answer, req.SortOrder); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DELETE /admin/faqs/:id
func (h *SupportHandler) DeleteFAQ(c *gin.Context) {
	id := c.Param("id")
	if err := h.db.DeleteFAQ(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
