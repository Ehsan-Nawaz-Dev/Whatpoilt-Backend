package store

import "time"

// SetShopToken saves (or updates) the Shopify access token for a shop.
// Called by the frontend after every OAuth so we always have a fresh token.
func (db *DB) SetShopToken(shop, token string) error {
	_, err := db.conn.Exec(`
		INSERT INTO shop_tokens(shop_domain, access_token, updated_at)
		VALUES(?,?,?)
		ON CONFLICT(shop_domain) DO UPDATE SET
			access_token=excluded.access_token,
			updated_at=excluded.updated_at`,
		shop, token, time.Now())
	return err
}

// ReauthStatus is the per-shop Shopify re-authorization state.
type ReauthStatus struct {
	NeedsReauth bool
	Reason      string
	DetectedAt  *time.Time
}

// FlagShopReauth marks a shop as needing Shopify re-authorization. Called when
// an Admin API call is rejected because the stored token is invalid/expired or a
// deprecated non-expiring token that Shopify no longer accepts. Idempotent: the
// detected_at timestamp is only set the first time so we keep the original time.
func (db *DB) FlagShopReauth(shop, reason string) error {
	_, err := db.conn.Exec(`
		INSERT INTO shop_tokens(shop_domain, access_token, needs_reauth, reauth_reason, reauth_detected_at, updated_at)
		VALUES(?, '', 1, ?, ?, ?)
		ON CONFLICT(shop_domain) DO UPDATE SET
			needs_reauth = 1,
			reauth_reason = excluded.reauth_reason,
			reauth_detected_at = COALESCE(shop_tokens.reauth_detected_at, excluded.reauth_detected_at),
			updated_at = excluded.updated_at`,
		shop, reason, time.Now(), time.Now())
	return err
}

// ClearShopReauth clears the re-auth flag for a shop. Called when a fresh, valid
// offline token arrives (new OAuth or token refresh).
func (db *DB) ClearShopReauth(shop string) error {
	_, err := db.conn.Exec(`
		UPDATE shop_tokens
		SET needs_reauth = 0, reauth_reason = '', reauth_detected_at = NULL, updated_at = ?
		WHERE shop_domain = ?`,
		time.Now(), shop)
	return err
}

// GetReauthStatus returns the current re-auth state for a shop.
func (db *DB) GetReauthStatus(shop string) ReauthStatus {
	var (
		needs      int
		reason     string
		detectedAt *time.Time
	)
	db.conn.QueryRow(`
		SELECT needs_reauth, reauth_reason, reauth_detected_at
		FROM shop_tokens WHERE shop_domain = ?`, shop).
		Scan(&needs, &reason, &detectedAt)
	return ReauthStatus{NeedsReauth: needs == 1, Reason: reason, DetectedAt: detectedAt}
}

// GetShopToken returns the best available access token for a shop.
// Priority: fresh session token (rotated by the Shopify library) → shop_tokens fallback.
// Using the session as primary prevents deprecated permanent offline tokens from being used.
func (db *DB) GetShopToken(shop string) string {
	if tok := db.GetFreshTokenForShop(shop); tok != "" {
		return tok
	}
	var t string
	db.conn.QueryRow(`SELECT access_token FROM shop_tokens WHERE shop_domain=?`, shop).Scan(&t)
	return t
}
