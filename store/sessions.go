package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/whatpilot/backend/config"
)

// StoreSession saves (or replaces) a Shopify OAuth session.
// data is the full JSON string of the session object.
func (db *DB) StoreSession(id, shop, data string) error {
	_, err := db.conn.Exec(`
		INSERT INTO shopify_sessions(id, shop, data, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			shop       = excluded.shop,
			data       = excluded.data,
			updated_at = excluded.updated_at`,
		id, shop, data, time.Now())
	return err
}

// LoadSession returns the raw JSON for a session, or "" if not found.
func (db *DB) LoadSession(id string) string {
	var data string
	db.conn.QueryRow(`SELECT data FROM shopify_sessions WHERE id=?`, id).Scan(&data)
	return data
}

// DeleteSession removes a single session by ID.
func (db *DB) DeleteSession(id string) error {
	_, err := db.conn.Exec(`DELETE FROM shopify_sessions WHERE id=?`, id)
	return err
}

// DeleteSessions removes multiple sessions by ID.
func (db *DB) DeleteSessions(ids []string) error {
	for _, id := range ids {
		db.conn.Exec(`DELETE FROM shopify_sessions WHERE id=?`, id)
	}
	return nil
}

// TokenHealth describes a shop's background-refresh readiness.
//
// A shop can only have its Shopify token renewed silently from a background
// webhook if its offline session carries a refresh_token. Without one (or with
// no offline session at all — e.g. a legacy non-expiring token), the token can
// only be refreshed the next time the merchant opens the app, so background
// order-tagging will fail until then.
type TokenHealth struct {
	Shop            string     `json:"shop"`
	HasOfflineToken bool       `json:"has_offline_token"`
	HasRefreshToken bool       `json:"has_refresh_token"`
	Expires         *time.Time `json:"expires,omitempty"`
	AtRisk          bool       `json:"at_risk"`
	Reason          string     `json:"reason,omitempty"`
}

// OfflineTokenHealth inspects every known shop and reports whether its offline
// session carries a refresh token (and is therefore safe to renew in the
// background). Shops flagged AtRisk only self-heal when the merchant reopens the
// app. Results are sorted at-risk first, then by shop.
func (db *DB) OfflineTokenHealth() ([]TokenHealth, error) {
	health := map[string]TokenHealth{}

	// 1. Inspect stored offline sessions.
	rows, err := db.conn.Query(`SELECT shop, data FROM shopify_sessions WHERE id LIKE 'offline_%'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var shop, data string
		if rows.Scan(&shop, &data) != nil {
			continue
		}
		var s map[string]interface{}
		if json.Unmarshal([]byte(data), &s) != nil {
			continue
		}
		accessToken, _ := s["accessToken"].(string)
		refreshToken, _ := s["refreshToken"].(string)
		h := TokenHealth{
			Shop:            shop,
			HasOfflineToken: accessToken != "",
			HasRefreshToken: refreshToken != "",
		}
		if expStr, ok := s["expires"].(string); ok {
			if exp, perr := time.Parse(time.RFC3339, expStr); perr == nil {
				h.Expires = &exp
			}
		}
		if h.HasOfflineToken && !h.HasRefreshToken {
			h.AtRisk = true
			h.Reason = "offline session has no refresh token — can't renew from a background webhook"
		}
		health[shop] = h
	}
	rows.Close()

	// 2. Catch shops that have a token row but no offline session at all
	//    (e.g. only a legacy non-expiring token) — the worst case.
	trows, err := db.conn.Query(`SELECT shop_domain FROM shop_tokens`)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var shop string
		if trows.Scan(&shop) != nil {
			continue
		}
		if _, ok := health[shop]; !ok {
			health[shop] = TokenHealth{
				Shop:   shop,
				AtRisk: true,
				Reason: "no offline session — only a fallback/legacy token; reconnect required to get a rotating token",
			}
		}
	}
	trows.Close()

	out := make([]TokenHealth, 0, len(health))
	for _, h := range health {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AtRisk != out[j].AtRisk {
			return out[i].AtRisk // at-risk first
		}
		return out[i].Shop < out[j].Shop
	})
	return out, nil
}

// FindSessionsByShop returns all session JSON blobs for a given shop.
func (db *DB) FindSessionsByShop(shop string) ([]string, error) {
	rows, err := db.conn.Query(`SELECT data FROM shopify_sessions WHERE shop=?`, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var data string
		rows.Scan(&data)
		out = append(out, data)
	}
	return out, nil
}

// RefreshOfflineTokenForShop manually uses the refresh_token from the DB to get a new access_token
func (db *DB) RefreshOfflineTokenForShop(shop string) (string, error) {
	offlineID := "offline_" + shop
	raw := db.LoadSession(offlineID)
	if raw == "" {
		return "", fmt.Errorf("no offline session found for shop")
	}

	var session map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return "", err
	}

	refreshToken, _ := session["refreshToken"].(string)
	if refreshToken == "" {
		return "", fmt.Errorf("no refresh token in session")
	}

	if config.App.ShopifyAPIKey == "" || config.App.ShopifyAPISecret == "" {
		return "", fmt.Errorf("SHOPIFY_API_KEY or SHOPIFY_API_SECRET not set in backend")
	}

	payload := map[string]string{
		"client_id":     config.App.ShopifyAPIKey,
		"client_secret": config.App.ShopifyAPISecret,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://%s/admin/oauth/access_token", shop)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to refresh token, status %d: %s", resp.StatusCode, string(respBytes))
	}

	var respData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respData); err != nil {
		return "", err
	}

	// Update session map
	session["accessToken"] = respData.AccessToken
	if respData.RefreshToken != "" {
		session["refreshToken"] = respData.RefreshToken
	}
	
	// Update expires to current time + expires_in
	expiresTime := time.Now().Add(time.Duration(respData.ExpiresIn) * time.Second)
	session["expires"] = expiresTime.Format(time.RFC3339)

	newData, _ := json.Marshal(session)
	if err := db.StoreSession(offlineID, shop, string(newData)); err != nil {
		return "", err
	}

	// Also update shop_tokens so the rest of the backend uses the new token
	_, _ = db.conn.Exec(`
		INSERT INTO shop_tokens(shop_domain, access_token, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(shop_domain) DO UPDATE SET
			access_token = excluded.access_token,
			updated_at   = excluded.updated_at`,
		shop, respData.AccessToken, time.Now())

	// A successful refresh means the shop's offline grant is healthy again.
	_ = db.ClearShopReauth(shop)

	return respData.AccessToken, nil
}

// GetFreshTokenForShop extracts the access token from the most recently updated
// Shopify offline session for this shop. Proactively refreshes if it's about to expire.
func (db *DB) GetFreshTokenForShop(shop string) string {
	offlineID := "offline_" + shop
	if raw := db.LoadSession(offlineID); raw != "" {
		var session map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &session); err == nil {
			accessToken, _ := session["accessToken"].(string)

			// Proactively refresh the token if it expires in less than 5 minutes
			if expiresStr, ok := session["expires"].(string); ok {
				if expires, err := time.Parse(time.RFC3339, expiresStr); err == nil {
					if time.Until(expires) < 5*time.Minute {
						slog.Info("offline token about to expire, proactively refreshing", "shop", shop)
						newToken, err := db.RefreshOfflineTokenForShop(shop)
						if err == nil && newToken != "" {
							return newToken
						}
						slog.Error("failed to refresh token proactively, falling back to old token", "shop", shop, "err", err)
					}
				}
			}

			return accessToken
		}
	}
	return ""
}
