package store

import "time"

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
