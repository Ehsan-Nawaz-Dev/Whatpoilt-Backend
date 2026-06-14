package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const ShopKey = "shop_domain"

// Shop extracts the X-Shop-Domain header and verifies the request carries the
// shared API key (Authorization: Bearer <BACKEND_API_KEY>) so the backend
// only accepts calls from the authenticated Remix frontend — not arbitrary clients.
func Shop(apiKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Verify shared API key between frontend and backend.
		if apiKey != "" {
			bearer := c.GetHeader("Authorization")
			if len(bearer) < 8 || bearer[:7] != "Bearer " || bearer[7:] != apiKey {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error": "invalid or missing Authorization header",
				})
				return
			}
		}

		shop := c.GetHeader("X-Shop-Domain")
		if shop == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error": "missing X-Shop-Domain header",
			})
			return
		}
		c.Set(ShopKey, shop)
		c.Next()
	}
}

func ShopFrom(c *gin.Context) string { return c.GetString(ShopKey) }
