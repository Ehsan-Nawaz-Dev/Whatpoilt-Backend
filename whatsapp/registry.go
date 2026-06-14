package whatsapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Registry manages one Manager per Shopify store so every merchant has their
// own independent WhatsApp phone number and session.
type Registry struct {
	mu      sync.RWMutex
	stores  map[string]*Manager
	sessDir string // data/sessions/
	optOut  shopOptOutFunc
}

func NewRegistry(sessDir string) (*Registry, error) {
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		return nil, fmt.Errorf("create sessions dir: %w", err)
	}
	return &Registry{
		stores:  make(map[string]*Manager),
		sessDir: sessDir,
	}, nil
}

// For returns the Manager for the given shop, creating it if it does not exist.
func (r *Registry) For(shop string) (*Manager, error) {
	// fast path — already initialised
	r.mu.RLock()
	if m, ok := r.stores[shop]; ok {
		r.mu.RUnlock()
		return m, nil
	}
	r.mu.RUnlock()

	// slow path — create new manager
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.stores[shop]; ok {
		return m, nil // another goroutine beat us
	}

	dbPath := filepath.Join(r.sessDir, shopToFilename(shop)+".db")
	mgr, err := NewManager(dbPath)
	if err != nil {
		return nil, fmt.Errorf("init manager for %s: %w", shop, err)
	}
	// Wire opt-out callback with the shop domain captured in closure.
	if r.optOut != nil {
		shopCopy := shop
		mgr.SetOptOutHandler(func(phone string) { r.optOut(shopCopy, phone) })
	}
	r.stores[shop] = mgr
	return mgr, nil
}

// Remove disconnects and removes the manager for a shop (called on APP_UNINSTALLED).
func (r *Registry) Remove(shop string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.stores[shop]; ok {
		m.Disconnect()
		delete(r.stores, shop)
	}
}

type shopOptOutFunc func(shop, phone string)

// SetOptOutHandler injects a callback that fires when a customer sends an opt-out
// keyword. The registry forwards it into every manager it creates.
func (r *Registry) SetOptOutHandler(fn shopOptOutFunc) {
	r.mu.Lock()
	r.optOut = fn
	r.mu.Unlock()
}

// ConnectAll restores sessions for every shop that has a persisted session DB.
// Called once at startup — errors are non-fatal (shop simply starts disconnected).
func (r *Registry) ConnectAll() {
	entries, _ := os.ReadDir(r.sessDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		shop := filenameToShop(strings.TrimSuffix(e.Name(), ".db"))
		mgr, err := r.For(shop)
		if err != nil {
			continue
		}
		go func() { _ = mgr.ConnectExisting() }()
	}
}

// StatusMap returns a snapshot of {shop → status} for all active managers.
// Does NOT create new managers — only shops that have connected at least once appear.
func (r *Registry) StatusMap() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.stores))
	for shop, mgr := range r.stores {
		out[shop] = string(mgr.GetStatus())
	}
	return out
}

// shopToFilename converts "my-store.myshopify.com" → "my-store_myshopify_com"
func shopToFilename(shop string) string {
	r := strings.NewReplacer(".", "_", "/", "_", "\\", "_", ":", "_")
	return r.Replace(shop)
}

func filenameToShop(name string) string {
	// "my-store_myshopify_com" → "my-store.myshopify.com"
	// Simple heuristic: replace last two underscores that look like TLD
	parts := strings.Split(name, "_")
	if len(parts) >= 3 {
		return strings.Join(parts[:len(parts)-2], "-") + "." +
			parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return name
}
