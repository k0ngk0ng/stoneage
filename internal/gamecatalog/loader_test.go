package gamecatalog

import (
	"errors"
	"testing"
)

func TestLoaderRetriesFailuresAndCachesSuccess(t *testing.T) {
	want := &Catalog{Items: []Item{{Entry: Entry{ID: 7, Kind: KindItem}}}}
	loadCalls := 0
	loader := NewLoader(func() (*Catalog, error) {
		loadCalls++
		if loadCalls == 1 {
			return nil, errors.New("data files are not ready")
		}
		return want, nil
	})

	if catalog, err := loader.Load(); err == nil || catalog != nil {
		t.Fatalf("first Load() = %v, %v; want an error and no catalog", catalog, err)
	}
	catalog, err := loader.Load()
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if catalog != want {
		t.Fatalf("second Load() returned %p, want %p", catalog, want)
	}
	cached, err := loader.Load()
	if err != nil || cached != want {
		t.Fatalf("cached Load() = %p, %v; want %p, nil", cached, err, want)
	}
	if loadCalls != 2 {
		t.Fatalf("load callback calls = %d, want 2", loadCalls)
	}
}

func TestLoaderRejectsNilSuccess(t *testing.T) {
	loadCalls := 0
	loader := NewLoader(func() (*Catalog, error) {
		loadCalls++
		return nil, nil
	})
	if _, err := loader.Load(); err == nil {
		t.Fatal("Load() succeeded with nil catalog")
	}
	if _, err := loader.Load(); err == nil {
		t.Fatal("Load() cached a nil catalog")
	}
	if loadCalls != 2 {
		t.Fatalf("load callback calls = %d, want 2", loadCalls)
	}
}
