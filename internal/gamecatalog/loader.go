package gamecatalog

import (
	"errors"
	"sync"
)

// Loader lazily loads a catalog and keeps the first successful result.
//
// Loading can race the first initialization of the native game server. Failed
// loads are deliberately not cached, so a later request can retry after the
// server has finished creating its data files. Calls are serialized while a
// load is in progress; no background goroutine is started.
type Loader struct {
	mu      sync.Mutex
	load    func() (*Catalog, error)
	catalog *Catalog
}

// NewLoader returns a lazy catalog loader around load.
func NewLoader(load func() (*Catalog, error)) *Loader {
	return &Loader{load: load}
}

// Load returns the cached catalog or tries to load it once. A nil catalog is
// treated as an error and, like any other load error, will be retried later.
func (loader *Loader) Load() (*Catalog, error) {
	if loader == nil {
		return nil, errors.New("gamecatalog: nil loader")
	}
	loader.mu.Lock()
	defer loader.mu.Unlock()
	if loader.catalog != nil {
		return loader.catalog, nil
	}
	if loader.load == nil {
		return nil, errors.New("gamecatalog: catalog loader is not configured")
	}
	catalog, err := loader.load()
	if err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errors.New("gamecatalog: catalog loader returned nil catalog")
	}
	loader.catalog = catalog
	return catalog, nil
}
