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

// GetShopToken returns the stored access token for a shop, or "" if not found.
func (db *DB) GetShopToken(shop string) string {
	var t string
	db.conn.QueryRow(`SELECT access_token FROM shop_tokens WHERE shop_domain=?`, shop).Scan(&t)
	return t
}
