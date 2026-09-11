package gamecatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
)

// Bitmap describes one logical bitmap record from the browser asset manifest.
// Width and height are included so a caller can render the same native asset
// without reimplementing the Web manifest parser.
type Bitmap struct {
	File     string `json:"file"`
	Physical int    `json:"physical"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	XOffset  int    `json:"xoffset"`
	YOffset  int    `json:"yoffset"`
}

// Asset is a resolved bitmap reference.  File is relative to the Web asset
// root (for example bitmaps/bitmap_7246.png).
type Asset struct {
	LogicalID  int    `json:"logical_id"`
	PhysicalID int    `json:"physical_id"`
	File       string `json:"file"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	XOffset    int    `json:"xoffset"`
	YOffset    int    `json:"yoffset"`
}

// Manifest is the subset of client/web/assets/original/manifest.json needed
// to resolve catalog image IDs.  BitmapAliases is important for itemset:
// item image numbers are logical bmp_number values and usually differ from
// the physical filename in the browser asset pack.
type Manifest struct {
	Bitmaps       map[string]Bitmap `json:"bitmaps"`
	BitmapAliases map[string]string `json:"bitmap_aliases"`
	ActorBitmaps  map[string]string `json:"actor_bitmaps"`
	AlbumGraphics map[string]string `json:"album_graphics"`
}

// UnmarshalJSON accepts both forms emitted by older and newer asset builders:
// bitmap_aliases historically used JSON strings ("9136"), while some
// generated manifests write the same physical number as a JSON number (9136).
// Keep the public map convenient for callers while accepting both wire forms.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	type rawManifest struct {
		Bitmaps       map[string]Bitmap          `json:"bitmaps"`
		BitmapAliases map[string]json.RawMessage `json:"bitmap_aliases"`
		ActorBitmaps  map[string]string          `json:"actor_bitmaps"`
		AlbumGraphics map[string]string          `json:"album_graphics"`
	}
	var raw rawManifest
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	aliases := make(map[string]string, len(raw.BitmapAliases))
	for key, value := range raw.BitmapAliases {
		text, err := stringOrNumber(value)
		if err != nil {
			return fmt.Errorf("bitmap alias %q: %w", key, err)
		}
		aliases[key] = text
	}
	m.Bitmaps = raw.Bitmaps
	m.BitmapAliases = aliases
	m.ActorBitmaps = raw.ActorBitmaps
	m.AlbumGraphics = raw.AlbumGraphics
	return nil
}

func stringOrNumber(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", fmt.Errorf("empty alias")
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		if _, err := strconv.Atoi(value); err != nil {
			return "", fmt.Errorf("invalid physical bitmap %q", value)
		}
		return value, nil
	}
	var value json.Number
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	if _, err := strconv.Atoi(string(value)); err != nil {
		return "", fmt.Errorf("invalid physical bitmap %q", value)
	}
	return string(value), nil
}

// LoadManifest decodes a browser asset manifest from a file.
func LoadManifest(path string) (*Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("gamecatalog: open manifest %s: %w", path, err)
	}
	defer file.Close()
	return ParseManifest(file)
}

// ParseManifest decodes a browser asset manifest from r.
func ParseManifest(r io.Reader) (*Manifest, error) {
	var manifest Manifest
	if err := json.NewDecoder(r).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("gamecatalog: decode asset manifest: %w", err)
	}
	if manifest.Bitmaps == nil {
		manifest.Bitmaps = make(map[string]Bitmap)
	}
	if manifest.BitmapAliases == nil {
		manifest.BitmapAliases = make(map[string]string)
	}
	if manifest.ActorBitmaps == nil {
		manifest.ActorBitmaps = make(map[string]string)
	}
	if manifest.AlbumGraphics == nil {
		manifest.AlbumGraphics = make(map[string]string)
	}
	return &manifest, nil
}

// ResolveItem resolves an itemset logical bitmap ID through Web aliases.
// Pet sprite IDs must instead be resolved through ParsePetPreviews: numeric
// actor_bitmaps entries can collide with unrelated physical bitmaps.
func (m *Manifest) ResolveItem(logicalID int) (Asset, bool) {
	if m == nil {
		return Asset{}, false
	}
	logicalKey := strconv.Itoa(logicalID)
	physicalID := logicalID
	if alias, ok := m.BitmapAliases[logicalKey]; ok {
		value, err := strconv.Atoi(alias)
		if err == nil {
			physicalID = value
		}
	}
	bitmap, ok := m.Bitmaps[strconv.Itoa(physicalID)]
	if !ok {
		// Some manifests emit a logical entry directly without an alias.
		bitmap, ok = m.Bitmaps[logicalKey]
		if !ok {
			return Asset{}, false
		}
		physicalID = bitmap.Physical
		if physicalID == 0 {
			physicalID = logicalID
		}
	}
	return Asset{
		LogicalID:  logicalID,
		PhysicalID: physicalID,
		File:       bitmap.File,
		Width:      bitmap.Width,
		Height:     bitmap.Height,
		XOffset:    bitmap.XOffset,
		YOffset:    bitmap.YOffset,
	}, true
}
